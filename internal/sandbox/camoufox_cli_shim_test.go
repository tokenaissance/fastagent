package sandbox

// The sandbox image puts a shim in front of the pip-installed camoufox-cli
// (deploy/docker/sandbox/camoufox-cli-shim.sh). Everything the agent types
// goes through it, and its contract has been invisible to this repo until
// now: shell, embedded in a Dockerfile, exercised by nothing.
//
// Two rules it has to keep, both pinned here against a fake client:
//
//  1. --proxy is injected from the environment, once, and never duplicated
//     when the caller passed it explicitly.
//  2. A tool call that never reached a daemon is retried once — and only such
//     a call, decided by the client's own words. The client says it two ways:
//     "Daemon did not start within 5 seconds" (it spawned a daemon and gave up
//     on its socket; that daemon keeps booting) and "Failed to connect to
//     daemon after 5 attempts: [Errno 2|111]" (the daemon that was there is
//     gone; the retry is what spawns a replacement). Both mean the command was
//     never delivered, so re-running the same argv cannot replay a click /
//     fill / submit — and because it IS the same argv, a daemon that is still
//     booting ends up configured by the caller (proxy / persistent / locale),
//     not by a re-derived guess.
//
// The [Errno 2] case is the one a user hit (2026-10-01): a daemon the
// sandbox's START COMMAND had left behind was killed about ten seconds into
// the sandbox, socket and browser with it, while a call was attached to it.
// The shim before this one retried on "the call failed and no socket exists",
// which is exactly the state a killed daemon leaves behind — so it waited out
// its whole budget for a daemon nothing was going to start, and then, with no
// socket, never retried at all.
//
// Falsification: drop the retry block and tests 2, 3 and 5 go red (one call
// where two are required); accept a live daemon's failure (test 4) or an
// unreadable failure (test 6) as retryable and those go red; retry in a loop
// instead of once and test 5 goes red with three calls.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const shimScriptPath = "../../deploy/docker/sandbox/camoufox-cli-shim.sh"

// fakeClient is camoufox-cli-bin for the tests below: it records its argv,
// emulates the client's daemon behaviour per $FAKE_MODE, and never touches a
// browser. The failure modes print the real client's error strings (they come
// from camoufox_cli/cli.py: spawn_daemon and the send loop) because those
// strings are what the shim decides on.
const fakeClient = `#!/bin/sh
n=0
if [ -f "$FAKE_COUNT" ]; then n=$(cat "$FAKE_COUNT"); fi
n=$((n + 1))
echo "$n" > "$FAKE_COUNT"
line="call $n:"
for a in "$@"; do line="$line $a"; done
echo "$line" >> "$FAKE_LOG"
echo "fake-stdout-$n"
echo "fake-stderr-$n" >&2
case "$FAKE_MODE" in
    ok)
        exit 0 ;;
    race-once)
        # The client's own 5 s spawn wait expires with no socket; the daemon it
        # spawned binds the socket a moment later.
        if [ "$n" -le 1 ]; then
            echo "Error: Daemon did not start within 5 seconds" >&2
            ( sleep 1; : > "$FAKE_SOCK" ) &
            exit 1
        fi
        exit 0 ;;
    killed-under-the-call)
        # The daemon this call attached to was killed; its socket went with it,
        # so connect() failed and the command was never delivered. The retry is
        # what spawns the replacement.
        if [ "$n" -le 1 ]; then
            echo "Error: Failed to connect to daemon after 5 attempts: [Errno 2] No such file or directory" >&2
            exit 1
        fi
        : > "$FAKE_SOCK"
        exit 0 ;;
    daemon-up-but-command-failed)
        : > "$FAKE_SOCK"
        exit 3 ;;
    no-daemon-no-socket)
        echo "Error: Failed to connect to daemon after 5 attempts: [Errno 2] No such file or directory" >&2
        exit 1 ;;
    unreadable-failure)
        # A failure the shim must not read as "this never reached a daemon".
        exit 1 ;;
esac
exit 0
`

type shimRun struct {
	exitCode int
	stdout   string
	stderr   string
	calls    []string
	sockPath string
}

