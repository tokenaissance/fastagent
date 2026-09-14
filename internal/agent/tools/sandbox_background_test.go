package tools

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSandboxRunner stands in for a sandbox executor: it records every
// command a job sent and answers with scripted output.
type fakeSandboxRunner struct {
	mu      sync.Mutex
	sent    []string
	replies []string
	err     error
}

func (f *fakeSandboxRunner) Exec(_ context.Context, command string, _ time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, command)
	if f.err != nil {
		return "", f.err
	}
	if len(f.replies) == 0 {
		return "", nil
	}
	reply := f.replies[0]
	f.replies = f.replies[1:]
	return reply, nil
}

func (f *fakeSandboxRunner) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

// probeReply builds the exact bytes backgroundProbeCommand prints.
func probeReply(status string, size int, code, body string) string {
	return "\x1ffcbg " + status + " " + strconv.Itoa(size) + " " + code + "\x1f" + body
}

// tickTimes returns the tick numbers present in a poll result, so tests can
// assert on progress rather than on exact byte counts.
func tickTimes(t *testing.T, out string) []int {
	t.Helper()
	var got []int
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "tick ") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "tick ")))
		if err != nil {
			t.Fatalf("unparsable tick line %q", line)
		}
		got = append(got, n)
	}
	return got
}

// The launcher must leave no file descriptor of the exec stream open to the
// job — that is the whole difference between this and the `nohup cmd &` that
// hung in production (the Connect stream stayed open, so exec never returned).
func TestBackgroundLaunchCommandDetachesTheJob(t *testing.T) {
	got := backgroundLaunchCommand("sbg_1", "python train.py --epochs 5")

	for _, want := range []string{
		"mkdir -p '/tmp/fastagent-bg' || exit 1",
		": > '/tmp/fastagent-bg/sbg_1.log'",
		"rm -f '/tmp/fastagent-bg/sbg_1.exit'",
		"command -v setsid",
		"$__RB sh -c '(",
		"  ( ( python train.py --epochs 5",
		`printf 'fcbg %s %s\n' "$!" "$__G"`,
		"python train.py --epochs 5",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("launch command is missing %q\n--- got ---\n%s", want, got)
		}
	}

	// The exit-code capture has to run inside the detached shell, not in the
	// launcher (where $? would be the status of the `&` itself).
	redirect := "> '/tmp/fastagent-bg/sbg_1.log' 2>&1 < /dev/null &"
	if strings.Count(got, redirect) != 2 {
		t.Fatalf("both detach forms must redirect the job's streams (got %d):\n%s", strings.Count(got, redirect), got)
	}
	// The exit-code capture has to be part of the detached command, not of the
	// launcher (where $? would be the status of the `&` itself).
	for _, form := range strings.Split(got, redirect)[:2] {
		if !strings.Contains(form, "__rc=$?") || !strings.Contains(form, "sbg_1.exit") {
			t.Errorf("exit-code capture missing from a detach form:\n%s", form)
		}
	}
}

// A command containing quotes, newlines or substitutions must reach the job
// verbatim — the composition never gets to interpret it.
func TestBackgroundLaunchCommandQuotesHostileCommand(t *testing.T) {
	hostile := "echo 'it'\\''s here'\nrm -f $HOME/x; printf '%s' \"$(date)\""
	got := backgroundLaunchCommand("sbg_7", hostile)

	if !strings.Contains(got, "sh -c '(") {
		t.Errorf("the detached script must be single-quoted so the outer shell cannot expand it:\n%s", got)
	}
	if !strings.Contains(got, "'\\''") {
		t.Errorf("embedded single quotes were not escaped:\n%s", got)
	}
	if !strings.Contains(got, "$(date)") {
		t.Errorf("the command body must reach the job literally:\n%s", got)
	}
}

func TestBackgroundProbeCommandReadsFromTheCursor(t *testing.T) {
	job := &sandboxJob{id: "sbg_2", pid: "77", grouped: true,
		logPath: sandboxJobLogPath("sbg_2"), exitPath: sandboxJobExitPath("sbg_2")}

	first := backgroundProbeCommand(job, 0)
	if !strings.Contains(first, "tail -c +1 '/tmp/fastagent-bg/sbg_2.log'") {
		t.Errorf("cursor 0 must read from byte 1 (tail counts from 1):\n%s", first)
	}
	next := backgroundProbeCommand(job, 41)
	if !strings.Contains(next, "tail -c +42 '/tmp/fastagent-bg/sbg_2.log'") {
		t.Errorf("cursor 41 must resume at byte 42:\n%s", next)
	}
	if !strings.Contains(next, "head -c "+strconv.Itoa(sandboxJobOutputCap)) {
		t.Errorf("one poll must be capped:\n%s", next)
	}
	if !strings.Contains(next, "'/tmp/fastagent-bg/sbg_2.exit'") {
		t.Errorf("probe must read the exit file:\n%s", next)
	}
}

