package tools

// Background execution inside a sandbox.
//
// A host-mode background run (exec.go with run_in_background=true) owns an
// os/exec process handle in shellMgr. A sandbox run cannot: the process lives
// in another kernel (docker container / e2b microVM), and all that survives
// the exec call is what the job leaves on disk. A sandbox job is therefore
// defined as two files inside the sandbox plus the pid that owns them:
//
//	/tmp/fastagent-bg/<id>.log    stdout+stderr, append-only
//	/tmp/fastagent-bg/<id>.exit   the exit code, written when the job ends
//
// and every operation is one more command through the same executor:
//
//	start  create the files, then detach the job (setsid when available)
//	poll   emit the bytes past the cursor, plus running / exited / missing
//	kill   SIGKILL the job's process group
//
// Why not the backend's own background flag (e2b's `commands.run(cmd,
// {background: true})`)? It exists on one backend out of three, and the
// stream it returns is delivered only to the caller that started it — e2b's
// own cross-process recipe is "background + redirect output to a file"
// (https://docs.e2b.dev/commands/background): the file is the part that makes
// the output readable from a later call. This composition also settles the
// bug that motivated the feature — `nohup cmd &` without `</dev/null` leaves
// the Connect stream open, so the exec call hangs until the server deadline
// and the model never gets its pid (production r39–r42).

import (
	"context"
	"fmt"
	mrand "math/rand/v2"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

const (
	// sandboxJobDir lives in /tmp inside the sandbox on purpose: /workspace is
	// flushed to the durable workspace store after every exec, so a growing log
	// there would be re-uploaded on every poll and would show up in the user's
	// Files panel. /tmp also survives an e2b pause (files + memory), though not
	// a sandbox replacement — that case is the "missing" status.
	sandboxJobDir = "/tmp/fastagent-bg"
	// sandboxJobOpTimeout bounds each control command (start / poll / kill).
	// They are all local shell work, so the tool's own `timeout` argument does
	// not apply to them: that argument describes how long the *job* may run,
	// not how long reading its log may take.
	sandboxJobOpTimeout = 30 * time.Second
	// sandboxJobOutputCap bounds one bash_output payload. A job that outran the
	// cap keeps its unread tail for the next call — the cursor only advances by
	// the bytes actually returned.
	sandboxJobOutputCap = 64 * 1024
	// sandboxRetainedFinished bounds how many *finished* jobs stay addressable by
	// bash_output. A running job is never forgotten, and the entry is small (a
	// path, a read cursor, a runner) — but without a bound the table grows by one
	// entry for every job this agent has ever started, which is the "sized by
	// history" shape the residency audit looks for (docs 10 §10, G27).
	sandboxRetainedFinished = 64
)

// sandboxRunner is the single capability a sandbox job needs: run one shell
// command and return its output. sandbox.Executor satisfies it as-is.
type sandboxRunner interface {
	Exec(ctx context.Context, command string, timeout time.Duration) (string, error)
}

// sandboxJob is one detached command inside one sandbox.
type sandboxJob struct {
	id       string
	pid      string
	grouped  bool // pid leads its own process group, so kill can target the group
	logPath  string
	exitPath string
	command  string
	runner   sandboxRunner
	// table is the job table this job belongs to, so a poll that observes the
	// job has finished can retire it (nil for jobs built by hand in tests).
	table *sandboxJobs

	mu     sync.Mutex
	cursor int  // bytes of the log already handed to the model
	killed bool // kill_shell ran; the job may still be writing its exit code
	// finished is set once a poll has seen the job exited (or its sandbox gone),
	// and finishedAt orders retirement. Both are guarded by mu.
	finished   bool
	finishedAt time.Time
}

// sandboxJobs is the per-Registry table of live sandbox jobs. It is the
// sandbox counterpart of shellManager: shellManager holds process handles,
// this holds how to reach the job again (runner + paths + read cursor).
//
// Every instance mints its own id namespace. A job's identity IS its two
// files inside the sandbox, so two tables that both started at "sbg_1" would
// share them: the second launcher's `rm -f …/sbg_1.exit` and `: > …/sbg_1.log`
// would erase the first job's record while its process kept writing into the
// now-truncated log. That is not hypothetical — it is what turned one test run
// into `malformed status marker "fcbg exited 211 "`, and in production the
// same collision is reachable whenever two registries (two agents in one
// process, or two replicas holding the same session lease) start a job.
type sandboxJobs struct {
	mu     sync.Mutex
	prefix string // "sbg_<4 hex>_", fixed for the life of this table
	seq    int
	live   map[string]*sandboxJob
	closed bool
}

func newSandboxJobs() *sandboxJobs {
	return &sandboxJobs{
		prefix: fmt.Sprintf("sbg_%04x_", mrand.Uint32()&0xffff),
		live:   map[string]*sandboxJob{},
	}
}

// get returns the job or nil. Safe on a nil table so callers that build a
// Registry by hand (tests) can't panic.
func (s *sandboxJobs) get(id string) *sandboxJob {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[id]
}

// start launches command as a detached job inside the runner's sandbox.
func (s *sandboxJobs) start(ctx context.Context, runner sandboxRunner, command string) (*sandboxJob, error) {
	if runner == nil {
		return nil, fmt.Errorf("no sandbox runner is bound to this session")
	}
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}
	if s == nil {
		return nil, fmt.Errorf("no sandbox job table on this registry")
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("sandbox job table closed")
	}
	s.seq++
	id := s.prefix + strconv.Itoa(s.seq)
	s.mu.Unlock()

	out, err := runner.Exec(ctx, backgroundLaunchCommand(id, command), sandboxJobOpTimeout)
	if err != nil {
		return nil, fmt.Errorf("launch: %w (output: %s)", err, strings.TrimSpace(out))
	}
	pid, grouped, err := parseBackgroundLaunch(out)
	if err != nil {
		return nil, err
	}

	job := &sandboxJob{
		id:       id,
		pid:      pid,
		grouped:  grouped,
		logPath:  sandboxJobLogPath(id),
		exitPath: sandboxJobExitPath(id),
		command:  command,
		runner:   runner,
		table:    s,
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("sandbox job table closed")
	}
	s.live[id] = job
	s.mu.Unlock()
	return job, nil
}

