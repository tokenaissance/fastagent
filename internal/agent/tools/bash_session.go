package tools

// Background-shell management — Claude-Code-style.
//
// One tool call (`exec` with run_in_background=true) launches a long-
// running command and returns immediately with a `bash_id`. The agent
// observes progress via `bash_output(bash_id)` (returns new stdout/stderr
// since the last call) and terminates with `kill_shell(bash_id)`.
//
// Scope (deliberately narrow):
//   - host-mode os/exec only; sandbox-mode background is a v2 follow-up
//   - tail-only observation; no send-keys / paste / interactive control
//     (those use cases route through tmux invoked from regular `exec`)
//   - sessions are agent-private and live until killed or Registry.Close

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// bufferCap caps the output retained per session. When exceeded, the
// oldest bytes are dropped FIFO. 4 MiB comfortably holds 30 minutes of
// dev-server logs while bounding total memory at 4 MiB × live sessions.
const bufferCap = 4 * 1024 * 1024

const (
	// shellExitedTailBytes is what an exited shell keeps. The 4 MiB cap exists to
	// hold a *running* job's output for polling; once the job has exited, the
	// only thing the tool still has to deliver is the tail that explains how it
	// ended, and the reader is told the rest is gone. Without this, every
	// background job an agent has ever run keeps its own 4 MiB resident for as
	// long as the agent lives — a container whose size is set by history rather
	// than by work in flight (docs 10 §10, G27).
	shellExitedTailBytes = 64 * 1024
	// shellRetainedExited bounds how many *exited* shells stay addressable by
	// bash_output. Running shells are never dropped.
	shellRetainedExited = 32
	// shellExitedTailNote is what a reader is told when the tail it is about to
	// receive is not the whole story. It has to name the real reason: "exceeded
	// the 4 MiB session cap" would be a false statement after an exit trim.
	shellExitedTailNote = "is no longer buffered (the shell exited; only its last 64 KiB is kept)"
)

// outputBuffer is a thread-safe FIFO byte buffer with a hard cap.
// It tracks the total number of bytes ever written ("absolute offsets")
// so callers reading "since last check" can survive truncations: when
// older bytes get dropped, an existing read cursor advances to the
// current head and the caller learns some output was lost.
type outputBuffer struct {
	mu       sync.Mutex
	data     []byte
	head     int // absolute offset of data[0]; equals total bytes dropped
	total    int // absolute offset just past data[end]; equals total bytes ever written
	maxBytes int
	// dropNote is the reason handed to a reader whose cursor fell behind. Empty
	// means the only reason so far is maxBytes.
	dropNote string
}

func newOutputBuffer(maxBytes int) *outputBuffer {
	return &outputBuffer{maxBytes: maxBytes}
}

// Write appends p to the buffer, dropping the oldest bytes if cap is
// exceeded. Always succeeds; returns len(p), nil to satisfy io.Writer
// (so it can plug directly into exec.Cmd.Stdout / Stderr).
func (b *outputBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	b.total += len(p)
	if len(b.data) > b.maxBytes {
		drop := len(b.data) - b.maxBytes
		b.data = b.data[drop:]
		b.head += drop
	}
	return len(p), nil
}

// readSince returns content from absolute offset `since` onward. If
// `since` is below the head (older content was dropped), returns
// everything currently held with `dropped=true` so the caller can warn
// the model. Returns the new absolute offset for the caller to remember.
func (b *outputBuffer) readSince(since int) (out []byte, dropped bool, newSince int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if since < b.head {
		dropped = true
		since = b.head
	}
	// since is now ≥ b.head, so start ≥ 0 by construction. The only
	// remaining out-of-range case is "caller's cursor is ahead of what
	// we've ever produced" (since > b.total), which means there's
	// nothing new yet.
	start := since - b.head
	if start > len(b.data) {
		return nil, dropped, b.total
	}
	// Copy out so the caller can release the lock without aliasing.
	out = append([]byte(nil), b.data[start:]...)
	return out, dropped, b.total
}

