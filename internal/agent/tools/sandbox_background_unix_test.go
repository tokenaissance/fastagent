//go:build unix

package tools

// The composition in sandbox_background.go runs through `sh -c` on the host
// side of every sandbox backend (docker's DockerSandbox.Exec and e2b's envd
// both exec a shell with the command string). This file runs the REAL composed
// commands against a local /bin/sh, so the mechanism is verified offline and
// not only asserted in string form.
//
// The live counterpart — same flow through a real container and a real e2b
// sandbox — is sandbox_background_e2e_test.go.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// localShellRunner is the same shape as the sandbox backends' exec: a shell
// with a single command string, whose output comes back when the shell exits.
type localShellRunner struct{}

func (localShellRunner) Exec(ctx context.Context, command string, timeout time.Duration) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(execCtx, "sh", "-c", command).CombinedOutput()
	return string(out), err
}

func cleanupJobFiles(t *testing.T, job *sandboxJob) {
	t.Helper()
	t.Cleanup(func() {
		_ = os.Remove(job.logPath)
		_ = os.Remove(job.exitPath)
	})
}

// The production failure this feature exists to fix: an exec call that starts
// a detached job must return immediately, and the job must still be there on
// the next call.
func TestSandboxBackgroundMechanismJobOutlivesTheCall(t *testing.T) {
	ctx := context.Background()
	jobs := newSandboxJobs()

	started := time.Now()
	job, err := jobs.start(ctx, localShellRunner{},
		`i=0; while [ $i -lt 300 ]; do i=$((i+1)); echo tick $i; sleep 0.1; done`)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	cleanupJobFiles(t, job)

	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("launch took %s — the exec call waited for the job instead of detaching it", elapsed)
	}

	// The job is alive and writing, with no help from the process that
	// launched it.
	time.Sleep(1500 * time.Millisecond)
	first, err := job.output(ctx, nil)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if !strings.Contains(first, "[status] running") {
		t.Fatalf("job should still be running, poll said: %q", first)
	}
	firstTicks := tickTimes(t, first)
	if len(firstTicks) < 5 {
		t.Fatalf("expected the job to have produced output after the call returned, got %q", first)
	}

	// A second poll returns only what arrived since the first one.
	time.Sleep(700 * time.Millisecond)
	second, err := job.output(ctx, nil)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	secondTicks := tickTimes(t, second)
	if len(secondTicks) == 0 || secondTicks[0] <= firstTicks[len(firstTicks)-1] {
		t.Fatalf("second poll repeated or skipped output: first=%v second=%v", firstTicks, secondTicks)
	}
	if firstTicks[0] != 1 {
		t.Errorf("the cursor must start at the beginning of the log, first tick = %d", firstTicks[0])
	}
}

func TestSandboxBackgroundMechanismCapturesExitCode(t *testing.T) {
	ctx := context.Background()
	jobs := newSandboxJobs()

	job, err := jobs.start(ctx, localShellRunner{},
		`echo about to fail; exit 3`)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	cleanupJobFiles(t, job)

	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := job.output(ctx, nil)
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		if strings.Contains(out, "[status] exited") {
			if !strings.Contains(out, "[status] exited (code=3)") {
				t.Fatalf("exit code was lost: %q", out)
			}
			if !strings.Contains(out, "about to fail") {
				t.Fatalf("output written before the exit was lost: %q", out)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never reported its exit: %q", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// kill_shell must stop the whole tree, not just the wrapper shell — the job's
// own `sleep` children are what would otherwise keep ticking (and keep the CPU
// busy) after the model thinks it stopped the work.
func TestSandboxBackgroundMechanismKillStopsTheTree(t *testing.T) {
	ctx := context.Background()
	jobs := newSandboxJobs()

	job, err := jobs.start(ctx, localShellRunner{},
		`i=0; while [ $i -lt 3000 ]; do i=$((i+1)); echo tick $i; sleep 0.1; done`)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	cleanupJobFiles(t, job)
	// The reported scope has to match what the host can actually do: a group
	// kill is only safe when the launcher got a session of its own.
	_, setsidErr := exec.LookPath("setsid")
	if setsidErr == nil && !job.grouped {
		t.Fatal("setsid is on PATH, so the job should have its own process group")
	}
	if setsidErr != nil && job.grouped {
		t.Fatal("grouped=true without setsid — kill would signal a process group that isn't ours")
	}

	time.Sleep(1200 * time.Millisecond)
	if _, err := job.output(ctx, nil); err != nil {
		t.Fatalf("poll before kill: %v", err)
	}

	msg, err := job.kill(ctx)
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if !strings.Contains(msg, "Sent SIGKILL") {
		t.Fatalf("kill message = %q", msg)
	}

	// Give the kill a moment, then confirm the log stopped growing.
	time.Sleep(500 * time.Millisecond)
	afterKill, err := job.output(ctx, nil)
	if err != nil {
		t.Fatalf("poll after kill: %v", err)
	}
	if !strings.Contains(afterKill, "[status] killed") {
		t.Fatalf("poll after kill = %q", afterKill)
	}
	time.Sleep(700 * time.Millisecond)
	settled, err := job.output(ctx, nil)
	if err != nil {
		t.Fatalf("second poll after kill: %v", err)
	}
	if ticks := tickTimes(t, settled); len(ticks) != 0 {
		t.Fatalf("the job kept running after kill_shell: %v", ticks)
	}
}

// Without setsid the launcher falls back to a plain backgrounded shell. The
// job must still start and still be observable — only the kill scope narrows.
func TestSandboxBackgroundMechanismFallsBackWithoutSetsid(t *testing.T) {
	// Hide setsid from `command -v` (a non-executable file earlier in PATH is
	// enough — verified: dash's `command -v` checks executability) while
	// leaving the rest of the PATH intact, since mkdir/tail/head are real
	// binaries. This is what a minimal sandbox image looks like.
	shadow := t.TempDir()
	if err := os.WriteFile(shadow+"/setsid", nil, 0o644); err != nil {
		t.Fatalf("shadow dir: %v", err)
	}
	t.Setenv("PATH", shadow+":"+os.Getenv("PATH"))

	ctx := context.Background()
	jobs := newSandboxJobs()

	job, err := jobs.start(ctx, localShellRunner{}, `echo started; exit 0`)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	cleanupJobFiles(t, job)
	if job.grouped {
		t.Fatalf("grouped=%v with no setsid on PATH — the group kill would hit a group that isn't ours", job.grouped)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := job.output(ctx, nil)
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		if strings.Contains(out, "[status] exited (code=0)") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never finished without setsid: %q", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
