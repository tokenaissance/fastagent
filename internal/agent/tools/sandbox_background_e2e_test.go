package tools

// End-to-end proof that the tool-level background flow works against a real
// sandbox, on both backends that matter:
//
//   - docker (the self-hosted backend): a real container is created, the three
//     tool calls run through the real shell, and the job's process group is
//     really killed.
//   - e2b (the hosted backend, and the one that broke in production): a real
//     sandbox is created, and the job must still be alive on a LATER tool call
//     — that survival is the whole claim of the feature, and the only way to
//     check it is against the real API.
//
// Credential policy follows the rest of the repo: skip locally for ergonomics,
// fail in CI so a green run can never be mistaken for a verified one.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// dockerExecAdapter exposes the legacy DockerSandbox through the session-shaped
// sandbox.Executor interface the tools expect. Only Exec is used here.
type dockerExecAdapter struct{ sb *sandbox.DockerSandbox }

func (a *dockerExecAdapter) Exec(ctx context.Context, command string, timeout time.Duration) (string, error) {
	return a.sb.Exec(ctx, command, "/workspace")
}
func (a *dockerExecAdapter) ReadFile(context.Context, string) (string, error) { return "", nil }
func (a *dockerExecAdapter) WriteFile(context.Context, string, string) (string, error) {
	return "", nil
}
func (a *dockerExecAdapter) ListDir(context.Context, string) (string, error) { return "", nil }
func (a *dockerExecAdapter) Backend() string                                 { return "docker" }
func (a *dockerExecAdapter) Close() error                                    { return a.sb.Close() }

// jobIDFromStart pulls the bash_id out of the start confirmation — the test
// then uses it exactly the way the model does.
func jobIDFromStart(t *testing.T, out string) string {
	t.Helper()
	const marker = `bash_output(bash_id="`
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("start result carries no bash_id: %q", out)
	}
	rest := out[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("start result has an unterminated bash_id: %q", out)
	}
	return rest[:j]
}

// runBackgroundJob starts a detached job through the tool surface.
func runBackgroundJob(t *testing.T, ctx context.Context, r *Registry, command string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"command": command, "run_in_background": true})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	start, err := r.Execute(ctx, "exec", string(raw))
	if err != nil {
		t.Fatalf("exec(run_in_background): %v", err)
	}
	return start
}