// trimTo drops the oldest bytes so at most maxBytes remain, and records why in
// the words a reader will see. Absolute offsets are preserved (head advances by
// exactly the number of bytes dropped), so a cursor that was behind learns the
// truth from readSince's `dropped` flag instead of silently mis-splicing output.
//
// The remaining bytes are copied rather than sliced: slicing would keep the
// whole 4 MiB backing array alive, which is the opposite of the point.
func (b *outputBuffer) trimTo(maxBytes int, note string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(b.data) > maxBytes {
		drop := len(b.data) - maxBytes
		tail := make([]byte, maxBytes)
		copy(tail, b.data[drop:])
		b.data = tail
		b.head += drop
	}
	b.dropNote = note
}

// dropNoteText reports the true reason bytes are missing, or "" when nothing has
// been dropped for a reason other than the cap.
func (b *outputBuffer) dropNoteText() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropNote
}

// bashSession is a single backgrounded shell command and the state
// needed to observe and terminate it.
type bashSession struct {
	id        string
	command   string
	startedAt time.Time

	cmd    *exec.Cmd
	cancel context.CancelFunc
	out    *outputBuffer

	// readCursor is the absolute offset already returned to bash_output.
	// Guarded by readMu (separate from outputBuffer.mu so writers and
	// readers don't contend on one lock).
	readMu     sync.Mutex
	readCursor int

	// done flips to true exactly once when cmd.Wait returns. exitCode is
	// written before done; reading exitCode is safe iff done is true
	// (acquire-release via atomic.Bool).
	done     atomic.Bool
	exitCode int
	exitErr  error

	// finishedAt is when this shell was retired. Written under the shell
	// manager's mutex (retireExited) and read under it, so it needs no lock of
	// its own. It orders retirement when the retained-exited cap has to choose
	// which entries to forget.
	finishedAt time.Time
}

// status reports a session's runtime state.
type bashStatus int

const (
	statusRunning bashStatus = iota
	statusExited
)

// snapshot returns a consistent view of the session's terminal state.
// status is observable while running; exitCode is meaningful only when
// status == statusExited.
func (s *bashSession) snapshot() (status bashStatus, exitCode int, exitErr error) {
	if !s.done.Load() {
		return statusRunning, 0, nil
	}
	return statusExited, s.exitCode, s.exitErr
}

// readNew pulls all output produced since the last readNew call on this
// session. dropped=true means the buffer rolled past the read cursor
// and some bytes are permanently gone.
func (s *bashSession) readNew() (out []byte, dropped bool) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	out, dropped, next := s.out.readSince(s.readCursor)
	s.readCursor = next
	return out, dropped
}

// dropNoteText is the true reason bytes are missing, in the words the tool
// prints. A retired shell has been trimmed to its exit tail, and saying "it
// exceeded the 4 MiB cap" about that would be a false statement about a real
// loss (docs 08 §2, O1).
func (s *bashSession) dropNoteText() string {
	if note := s.out.dropNoteText(); note != "" {
		return note
	}
	return "exceeded the 4 MiB session cap and was dropped"
}

// kill signals SIGKILL via the cancel function tied to the session's
// own context. Returns nil if the session was already done. Idempotent.
func (s *bashSession) kill() error {
	if s.done.Load() {
		return nil
	}
	s.cancel()
	return nil
}

// shellManager owns every backgrounded session for a Registry. Sessions
// outlive the request context that started them — they die on kill, on
// natural exit, or on Registry.Close.
type shellManager struct {
	mu      sync.Mutex
	shells  map[string]*bashSession
	counter int
	closed  bool // Close was called; Start refuses new sessions
}

func newShellManager() *shellManager {
	return &shellManager{shells: make(map[string]*bashSession)}
}