// runShim drives the shim with a fake client. session is the session the
// caller's argv selects ("default" when it names none) and it also decides the
// socket path, so the two must agree — a mismatch would make the race tests
// pass by proving nothing, hence the guard.
func runShim(t *testing.T, mode, session string, extraEnv []string, args ...string) shimRun {
	t.Helper()

	shim, err := filepath.Abs(shimScriptPath)
	if err != nil {
		t.Fatalf("resolve shim path: %v", err)
	}
	if _, err := os.Stat(shim); err != nil {
		t.Fatalf("shim script: %v", err)
	}
	if session != "default" && !strings.Contains(strings.Join(args, " "), session) {
		t.Fatalf("test bug: argv %q does not name session %q, so the fake's socket would point nowhere", strings.Join(args, " "), session)
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "camoufox-cli-bin")
	if err := os.WriteFile(bin, []byte(fakeClient), 0o755); err != nil {
		t.Fatalf("write fake client: %v", err)
	}
	logPath := filepath.Join(dir, "calls.log")
	sock := fmt.Sprintf("/tmp/camoufox-cli-%s.sock", session)
	_ = os.Remove(sock)
	t.Cleanup(func() { _ = os.Remove(sock) })

	cmd := exec.Command("sh", append([]string{shim}, args...)...)
	cmd.Env = append([]string{
		"PATH=" + dir + ":/usr/bin:/bin",
		"CAMOUFOX_CLI_REAL_BIN=" + bin,
		"FAKE_LOG=" + logPath,
		"FAKE_COUNT=" + filepath.Join(dir, "count"),
		"FAKE_MODE=" + mode,
		"FAKE_SOCK=" + sock,
		"CAMOUFOX_SHIM_WAIT_SECS=10",
	}, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run shim: %v", err)
		}
		code = ee.ExitCode()
	}

	raw, _ := os.ReadFile(logPath)
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			calls = append(calls, line)
		}
	}
	return shimRun{exitCode: code, stdout: stdout.String(), stderr: stderr.String(), calls: calls, sockPath: sock}
}

// The proxy shim exists because Playwright/Camoufox ignore HTTPS_PROXY: only
// the flag works. It must inject the environment's proxy exactly once.
func TestCamoufoxShimInjectsProxyOnce(t *testing.T) {
	session := "--session=shimtest-proxy"

	got := runShim(t, "ok", "shimtest-proxy", []string{"HTTPS_PROXY=http://proxy.test:3128"}, "open", "about:blank", session)
	want := "call 1: --proxy http://proxy.test:3128 open about:blank --session=shimtest-proxy"
	if len(got.calls) != 1 || got.calls[0] != want {
		t.Fatalf("caller's args with an injected proxy:\n got %q\nwant %q", got.calls, want)
	}

	// A caller that passed --proxy itself must not get a second one.
	got = runShim(t, "ok", "default", []string{"HTTPS_PROXY=http://proxy.test:3128"},
		"open", "about:blank", "--proxy", "http://explicit.test:9")
	if len(got.calls) != 1 || strings.Count(got.calls[0], "--proxy") != 1 {
		t.Fatalf("explicit --proxy was duplicated: %q", got.calls)
	}
	if !strings.Contains(got.calls[0], "http://explicit.test:9") {
		t.Fatalf("explicit --proxy was replaced: %q", got.calls)
	}

	// Without the environment variable, nothing is injected.
	got = runShim(t, "ok", "default", nil, "open", "about:blank")
	if len(got.calls) != 1 || got.calls[0] != "call 1: open about:blank" {
		t.Fatalf("no proxy in the environment, but the argv changed: %q", got.calls)
	}
}

// The cold-start race: the client gave up on its own daemon, the daemon came
// up anyway, and the command was never sent. Same argv, one more attempt —
// after waiting for the socket it has budget for, so the retry attaches to
// that daemon instead of racing a second one against it.
func TestCamoufoxShimRetriesOnceWhenTheDaemonMissedItsOwnFiveSecondWait(t *testing.T) {
	session := "shimtest-race"

	// The space form of --session, because that is what the agent's skill text
	// uses; the socket path has to be read from it either way.
	start := time.Now()
	got := runShim(t, "race-once", session, nil, "open", "about:blank", "--session", session)
	elapsed := time.Since(start)

	if len(got.calls) != 2 {
		t.Fatalf("calls = %q; want the command retried exactly once", got.calls)
	}
	if !strings.HasSuffix(got.calls[1], "open about:blank --session shimtest-race") {
		t.Fatalf("the retry ran a different command: %q", got.calls[1])
	}
	if got.exitCode != 0 {
		t.Fatalf("exit code = %d; want 0 once the retry attaches to the daemon (stderr=%q)", got.exitCode, got.stderr)
	}
	// The caller's output is forwarded both times, in order, and the shim
	// says why it tried again.
	for _, want := range []string{"fake-stdout-1", "fake-stdout-2"} {
		if !strings.Contains(got.stdout, want) {
			t.Fatalf("stdout is missing %s: %q", want, got.stdout)
		}
	}
	if !strings.Contains(got.stderr, "retrying once") {
		t.Fatalf("stderr does not explain the retry: %q", got.stderr)
	}
	// The fake's socket appears 1 s after the first attempt; a shim that
	// retried immediately would have raced the daemon it was told about.
	if elapsed < time.Second {
		t.Fatalf("the shim did not wait for the socket it had budget for: %s", elapsed)
	}
}

