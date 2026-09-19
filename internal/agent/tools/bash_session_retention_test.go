package tools

import (
	"strings"
	"testing"
	"time"
)

// G27 (docs 10 §10): a retired background shell must stop being sized by
// history. Before this, every job an agent had ever run kept its own 4 MiB
// buffer and its map entry for as long as the agent lived.

func waitForCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A shell that exits is trimmed to its tail, and the reader is told *why* the
// bytes are missing — naming the 4 MiB running-cap there would be a false
// explanation of a real loss.
//
// Falsification: drop the trimTo call in retireExited and the size assertion
// below fails with ~200 KiB held.
func TestRetiredShellKeepsOnlyItsTail(t *testing.T) {
	m := newShellManager()
	s, err := m.Start("printf 'BEGIN-MARKER\\n'; yes x | head -c 200000; printf '\\nTHE-END\\n'", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForCondition(t, "the shell to exit", func() bool {
		st, _, _ := s.snapshot()
		return st == statusExited
	})

	s.out.mu.Lock()
	kept := len(s.out.data)
	s.out.mu.Unlock()
	if kept > shellExitedTailBytes {
		t.Fatalf("retired shell holds %d bytes, want <= %d", kept, shellExitedTailBytes)
	}

	out, dropped := s.readNew()
	if !dropped {
		t.Fatal("a reader that never caught up must be told the tail is not the whole story")
	}
	if !strings.Contains(string(out), "THE-END") {
		t.Fatalf("the tail lost the end of the output: %d bytes back", len(out))
	}
	if strings.Contains(string(out), "BEGIN-MARKER") {
		t.Fatal("the trimmed head is still being served; the trim did not happen")
	}
	if note := s.dropNoteText(); !strings.Contains(note, "shell exited") {
		t.Fatalf("the reader was told %q, want the exit trim (not the 4 MiB running cap)", note)
	}
}

// The retired population is capped, and a *running* shell is never part of that
// accounting.
//
// Falsification: remove the delete loop in retireExited and the cap assertion
// below fails (40 exited entries stay in the map).
func TestRetiredShellsAreCappedAndRunningOnesSurvive(t *testing.T) {
	m := newShellManager()
	running, err := m.Start("sleep 30", nil)
	if err != nil {
		t.Fatalf("start running shell: %v", err)
	}
	defer func() { _ = running.kill() }()

	for i := 0; i < shellRetainedExited+8; i++ {
		if _, err := m.Start("true", nil); err != nil {
			t.Fatalf("start exited shell %d: %v", i, err)
		}
	}

	// Wait until every short-lived shell has exited (only the `sleep 30` is left
	// running) AND the cap has settled. Counting the map alone is not enough: a
	// shell whose reaper has not run yet is still "running", and running shells
	// are correctly excluded from the accounting.
	waitForCondition(t, "the exited population to settle at the cap", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		exited, runningCount := 0, 0
		for _, s := range m.shells {
			if s.done.Load() {
				exited++
			} else {
				runningCount++
			}
		}
		return runningCount == 1 && exited <= shellRetainedExited
	})

	m.mu.Lock()
	_, alive := m.shells[running.id]
	total := len(m.shells)
	m.mu.Unlock()
	if !alive {
		t.Fatal("a running shell was dropped; only *exited* ones may be forgotten")
	}
	if total > shellRetainedExited+1 {
		t.Fatalf("shell manager holds %d entries, want <= %d exited + the running one", total, shellRetainedExited)
	}
}
