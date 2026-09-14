package tools

// Tool-surface contract for background jobs in sandbox mode: the model calls
// exec(run_in_background=true) and gets a bash_id back, then reads it with
// bash_output and stops it with kill_shell — the same three calls it uses on
// the host. Before this, the sandbox path answered all three with "use tmux
// instead", so a long job could only be driven by hand-rolled nohup + sleep
// (which is what hung production on the Connect stream).

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type bgRecordingExecutor struct {
	mu      sync.Mutex
	sent    []string
	replies []string
}

func (e *bgRecordingExecutor) Exec(_ context.Context, command string, _ time.Duration) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sent = append(e.sent, command)
	if len(e.replies) == 0 {
		return "", nil
	}
	reply := e.replies[0]
	e.replies = e.replies[1:]
	return reply, nil
}

func (e *bgRecordingExecutor) ReadFile(context.Context, string) (string, error) { return "", nil }
func (e *bgRecordingExecutor) WriteFile(context.Context, string, string) (string, error) {
	return "", nil
}
func (e *bgRecordingExecutor) ListDir(context.Context, string) (string, error) { return "", nil }
func (e *bgRecordingExecutor) Backend() string                                 { return "test" }
func (e *bgRecordingExecutor) Close() error                                    { return nil }

func (e *bgRecordingExecutor) commands() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.sent...)
}

func TestExecRunInBackgroundInSandboxMode(t *testing.T) {
	ctx := context.Background()
	ex := &bgRecordingExecutor{replies: []string{"fcbg 4242 1\n"}}
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	r.SetExecutor(ex)

	started, err := r.Execute(ctx, "exec",
		`{"command":"bash /workspace/kronos_crypto_daily_cron.sh","run_in_background":true}`)
	if err != nil {
		t.Fatalf("exec(run_in_background) in sandbox mode: %v", err)
	}
	if !strings.HasPrefix(started, MetaSandboxPrefix) {
		t.Errorf("sandbox results must stay marked as such: %q", started)
	}
	if !strings.Contains(started, "sbg_1") || !strings.Contains(started, "pid 4242") {
		t.Errorf("start result must carry the job id and pid: %q", started)
	}
	if !strings.Contains(started, `bash_output(bash_id="sbg_1")`) ||
		!strings.Contains(started, `kill_shell(bash_id="sbg_1")`) {
		t.Errorf("start result must tell the model how to poll and stop the job: %q", started)
	}

	commands := ex.commands()
	if len(commands) != 1 {
		t.Fatalf("expected exactly one sandbox command (the launcher), got %d: %v", len(commands), commands)
	}
	if !strings.Contains(commands[0], "bash /workspace/kronos_crypto_daily_cron.sh") {
		t.Errorf("the launcher must carry the model's command: %q", commands[0])
	}
	if !strings.Contains(commands[0], "< /dev/null &") {
		t.Errorf("the launcher must detach the job's streams: %q", commands[0])
	}
	// The job belongs to the sandbox: nothing may be started on the host.
	if shells := r.shellMgr.list(); len(shells) != 0 {
		t.Errorf("sandbox background mode created %d host shell(s)", len(shells))
	}

	// bash_output finds the job through the sandbox table, not shellMgr.
	ex.replies = append(ex.replies, probeReply("running", 5, "-", "tick1"))
	polled, err := r.Execute(ctx, "bash_output", `{"bash_id":"sbg_1"}`)
	if err != nil {
		t.Fatalf("bash_output(sbg_1): %v", err)
	}
	if !strings.Contains(polled, "tick1") || !strings.Contains(polled, "[status] running") {
		t.Errorf("bash_output = %q", polled)
	}

	// kill_shell signals the job inside the sandbox.
	killed, err := r.Execute(ctx, "kill_shell", `{"bash_id":"sbg_1"}`)
	if err != nil {
		t.Fatalf("kill_shell(sbg_1): %v", err)
	}
	if !strings.Contains(killed, "Sent SIGKILL") {
		t.Errorf("kill_shell = %q", killed)
	}
	if last := ex.commands(); !strings.Contains(last[len(last)-1], "kill -KILL -4242") {
		t.Errorf("kill must reach the sandbox job, got %q", last[len(last)-1])
	}

	// The host path is untouched: an unknown id still reports the host error.
	if _, err := r.Execute(ctx, "bash_output", `{"bash_id":"bash_9"}`); err == nil {
		t.Error("an id that names neither a host shell nor a sandbox job must still fail")
	}
}

// A job started detached must still receive the skill env the foreground path
// injects — otherwise a backgrounded skill (image generation, long scrape)
// silently runs without its API keys.
func TestExecRunInBackgroundInSandboxModeInjectsSkillEnv(t *testing.T) {
	ctx := context.Background()
	ex := &bgRecordingExecutor{replies: []string{"fcbg 7 1\n"}}
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	r.SetExecutor(ex)
	r.envProvider = func(skill string) map[string]string {
		if skill == "image-tool" {
			return map[string]string{"FAL_KEY": "sk-test"}
		}
		return nil
	}
	r.skillDirs = []string{"/skills"}
	registerSandboxedExec(r, ex)

	if _, err := r.Execute(ctx, "exec",
		`{"command":"python /skills/image-tool/main.py --out ./x.webp","run_in_background":true}`); err != nil {
		t.Fatalf("exec: %v", err)
	}
	commands := ex.commands()
	if len(commands) != 1 || !strings.Contains(commands[0], "export FAL_KEY='sk-test'") {
		t.Fatalf("background job lost its skill env: %v", commands)
	}
	if !strings.Contains(commands[0], "python /skills/image-tool/main.py --out ./x.webp") {
		t.Fatalf("command was mangled by env injection: %v", commands)
	}
}

func TestExecRunInBackgroundWithoutExecutorFailsClearly(t *testing.T) {
	// Sandbox forced for this call, but no executor is bound (setExecutor
	// never ran): the model must be told the truth instead of being pointed
	// at a workaround that no longer exists.
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()

	_, err := r.Execute(context.Background(), "exec",
		`{"command":"bash job.sh","run_in_background":true,"sandbox":true}`)
	if err == nil {
		t.Fatal("expected a refusal when sandbox is forced without an executor")
	}
	if got := err.Error(); !strings.Contains(got, "run_in_background is unavailable on this sandbox path") {
		t.Errorf("error should name the missing executor, got %q", got)
	}
}