// close stops accepting new jobs. Existing jobs are deliberately left alone:
// they live inside sandboxes this process does not own (a replica may serve
// the next turn), and killing them on shutdown would break exactly the
// long-running work the feature exists for.
func (s *sandboxJobs) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// retireFinished records that a poll has seen this job finish, then forgets the
// oldest finished jobs beyond sandboxRetainedFinished. Running jobs are never
// touched: their entry is the only handle bash_output has on a live process.
//
// A forgotten id answers exactly like an id that never existed — the tool's
// message names that ("ids are valid only within the same agent process") — so
// the honest statement of the contract is "the most recent
// sandboxRetainedFinished finished jobs stay addressable", and that is what this
// constant means.
func (s *sandboxJobs) retireFinished(j *sandboxJob) {
	if s == nil || j == nil {
		return
	}
	j.mu.Lock()
	if !j.finished {
		j.finished = true
		j.finishedAt = time.Now()
	}
	j.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	type done struct {
		id string
		at time.Time
	}
	var finished []done
	for id, other := range s.live {
		other.mu.Lock()
		fin, at := other.finished, other.finishedAt
		other.mu.Unlock()
		if fin {
			finished = append(finished, done{id: id, at: at})
		}
	}
	if len(finished) <= sandboxRetainedFinished {
		return
	}
	sort.Slice(finished, func(a, b int) bool { return finished[a].at.Before(finished[b].at) })
	for _, old := range finished[:len(finished)-sandboxRetainedFinished] {
		delete(s.live, old.id)
	}
}

