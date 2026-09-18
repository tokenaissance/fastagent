package sandbox

// The agent's perception of sandbox-side change (docs/文件系统形式化证明/07 §3.11.1).
//
// The asymmetry this pins down: the agent always knows what IT wrote, because
// write_file / edit_file / apply_patch state it in the tool result. It has no equivalent signal
// for a script that edits /workspace inside `exec` — the command output says
// nothing about which files changed. That gap is what made the 2026-09-17
// incident invisible from the agent's side, and closing it is cheaper and more
// robust than trying to prevent every divergence.
//
// So the post-exec sync states the delta: what it moved into the store, and what
// it refused. Both go into the exec result.

// NOTE: sandbox-side DELETIONS are no longer signalled. Detecting "the sandbox
// removed this" needs a record of what it used to hold, and the baseline table
// that provided it was removed for the cross-replica reason in 07 §3.3. A
// store-only upload and a deleted-in-sandbox file are indistinguishable now.
// Recorded as a known gap in docs 08 §5; the fix, if wanted, is a DURABLE
// marker (a store-side manifest), not pod-local memory.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A sandbox edit that the sync moves must be named in the exec output.
func TestExecObservesSandboxChanges(t *testing.T) {
	lp, _, pool := syncFixture(t, "born", "born")
	ctx := context.Background()
	sc := sandboxScope{agentID: "erin"}
	if _, err := lp.getInner(ctx, sc); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	// A script in the sandbox creates one file and edits another.
	pool.current.files["generated.csv"] = []byte("a,b\n1,2\n")
	pool.current.files["report.html"] = []byte("EDITED INSIDE THE SANDBOX")

	ex, err := lp.Get(ctx, "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, execErr := ex.Exec(ctx, "python3 build.py", 30*time.Second)
	if execErr != nil {
		t.Fatalf("exec: %v", execErr)
	}

	// The signal must name both paths: the new artefact and the edited file —
	// the agent cannot infer either from the command it ran.
	for _, want := range []string{"generated.csv", "report.html"} {
		if !strings.Contains(out, want) {
			t.Fatalf("exec result does not name the changed path %q:\n%q", want, out)
		}
	}
	if !strings.Contains(out, "[workspace]") {
		t.Fatalf("exec result carries no workspace report:\n%q", out)
	}
}

// Silence when nothing changed: a signal on every exec would be ignored.
func TestExecIsQuietWhenNothingChanged(t *testing.T) {
	lp, _, _ := syncFixture(t, "born", "born")
	ctx := context.Background()
	sc := sandboxScope{agentID: "erin"}
	if _, err := lp.getInner(ctx, sc); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	ex, err := lp.Get(ctx, "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, err := ex.Exec(ctx, "true", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out, "[workspace]") {
		t.Fatalf("an unchanged workspace produced a signal:\n%q", out)
	}
}

// A refused path must be named too — that is the case where the agent most
// needs to know, and it is precisely the one with no other signal.
func TestExecObservesRefusedPaths(t *testing.T) {
	lp, ws, pool := syncFixture(t, "born", "born")
	ctx := context.Background()
	sc := sandboxScope{agentID: "erin"}
	if _, err := lp.getInner(ctx, sc); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	// A host write whose mirror failed: the store moved, the sandbox still holds
	// the older copy. That difference must not be pushed — and the agent has to
	// hear about it.
	hostWrite(t, ws, "THE HOST WROTE THIS LONGER VERSION")
	pool.current.files["report.html"] = []byte("older sandbox copy")

	ex, err := lp.Get(ctx, "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, err := ex.Exec(ctx, "ls", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "NOT synced") || !strings.Contains(out, "report.html") {
		t.Fatalf("exec result does not report the refused path:\n%q", out)
	}
}

// A change that only surfaced during idle eviction — the sync that runs between
// turns, with no tool result to attach to — must still reach the agent on its
// next tool call. Otherwise "the world changed while you were not looking" is
// invisible, which is the same class of failure as the original incident.
func TestEvictionSignalReachesNextToolResult(t *testing.T) {
	lp, _, pool := syncFixture(t, "born", "born")
	// The carrier is where this signal waits between the eviction and the next
	// tool call. It used to be an in-process map; it is now durable, so the same
	// assertion also holds when another replica (or another process) delivers it
	// — see TestEvictSignalOutlivesThePoolThatProducedIt (docs 09, G3).
	lp.SetSignalStore(newDurableSignals())
	ctx := context.Background()
	sc := sandboxScope{agentID: "erin"}
	if _, err := lp.getInner(ctx, sc); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	// A script writes a file, and the scope goes idle before any tool result
	// can carry it: the eviction sync is the only thing that sees the change.
	pool.current.files["while_away.md"] = []byte("written by a background script")
	lp.flushIfSupported(sc)

	ex, err := lp.Get(ctx, "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, err := ex.Exec(ctx, "true", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "while_away.md") {
		t.Fatalf("the eviction-time change never reached the agent:\n%q", out)
	}

	// And it is delivered ONCE: repeating it every turn would make the signal
	// background noise the model learns to skip.
	out2, err := ex.Exec(ctx, "true", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out2, "while_away.md") {
		t.Fatalf("the eviction signal was delivered twice:\n%q", out2)
	}
}

// A reconcile that could not RUN is itself a state the agent must know: the
// sandbox's changes did not reach the store, so reading the file back returns
// the store's older copy. Production hit this 64 times in one session (the
// snapshot cap) and the agent was never told — it saw only an operator log
// line, then read stale content and concluded its work had vanished.
func TestExecObservesSyncFailure(t *testing.T) {
	lp, _, pool := syncFixture(t, "born", "born")
	ctx := context.Background()
	sc := sandboxScope{agentID: "erin"}
	if _, err := lp.getInner(ctx, sc); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	// A snapshot that fails is what an over-cap /workspace looks like.
	pool.current.snapshotErr = errors.New("workspace snapshot is over the 32.0 MB cap — refusing to flush /workspace after every exec")

	ex, err := lp.Get(ctx, "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, err := ex.Exec(ctx, "python build.py", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "could NOT be synced") {
		t.Fatalf("exec result does not report the failed sync:\n%q", out)
	}
	if !strings.Contains(out, "32.0 MB") {
		t.Fatalf("the signal does not carry the reason:\n%q", out)
	}
	if !strings.Contains(out, "read_file") {
		t.Fatalf("the signal does not warn that the store's copy may be older:\n%q", out)
	}
}

// A rebuilt sandbox must announce itself. The rebuild is otherwise invisible —
// the next exec succeeds normally — while anything that lived only inside the
// old instance (unsynced script output) is gone for good. docs 09 G1/G2.
func TestRebuiltSandboxIsAnnounced(t *testing.T) {
	lp, _, pool := syncFixture(t, "born", "born")
	ctx := context.Background()
	// The fake states a replacement once, the way the E2B executor does after
	// it recreates and rehydrates an expired instance.
	pool.current.replaced = true

	ex, err := lp.Get(ctx, "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, err := ex.Exec(ctx, "true", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "sandbox was REPLACED") {
		t.Fatalf("a rebuilt sandbox produced no signal:\n%q", out)
	}
	if !strings.Contains(out, "never synced") {
		t.Fatalf("the signal does not say what may have been lost:\n%q", out)
	}
	// Once is enough: repeating it every turn would make it background noise.
	out2, err := ex.Exec(ctx, "true", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out2, "sandbox was REPLACED") {
		t.Fatalf("the replacement signal was delivered twice:\n%q", out2)
	}
}