func TestParseBackgroundLaunch(t *testing.T) {
	pid, grouped, err := parseBackgroundLaunch("fcbg 122 1\n")
	if err != nil || pid != "122" || !grouped {
		t.Fatalf("parseBackgroundLaunch(fcbg 122 1) = %q, %v, %v; want 122, true, nil", pid, grouped, err)
	}
	// Noise before the handle (a shell that warns on startup) is tolerated.
	pid, grouped, err = parseBackgroundLaunch("sh: cannot set terminal process group\nfcbg 9 0\n")
	if err != nil || pid != "9" || grouped {
		t.Fatalf("parseBackgroundLaunch(noisy) = %q, %v, %v; want 9, false, nil", pid, grouped, err)
	}
	if _, _, err := parseBackgroundLaunch("boom\n"); err == nil {
		t.Fatal("parseBackgroundLaunch(boom) must fail: a job the model cannot poll is worse than a clear error")
	}
	if _, _, err := parseBackgroundLaunch("fcbg not-a-pid 1\n"); err == nil {
		t.Fatal("a non-numeric pid must be rejected — it is interpolated into the kill command")
	}
}

func TestParseBackgroundProbe(t *testing.T) {
	status, code, size, body, err := parseBackgroundProbe(probeReply("running", 12, "-", "hello\nworld"))
	if err != nil || status != "running" || code != "-" || size != 12 || body != "hello\nworld" {
		t.Fatalf("running reply = %q %q %d %q %v", status, code, size, body, err)
	}
	// BSD wc pads counts with spaces; Fields must absorb that.
	status, code, size, body, err = parseBackgroundProbe("\x1ffcbg    exited    4096  137  \x1ftail")
	if err != nil || status != "exited" || code != "137" || size != 4096 || body != "tail" {
		t.Fatalf("padded reply = %q %q %d %q %v", status, code, size, body, err)
	}
	if _, _, _, _, err := parseBackgroundProbe("no marker here"); err == nil {
		t.Fatal("a reply without the status marker must be an error, not empty output")
	}
	if _, _, _, _, err := parseBackgroundProbe("\x1ffcbg running not-a-size -\x1f"); err == nil {
		t.Fatal("an unparsable size must be an error")
	}
}

func newTestJob(runner sandboxRunner) *sandboxJob {
	return &sandboxJob{
		id:       "sbg_1",
		pid:      "122",
		grouped:  true,
		logPath:  sandboxJobLogPath("sbg_1"),
		exitPath: sandboxJobExitPath("sbg_1"),
		command:  "bash job.sh",
		runner:   runner,
	}
}

func TestSandboxJobOutputReturnsDeltaThenStatus(t *testing.T) {
	ctx := context.Background()
	runner := &fakeSandboxRunner{replies: []string{
		probeReply("running", 5, "-", "tick1"),
		probeReply("exited", 11, "0", "\ntick2\ndone"),
	}}
	job := newTestJob(runner)

	first, err := job.output(ctx, nil)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if first != "tick1\n[status] running\n" {
		t.Fatalf("first poll = %q", first)
	}

	second, err := job.output(ctx, nil)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if second != "\ntick2\ndone\n[status] exited (code=0)\n" {
		t.Fatalf("second poll = %q", second)
	}

	sent := runner.commands()
	if len(sent) != 2 {
		t.Fatalf("expected 2 polls, got %d", len(sent))
	}
	if !strings.Contains(sent[1], "tail -c +6 ") {
		t.Errorf("second poll must resume after the 5 bytes already read:\n%s", sent[1])
	}
}