func sandboxJobLogPath(id string) string  { return sandboxJobDir + "/" + id + ".log" }
func sandboxJobExitPath(id string) string { return sandboxJobDir + "/" + id + ".exit" }

// backgroundLaunchCommand builds the shell script that starts command
// detached. Every interpolated path goes through shellQuote, so a command
// containing quotes, newlines or `$(…)` is passed through literally.
//
// Two details are load-bearing:
//
//   - `> log 2>&1 </dev/null` on the job means no file descriptor of the exec
//     stream is left open by the job, so the exec call returns the moment the
//     launcher shell exits. That is the whole difference from `nohup cmd &`,
//     which pins the stream (and therefore the call) to the job.
//   - `setsid` puts the job in its own session, so it survives the exec
//     request ending and `kill -KILL -pid` can reach the whole tree. Backends
//     whose image lacks setsid fall back to a backgrounded subshell — no
//     external binary is needed for that, and the launcher reports which form
//     it used so kill knows whether a process group of ours even exists.
func backgroundLaunchCommand(id, command string) string {
	logPath := shellQuote(sandboxJobLogPath(id))
	exitPath := shellQuote(sandboxJobExitPath(id))
	// `( … )` makes $? the status of the caller's command whatever shape it
	// had (pipeline, &&-chain, cd), and keeps a trailing comment from eating
	// the exit-code line.
	inner := "( " + command + "\n)\n__rc=$?\nprintf %s \"$__rc\" > " + exitPath
	redirect := " > " + logPath + " 2>&1 < /dev/null &"
	launch := strings.Join([]string{
		"mkdir -p " + shellQuote(sandboxJobDir) + " || exit 1",
		": > " + logPath,
		"rm -f " + exitPath,
		"__RB=$(command -v setsid 2>/dev/null || true)",
		`if [ -n "$__RB" ]; then`,
		"  __G=1",
		"  $__RB sh -c " + shellQuote(inner) + redirect,
		"else",
		"  __G=0",
		"  ( " + inner + " )" + redirect,
		"fi",
		`printf 'fcbg %s %s\n' "$!" "$__G"`,
	}, "\n")
	return launch + "\n"
}

// backgroundProbeCommand emits one machine-readable status line and then the
// bytes the caller has not seen yet. The status line is wrapped in 0x1F so
// arbitrary log content can never be mistaken for protocol, and the size is
// read before the tail so the bytes it names are stable (the log is
// append-only).
func backgroundProbeCommand(j *sandboxJob, cursor int) string {
	logPath := shellQuote(j.logPath)
	exitPath := shellQuote(j.exitPath)
	return strings.Join([]string{
		"if [ -f " + logPath + " ]; then",
		"  __S=$(wc -c < " + logPath + " 2>/dev/null || echo 0)",
		// `[ -n "$__C" ] || __C=?`: the exit-code file is created by the shell's
		// own redirection and filled by the next statement, so a kill (or a
		// foreign writer) can leave it empty. An empty field would then vanish
		// into strings.Fields and take the whole status marker with it.
		"  if [ -f " + exitPath + " ]; then __ST=exited; __C=$(cat " + exitPath + ` 2>/dev/null); [ -n "$__C" ] || __C=?; else __ST=running; __C=-; fi`,
		"else",
		"  __S=0; __ST=missing; __C=-",
		"fi",
		`printf '\037fcbg %s %s %s\037' "$__ST" "$__S" "$__C"`,
		"tail -c +" + strconv.Itoa(cursor+1) + " " + logPath + " 2>/dev/null | head -c " + strconv.Itoa(sandboxJobOutputCap),
	}, "\n") + "\n"
}