// Start launches command via `sh -c` and returns its bash_id. The
// session lives until kill or natural exit; the caller's ctx does NOT
// propagate to the child — that ctx dies at turn end and would take
// every backgrounded process with it.
func (m *shellManager) Start(command string, env []string) (*bashSession, error) {
	if command == "" {
		return nil, errors.New("command is required")
	}

	// Refuse new sessions after Close. Without this, a Start that
	// races with shutdown could land its session in the post-Close map
	// and leak the process forever.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("shell manager closed")
	}
	m.mu.Unlock()

	// context.Background here is intentional — a backgrounded shell
	// must outlive the turn that spawned it. The session's own cancel
	// is the only path that terminates it before natural exit.
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	// Fail closed: if the caller didn't pass an explicit env, build a
	// scrubbed one rather than letting Go default to bare os.Environ()
	// inheritance — that path is how daemon secrets reached chat replies.
	if env != nil {
		cmd.Env = env
	} else {
		cmd.Env = buildSubprocessEnv(nil)
	}

	// Spawn into a fresh process group so kill_shell reaches every
	// descendant (e.g. `sh -c "npm run dev"` forks node — without group
	// kill, killing sh leaves node running). Override CommandContext's
	// default Cancel (which only kills cmd.Process directly) to send
	// SIGKILL to the whole group.
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killProcessGroup(cmd.Process.Pid)
	}

	out := newOutputBuffer(bufferCap)
	cmd.Stdout = out
	cmd.Stderr = out // outputBuffer's Write is mutex-protected so concurrent stdout+stderr is safe

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start background shell: %w", err)
	}

	// Re-check closed under the same lock that does the insert. The
	// pre-Start check above is just an early-out to avoid forking sh
	// when we know we're shutting down — the race-safe verdict is here.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel() // group-kill the shell we just spawned
		return nil, errors.New("shell manager closed")
	}
	m.counter++
	id := fmt.Sprintf("bash_%d", m.counter)
	s := &bashSession{
		id:        id,
		command:   command,
		startedAt: time.Now(),
		cmd:       cmd,
		cancel:    cancel,
		out:       out,
	}
	m.shells[id] = s
	m.mu.Unlock()

	// Reaper: cmd.Wait returns when the process exits OR when ctx is
	// cancelled (kill). Capture exit code, then flip done.
	go func() {
		err := cmd.Wait()
		s.exitErr = err
		var ee *exec.ExitError
		if err == nil {
			s.exitCode = 0
		} else if errors.As(err, &ee) {
			s.exitCode = ee.ExitCode()
		} else {
			// Killed by cancel, or pipe / IO error. Use -1 to signal
			// "abnormal termination" — the agent can disambiguate from
			// the exit_err string returned by snapshot.
			s.exitCode = -1
		}
		s.done.Store(true)
		// Retire, don't delete: bash_output stays useful after exit so the agent
		// can fetch the final output and exit status. What changes here is that
		// the entry stops being *unbounded in size and number* — the buffer is
		// trimmed to the tail that explains the exit, and the retained-exited
		// population is capped (G27). The trim happens BEFORE done is published,
		// so a reader that sees "exited" cannot be handed a tail without also
		// being told it is a tail.
		//
		// Registry.Close is not the retirement path: it has no production caller
		// (the agent's Registry lives as long as the UserSpace does), which is
		// exactly why the bound has to live here, on the transition, instead of
		// on a teardown that never runs.
		m.retireExited(id, s)
	}()

	return s, nil
}

// retireExited records that a shell has finished: it trims the shell's output to
// the tail worth keeping and forgets the oldest exited shells beyond
// shellRetainedExited. Running shells are never touched.
func (m *shellManager) retireExited(id string, s *bashSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.shells[id]; !ok || cur != s {
		return // already forgotten, or replaced by a newer shell under the same id
	}
	// Trim first: this is what turns "every job this agent ever ran holds 4 MiB"
	// into "every job holds at most the tail".
	s.out.trimTo(shellExitedTailBytes, shellExitedTailNote)
	s.finishedAt = time.Now()

	var exited []*bashSession
	for _, other := range m.shells {
		if other.done.Load() {
			exited = append(exited, other)
		}
	}
	if len(exited) <= shellRetainedExited {
		return
	}
	sort.Slice(exited, func(a, b int) bool { return exited[a].finishedAt.Before(exited[b].finishedAt) })
	for _, old := range exited[:len(exited)-shellRetainedExited] {
		delete(m.shells, old.id)
	}
}

// Get fetches a session by bash_id, or nil if not found.
func (m *shellManager) Get(id string) *bashSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shells[id]
}

// Close kills every live session, clears the registry, and refuses any
// future Start. Idempotent. Called from Registry.Close on agent
// shutdown so backgrounded processes don't outlive their owner.
func (m *shellManager) Close() {
	m.mu.Lock()
	shells := m.shells
	m.shells = make(map[string]*bashSession)
	m.closed = true
	m.mu.Unlock()
	for _, s := range shells {
		s.cancel()
	}
}

// list returns a snapshot of all current sessions, sorted by id.
// Currently used only by tests; exposed for future list_shells tool.
func (m *shellManager) list() []*bashSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*bashSession, 0, len(m.shells))
	for _, s := range m.shells {
		out = append(out, s)
	}
	return out
}