func TestSandboxJobOutputReportsUnreadTail(t *testing.T) {
	runner := &fakeSandboxRunner{replies: []string{
		probeReply("running", 4096, "-", "first-64k"),
	}}
	job := newTestJob(runner)

	out, err := job.output(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !strings.Contains(out, "first-64k") {
		t.Fatalf("poll dropped the body: %q", out)
	}
	if !strings.Contains(out, "[more output pending: "+strconv.Itoa(4096-len("first-64k"))+" bytes unread") {
		t.Fatalf("poll must tell the model there is more to read: %q", out)
	}
	// The cursor advances only past what was actually returned, so the next
	// call picks up the tail instead of skipping it.
	if job.cursor != len("first-64k") {
		t.Fatalf("cursor = %d, want %d", job.cursor, len("first-64k"))
	}
}

func TestSandboxJobOutputHandlesReplacedSandbox(t *testing.T) {
	ctx := context.Background()

	// A log that shrank means the sandbox was replaced under us: re-read from
	// offset 0 instead of reporting silence forever.
	reset := &fakeSandboxRunner{replies: []string{
		probeReply("running", 3, "-", ""),
		probeReply("running", 9, "-", "fresh\n"),
	}}
	job := newTestJob(reset)
	job.cursor = 10
	out, err := job.output(ctx, nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !strings.Contains(out, "fresh") {
		t.Fatalf("a replaced log must be re-read from the start, got %q", out)
	}
	sent := reset.commands()
	if len(sent) != 2 || !strings.Contains(sent[0], "tail -c +11 ") || !strings.Contains(sent[1], "tail -c +1 ") {
		t.Fatalf("expected a cursor reset between the two probes, got %d commands", len(sent))
	}

	// A missing log is the honest "the sandbox is gone" signal.
	missing := newTestJob(&fakeSandboxRunner{replies: []string{probeReply("missing", 0, "-", "")}})
	out, err = missing.output(ctx, nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !strings.Contains(out, "[status] lost") {
		t.Fatalf("a missing log must be reported as lost, got %q", out)
	}
}

func TestSandboxJobOutputReportsKill(t *testing.T) {
	// First reply answers the kill command, second answers the poll.
	runner := &fakeSandboxRunner{replies: []string{"", probeReply("running", 0, "-", "")}}
	job := newTestJob(runner)
	if _, err := job.kill(context.Background()); err != nil {
		t.Fatalf("kill: %v", err)
	}
	out, err := job.output(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !strings.Contains(out, "[status] killed") {
		t.Fatalf("a killed job must say so rather than keep claiming it runs: %q", out)
	}
}

func TestSandboxJobKillTargetsTheProcessGroup(t *testing.T) {
	ctx := context.Background()

	grouped := newTestJob(&fakeSandboxRunner{})
	msg, err := grouped.kill(ctx)
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if !strings.Contains(msg, "Sent SIGKILL") || !strings.Contains(msg, "process group") {
		t.Fatalf("kill message = %q", msg)
	}
	if got := grouped.runner.(*fakeSandboxRunner).commands(); len(got) != 1 || !strings.Contains(got[0], "kill -KILL -122 ") {
		t.Fatalf("a grouped job must be killed by process group, got %v", got)
	}

	// No setsid in the image: only the pid itself is ours to kill.
	plain := newTestJob(&fakeSandboxRunner{})
	plain.grouped = false
	if _, err := plain.kill(ctx); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if got := plain.runner.(*fakeSandboxRunner).commands(); len(got) != 1 || !strings.Contains(got[0], "kill -KILL 122 ") {
		t.Fatalf("an ungrouped job must be killed by pid, got %v", got)
	}

	// Idempotent: a second kill must not re-signal a pid the kernel may have
	// handed to somebody else in the meantime.
	if _, err := grouped.kill(ctx); err != nil {
		t.Fatalf("second kill: %v", err)
	}
	if got := grouped.runner.(*fakeSandboxRunner).commands(); len(got) != 1 {
		t.Fatalf("second kill re-signalled the pid: %v", got)
	}
}

func TestSandboxJobsStartRegistersResolvableJob(t *testing.T) {
	ctx := context.Background()
	runner := &fakeSandboxRunner{replies: []string{"fcbg 4242 1\n"}}
	jobs := newSandboxJobs()

	job, err := jobs.start(ctx, runner, "bash job.sh")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if job.id != "sbg_1" || job.pid != "4242" || !job.grouped {
		t.Fatalf("job = %+v", job)
	}
	if got := runner.commands(); len(got) != 1 || got[0] != backgroundLaunchCommand("sbg_1", "bash job.sh") {
		t.Fatalf("start did not send the launcher:\n%v", got)
	}
	if jobs.get("sbg_1") != job {
		t.Fatal("a started job must be reachable by its id — bash_output has no other way to find it")
	}

	// A second job gets its own id and its own files.
	runner.replies = []string{"fcbg 4243 0\n"}
	second, err := jobs.start(ctx, runner, "bash other.sh")
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if second.id == job.id || second.logPath == job.logPath {
		t.Fatalf("jobs share identity: %q/%q vs %q/%q", job.id, job.logPath, second.id, second.logPath)
	}
}

func TestSandboxJobsStartFailsLoudly(t *testing.T) {
	ctx := context.Background()
	jobs := newSandboxJobs()

	if _, err := jobs.start(ctx, nil, "bash job.sh"); err == nil {
		t.Fatal("start without a runner must fail rather than silently no-op")
	}
	if _, err := jobs.start(ctx, &fakeSandboxRunner{}, ""); err == nil {
		t.Fatal("start with an empty command must fail")
	}
	if _, err := jobs.start(ctx, &fakeSandboxRunner{replies: []string{"boom"}}, "bash job.sh"); err == nil {
		t.Fatal("start must fail when the backend never reported a pid — the model could never poll the job")
	}

	jobs.close()
	if _, err := jobs.start(ctx, &fakeSandboxRunner{replies: []string{"fcbg 5 1\n"}}, "bash job.sh"); err == nil {
		t.Fatal("a closed table must refuse new jobs")
	}
}