// The failure a user actually hit (2026-10-01, dev and prod): the daemon the
// call was attached to was killed under it, socket and all. The retry spawns
// the replacement — and because the client says "never reached a daemon" in
// words, the shim does not first wait out its budget for a socket that no one
// is going to bind.
func TestCamoufoxShimRetriesOnceWhenTheDaemonWasKilledUnderTheCall(t *testing.T) {
	session := "shimtest-killed"

	start := time.Now()
	got := runShim(t, "killed-under-the-call", session, nil, "open", "https://example.com", "--session="+session)
	elapsed := time.Since(start)

	if len(got.calls) != 2 {
		t.Fatalf("calls = %q; want the command retried exactly once", got.calls)
	}
	if got.exitCode != 0 {
		t.Fatalf("exit code = %d; want 0 once the retry spawns a daemon (stderr=%q)", got.exitCode, got.stderr)
	}
	for _, want := range []string{"fake-stdout-1", "fake-stdout-2"} {
		if !strings.Contains(got.stdout, want) {
			t.Fatalf("stdout is missing %s: %q", want, got.stdout)
		}
	}
	if !strings.Contains(got.stderr, "retrying once") {
		t.Fatalf("stderr does not explain the retry: %q", got.stderr)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the shim waited for a socket the dead daemon was never going to bind: %s", elapsed)
	}
}

// The guard: only a call that never reached a daemon may be re-run. Against a
// live daemon a failure is the command's own verdict — retrying would replay
// whatever it already did (a click, a fill, a submit).
func TestCamoufoxShimDoesNotRetryWhenTheDaemonIsUp(t *testing.T) {
	start := time.Now()
	got := runShim(t, "daemon-up-but-command-failed", "shimtest-live", nil, "click", "@e3", "--session=shimtest-live")

	if len(got.calls) != 1 {
		t.Fatalf("calls = %q; a live daemon's failure must not be retried", got.calls)
	}
	if got.exitCode != 3 {
		t.Fatalf("exit code = %d; want the client's own 3", got.exitCode)
	}
	if strings.Contains(got.stderr, "retrying once") {
		t.Fatalf("shim claimed a retry it did not make: %q", got.stderr)
	}
	// A live daemon also means no wait: the socket is already there.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the shim waited for a socket that already existed: %s", elapsed)
	}
}

// No daemon ever appears: exactly one retry (the retry is the thing that would
// spawn one), bounded, and then the client's own failure goes back to the
// caller. Retrying in a loop here would be a busy-wait.
func TestCamoufoxShimGivesUpWhenNoDaemonEverAppears(t *testing.T) {
	start := time.Now()
	got := runShim(t, "no-daemon-no-socket", "shimtest-none", []string{"CAMOUFOX_SHIM_WAIT_SECS=1"},
		"open", "about:blank", "--session=shimtest-none")

	if len(got.calls) != 2 {
		t.Fatalf("calls = %q; want the client's call plus exactly one retry", got.calls)
	}
	if got.exitCode != 1 {
		t.Fatalf("exit code = %d; want the client's own 1", got.exitCode)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("the wait was not bounded by CAMOUFOX_SHIM_WAIT_SECS: %s", elapsed)
	}
}

// A failure the shim cannot read as "this never reached a daemon" is forwarded
// as it stands: the shim is insurance for one specific race, not a blanket
// retry of everything that fails.
func TestCamoufoxShimDoesNotRetryAFailureItCannotRead(t *testing.T) {
	got := runShim(t, "unreadable-failure", "shimtest-unreadable", nil,
		"click", "@e3", "--session=shimtest-unreadable")

	if len(got.calls) != 1 {
		t.Fatalf("calls = %q; an unreadable failure must not be replayed", got.calls)
	}
	if got.exitCode != 1 {
		t.Fatalf("exit code = %d; want the client's own 1", got.exitCode)
	}
	if strings.Contains(got.stderr, "retrying once") {
		t.Fatalf("shim claimed a retry it did not make: %q", got.stderr)
	}
}