// pollUntil polls a job id until the predicate accepts a poll result.
func pollUntil(t *testing.T, ctx context.Context, r *Registry, id string, timeout time.Duration, ok func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for {
		out, err := r.Execute(ctx, "bash_output", `{"bash_id":"`+id+`"}`)
		if err != nil {
			t.Fatalf("bash_output(%s): %v", id, err)
		}
		last = out
		if ok(out) {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("bash_output(%s) never satisfied the condition within %s; last = %q", id, timeout, last)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestSandboxBackgroundDockerE2E(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("this e2e needs docker in CI — it is the only check that a sandbox job really outlives the exec call")
		}
		t.Skip("docker not available locally")
	}
	image := os.Getenv("FASTAGENT_TEST_DOCKER_IMAGE")
	if image == "" {
		image = "alpine:3.21"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	sb := sandbox.NewDockerSandbox(image, t.TempDir(), nil)
	if err := sb.Create(); err != nil {
		t.Fatalf("create docker sandbox (%s): %v", image, err)
	}
	defer func() { _ = sb.Close() }()

	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	ex := &dockerExecAdapter{sb: sb}
	r.SetExecutor(ex)

	// The job runs far longer than any single tool call may.
	start := runBackgroundJob(t, ctx, r, `i=0; while [ $i -lt 600 ]; do i=$((i+1)); echo tick $i; sleep 1; done`)
	id := jobIDFromStart(t, start)

	// Every poll is an independent exec into the container. Collecting the
	// ticks they return proves two things at once: the job kept running after
	// the call that started it returned, and each poll returned only what had
	// arrived since the previous one (strictly increasing, no repeats).
	var seen []int
	last := pollUntil(t, ctx, r, id, 30*time.Second, func(o string) bool {
		seen = append(seen, tickTimes(t, o)...)
		return len(seen) >= 3
	})
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Fatalf("polls repeated or rewound output: %v (last poll %q)", seen, last)
		}
	}
	if !strings.Contains(last, "[status] running") {
		t.Fatalf("job should still be running while it ticks: %q", last)
	}

	killed, err := r.Execute(ctx, "kill_shell", `{"bash_id":"`+id+`"}`)
	if err != nil {
		t.Fatalf("kill_shell: %v", err)
	}
	if !strings.Contains(killed, "Sent SIGKILL") {
		t.Fatalf("kill_shell = %q", killed)
	}
	final := pollUntil(t, ctx, r, id, 10*time.Second, func(o string) bool { return strings.Contains(o, "[status] killed") })
	_ = final

	// The whole tree is gone: no tick may arrive after the kill settles.
	time.Sleep(1500 * time.Millisecond)
	settled, err := r.Execute(ctx, "bash_output", `{"bash_id":"`+id+`"}`)
	if err != nil {
		t.Fatalf("bash_output after kill: %v", err)
	}
	if ticks := tickTimes(t, settled); len(ticks) != 0 {
		t.Fatalf("the job survived kill_shell: %v", ticks)
	}

	// A failing job still reports its exit code — that is what makes the
	// primitive usable for batch runs.
	shortStart := runBackgroundJob(t, ctx, r, `echo bye; exit 3`)
	shortID := jobIDFromStart(t, shortStart)
	exited := pollUntil(t, ctx, r, shortID, 20*time.Second, func(o string) bool { return strings.Contains(o, "[status] exited") })
	if !strings.Contains(exited, "[status] exited (code=3)") || !strings.Contains(exited, "bye") {
		t.Fatalf("exit code or output lost: %q", exited)
	}
}

func TestSandboxBackgroundE2BLive(t *testing.T) {
	apiKey := os.Getenv("E2B_API_KEY")
	template := os.Getenv("E2B_TEMPLATE")
	if apiKey == "" || template == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TestSandboxBackgroundE2BLive requires E2B_API_KEY and E2B_TEMPLATE in CI")
		}
		t.Skip("set E2B_API_KEY and E2B_TEMPLATE to run the live e2b background test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pool := sandbox.NewE2BExecutorPool(apiKey, template, "", 5*time.Minute)
	defer pool.CloseAll()
	ex, err := pool.Get(ctx, "agt-bg-e2e", "", "sess-bg-e2e")
	if err != nil {
		t.Fatalf("pool.Get: %v", err)
	}

	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	r.SetExecutor(ex)

	// Record which kill scope this image supports; the job itself must work
	// either way.
	if out, err := ex.Exec(ctx, "command -v setsid || echo no-setsid", 30*time.Second); err == nil {
		t.Logf("e2b image setsid: %s", strings.TrimSpace(out))
	}

	start := runBackgroundJob(t, ctx, r, `i=0; while [ $i -lt 600 ]; do i=$((i+1)); echo tick $i; sleep 1; done`)
	id := jobIDFromStart(t, start)

	// The claim under test: the job is still running on a LATER call, i.e. it
	// outlived the exec request that started it.
	var seen []int
	last := pollUntil(t, ctx, r, id, 60*time.Second, func(o string) bool {
		seen = append(seen, tickTimes(t, o)...)
		return len(seen) >= 3
	})
	if !strings.Contains(last, "[status] running") {
		t.Fatalf("job should still be running while it ticks: %q", last)
	}

	if _, err := r.Execute(ctx, "kill_shell", `{"bash_id":"`+id+`"}`); err != nil {
		t.Fatalf("kill_shell: %v", err)
	}
	pollUntil(t, ctx, r, id, 30*time.Second, func(o string) bool { return strings.Contains(o, "[status] killed") })
}