// backgroundKillCommand SIGKILLs the job. Group form when the launcher got a
// session of its own (verified live on docker: the wrapper shell, its subshell
// and the command all die together), single-pid form otherwise.
//
// SIGKILL rather than SIGTERM: a wrapper shell that is blocked waiting for a
// child defers SIGTERM until that child exits, so a TERM'd job can keep
// running — SIGKILL cannot be deferred or ignored.
func backgroundKillCommand(j *sandboxJob) string {
	if j.grouped {
		return "kill -KILL -" + j.pid + " 2>/dev/null || true\n"
	}
	// No session of our own: signal the job and its direct children, in that
	// order — killing the parent first would reparent the children and make
	// them unreachable for -P. pkill may be missing on a minimal image; then
	// only the job's own pid is signalled, which is this path's honest floor.
	return "pkill -KILL -P " + j.pid + " 2>/dev/null || true\n" +
		"kill -KILL " + j.pid + " 2>/dev/null || true\n"
}

// parseBackgroundLaunch reads the `fcbg <pid> <0|1>` handle the launcher
// prints. Split out so the wire shape is pinned without a sandbox.
func parseBackgroundLaunch(out string) (pid string, grouped bool, err error) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "fcbg" {
			continue
		}
		if _, convErr := strconv.Atoi(fields[1]); convErr != nil {
			continue
		}
		return fields[1], fields[2] == "1", nil
	}
	return "", false, fmt.Errorf("sandbox background launch: no job handle in output %q — the shell could not start a detached process", firstN(strings.TrimSpace(out), 200))
}

// parseBackgroundProbe splits the status marker from the log bytes.
func parseBackgroundProbe(out string) (status, code string, size int, body string, err error) {
	start := strings.IndexByte(out, 0x1f)
	if start < 0 {
		return "", "", 0, "", fmt.Errorf("sandbox background poll: no status marker in output %q", firstN(strings.TrimSpace(out), 200))
	}
	rest := out[start+1:]
	endRel := strings.IndexByte(rest, 0x1f)
	if endRel < 0 {
		return "", "", 0, "", fmt.Errorf("sandbox background poll: unterminated status marker in output %q", firstN(strings.TrimSpace(out), 200))
	}
	fields := strings.Fields(rest[:endRel])
	// Three fields is a real shape — an exit code nobody managed to write — and
	// a poll must still hand back the output it read. Losing the log over a
	// cosmetic field is the worse failure.
	if len(fields) < 3 || len(fields) > 4 || fields[0] != "fcbg" {
		return "", "", 0, "", fmt.Errorf("sandbox background poll: malformed status marker %q", rest[:endRel])
	}
	// BSD wc pads its count with spaces; strings.Fields already dropped them.
	size, convErr := strconv.Atoi(fields[2])
	if convErr != nil {
		return "", "", 0, "", fmt.Errorf("sandbox background poll: unparsable log size %q", fields[2])
	}
	code = "-"
	if len(fields) == 4 {
		code = fields[3]
	}
	if fields[1] == "exited" && (code == "" || code == "-") {
		code = "?"
	}
	return fields[1], code, size, rest[endRel+1:], nil
}

// output returns the log bytes produced since the previous call, followed by
// the job's status — same shape as the host-side bash_output.
func (j *sandboxJob) output(ctx context.Context, filter *regexp.Regexp) (string, error) {
	// Two attempts: if the log turns out to be shorter than our cursor the
	// sandbox was replaced under us, and the honest answer is to re-read from
	// offset 0 rather than to report silence.
	for attempt := 0; ; attempt++ {
		j.mu.Lock()
		cursor := j.cursor
		killed := j.killed
		j.mu.Unlock()

		out, err := j.runner.Exec(ctx, backgroundProbeCommand(j, cursor), sandboxJobOpTimeout)
		if err != nil {
			return "", fmt.Errorf("sandbox background poll: %w", err)
		}
		status, code, size, body, err := parseBackgroundProbe(out)
		if err != nil {
			return "", err
		}
		// A poll is the only moment the runtime learns a sandbox job is over
		// (there is no process handle to wait on), so retirement hangs off the
		// observation instead of off a ticker.
		if status == "exited" || status == "missing" {
			j.table.retireFinished(j)
		}

		if size < cursor && attempt == 0 && status != "missing" {
			j.mu.Lock()
			j.cursor = 0
			j.mu.Unlock()
			continue
		}

		j.mu.Lock()
		j.cursor = cursor + len(body)
		j.mu.Unlock()

		pending := size - cursor - len(body)
		if filter != nil && body != "" {
			body = filterLines(body, filter)
		}

		var sb strings.Builder
		if body != "" {
			sb.WriteString(body)
			if !strings.HasSuffix(body, "\n") {
				sb.WriteByte('\n')
			}
		}
		if pending > 0 {
			fmt.Fprintf(&sb, "[more output pending: %d bytes unread — call bash_output again]\n", pending)
		}
		switch {
		case killed:
			fmt.Fprintf(&sb, "[status] killed (SIGKILL sent to job %s)\n", j.id)
		case status == "exited":
			fmt.Fprintf(&sb, "[status] exited (code=%s)\n", code)
		case status == "missing":
			fmt.Fprintf(&sb, "[status] lost — job %s (pid %s) is gone: its log %s no longer exists, which means the sandbox was replaced\n", j.id, j.pid, j.logPath)
		default:
			sb.WriteString("[status] running\n")
		}
		return sb.String(), nil
	}
}

// kill stops the job. Idempotent: the kill command is harmless once the job
// is gone, and the next bash_output reports what actually happened.
func (j *sandboxJob) kill(ctx context.Context) (string, error) {
	j.mu.Lock()
	alreadyKilled := j.killed
	j.killed = true
	j.mu.Unlock()
	if alreadyKilled {
		return fmt.Sprintf("Job %s was already sent a kill — call bash_output(bash_id=%q) to see its final output.", j.id, j.id), nil
	}
	if _, err := j.runner.Exec(ctx, backgroundKillCommand(j), sandboxJobOpTimeout); err != nil {
		return "", fmt.Errorf("sandbox background kill: %w", err)
	}
	scope := "pid"
	if j.grouped {
		scope = "process group"
	}
	return fmt.Sprintf("Sent SIGKILL to job %s (%s %s). Poll bash_output(bash_id=%q) for its final output.", j.id, scope, j.pid, j.id), nil
}

// startedMessage is what the model sees when the job is launched. It has to
// carry the three facts the model needs to work with the job later: the id,
// where the raw log is inside the sandbox, and that the job outlives the call.
func (j *sandboxJob) startedMessage() string {
	return fmt.Sprintf(
		"Started background job %s (pid %s) in the sandbox: %s\n"+
			"Read its output with bash_output(bash_id=%q) (returns what it printed since the last call, plus running/exited status); stop it with kill_shell(bash_id=%q).\n"+
			"The job keeps running after this call returns, and its output is captured at %s inside the sandbox.",
		j.id, j.pid, j.command, j.id, j.id, j.logPath)
}

// startSandboxBackground launches command as a detached job inside ex and
// returns the model-facing confirmation line.
//
// The registry (not the executor) owns the job table: jobs must be reachable
// from bash_output / kill_shell, which are registered once per Registry while
// exec is re-registered per session.
func (r *Registry) startSandboxBackground(ctx context.Context, ex sandbox.Executor, command string) (string, error) {
	if ex == nil {
		return "", fmt.Errorf("run_in_background needs a sandbox executor, and none is bound to this session")
	}
	if r == nil || r.sandboxJobs == nil {
		return "", fmt.Errorf("run_in_background is unavailable: this registry has no sandbox job table")
	}
	job, err := r.sandboxJobs.start(ctx, ex, command)
	if err != nil {
		return "", fmt.Errorf("run_in_background failed: %w", err)
	}
	return MetaSandboxPrefix + job.startedMessage(), nil
}

// sandboxJobByID is the lookup bash_output / kill_shell use after the host
// shell manager comes up empty.
func (r *Registry) sandboxJobByID(id string) *sandboxJob {
	if r == nil {
		return nil
	}
	return r.sandboxJobs.get(id)
}
