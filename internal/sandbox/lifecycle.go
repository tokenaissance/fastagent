package sandbox

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// bytesReader wraps a byte slice as an io.Reader — inlined helper so flush
// code doesn't clutter with bytes.NewReader calls.
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// LifecyclePool wraps any ExecutorPool with two knobs that matter for cost
// in multi-tenant cloud deployments:
//
//  1. Lazy creation — sandboxes aren't spun up until the first tool call.
//     An agent that just chats (no exec/read_file/write_file) never starts
//     one, so idle users pay nothing for sandbox compute.
//  2. Idle eviction — a background sweeper Release()s sandboxes that have
//     been unused for IdleTTL. The next call recreates them; in the
//     meantime nothing is running.
//
// Backend-agnostic: works with DockerExecutorPool, E2B, or any future
// implementation. The inner pool still handles the actual create/destroy.
type LifecyclePool struct {
	inner   ExecutorPool
	idleTTL time.Duration
	sweep   time.Duration

	mu sync.Mutex
	// Both maps are keyed on poolKey(agentID, sessionID) so per-session
	// sandboxes are tracked independently. lastUsed drives idle eviction;
	// hydrated tracks whether we've already copied workspace.Store
	// contents into this sandbox (drops to false on eviction so the next
	// lazy-creation re-hydrates from the durable store).
	lastUsed map[string]time.Time
	hydrated map[string]bool
	// inUse counts operations currently running against a scope. lastUsed is
	// stamped when an operation STARTS, so an exec longer than idleTTL used to
	// look idle and had its sandbox destroyed underneath it — a 20-minute build
	// against a 10-minute TTL. The sweeper skips busy scopes instead.
	inUse map[string]int
	// scopes maps the same composite key back to (agentID, sessionID) so
	// flush + release paths can talk to the right workspace scope without
	// re-parsing the key.
	scopes map[string]sandboxScope

	// workspace is the optional blob store that bootstraps /workspace on
	// sandbox creation. When nil, sandboxes start empty and rely on
	// write_file tool calls (which already write through workspace.Store)
	// to produce files the agent later reads via read_file.
	workspace workspace.Store

	stopCh chan struct{}
	done   chan struct{}
	// launched records that Start has run, so a second Start does not spawn a
	// second sweeper and CloseAll knows whether there is a loop to wait for —
	// without it, CloseAll on a never-started pool blocked forever.
	launched bool

	// longOpRenew is how long an operation may run before the lease is renewed
	// again when it finishes. The lease is already renewed when an operation
	// starts (Get → reconcile), so only operations long enough to outlive the
	// TTL on their own need the trailing renew; keeping it conditional keeps the
	// common path free of an extra round trip.
	longOpRenew time.Duration
}

// sandboxScope is the (agentID, projectID, sessionID) tuple a sandbox
// belongs to. Stored alongside the composite map key so lifecycle code
// can call back into ExecutorPool.Get/Release with the right scope
// without re-parsing.
type sandboxScope struct {
	agentID   string
	projectID string
	sessionID string
}

// NewLifecyclePool wraps inner with idle tracking. idleTTL=0 disables
// eviction (everything stays alive); sweep=0 uses a sensible default.
func NewLifecyclePool(inner ExecutorPool, idleTTL, sweep time.Duration) *LifecyclePool {
	if sweep <= 0 {
		sweep = 30 * time.Second
	}
	return &LifecyclePool{
		inner:       inner,
		idleTTL:     idleTTL,
		sweep:       sweep,
		lastUsed:    make(map[string]time.Time),
		hydrated:    make(map[string]bool),
		inUse:       make(map[string]int),
		scopes:      make(map[string]sandboxScope),
		longOpRenew: defaultLongOpRenew,
		stopCh:      make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// defaultLongOpRenew is the operation length past which the trailing lease
// renew is worth its round trip. Comfortably below the 15-minute lease TTL, so
// an operation that could outlive the TTL always renews, while the common
// sub-second tool call never pays for it.
const defaultLongOpRenew = 2 * time.Minute

// LeaseRenewer is implemented by pools that hold a time-limited shared lease
// for a scope (E2B does; docker has none). The lifecycle layer calls it after
// an operation long enough to have outlived the lease, so a busy sandbox is not
// declared lapsed while its task is still running.
type LeaseRenewer interface {
	RenewLease(ctx context.Context, agentID, projectID, sessionID string) error
}

// ScopeSleeper is implemented by pools whose backend can put a scope's sandbox
// to sleep instead of destroying it. E2B pauses the instance — filesystem and
// memory, running processes included — and keeps it, unbilled and outside the
// concurrency limit, so the next caller resumes the same sandbox rather than
// paying for a new one. A pool without it (docker) is released as before.
//
// SleepScope reports paused=true when the instance is now asleep. paused=false
// with a nil error means there was nothing to sleep; an error means the attempt
// failed and the caller must decide — see evictIdle, which never destroys a
// sandbox it could not sleep.
type ScopeSleeper interface {
	SleepScope(ctx context.Context, agentID, projectID, sessionID string) (paused bool, err error)
}

// ScopeExtender is implemented by pools whose backend can push out a running
// sandbox's expiry. It exists for one hazard: an operation that starts near the
// end of the instance's life and outlives it. With autoPause on the expiry
// pauses the sandbox mid-operation and cuts our stream, so the caller sees a
// truncated response even though the process survives in the snapshot.
//
// A pool without it (docker) simply has no expiry to move.
type ScopeExtender interface {
	ExtendScope(ctx context.Context, agentID, projectID, sessionID string, d time.Duration) error
}

// extendThreshold is the operation budget past which moving the expiry is worth
// an API round trip. Shorter operations can in principle still straddle an
// expiry that is seconds away; that surfaces as a truncated stream, which the
// caller already classifies and can retry. Paying a call per tool call to
// close that last few-seconds window would cost more than it saves.
const extendThreshold = 60 * time.Second

// extendSlack is added to the operation's budget so the expiry lands after the
// operation ends rather than exactly at it.
const extendSlack = 2 * time.Minute

// beginUse marks one operation as running against sc and refreshes the idle
// clock. Paired with endUse.
func (p *LifecyclePool) beginUse(sc sandboxScope) {
	k := poolKey(sc.agentID, sc.projectID, sc.sessionID)
	p.mu.Lock()
	p.inUse[k]++
	p.lastUsed[k] = time.Now()
	p.scopes[k] = sc
	p.mu.Unlock()
}

// endUse releases the in-use marker and, when the operation ran long enough to
// have outlived the lease, renews it before anyone else can take the scope
// over mid-task.
func (p *LifecyclePool) endUse(ctx context.Context, sc sandboxScope, started time.Time) {
	k := poolKey(sc.agentID, sc.projectID, sc.sessionID)
	p.mu.Lock()
	if p.inUse[k] > 0 {
		p.inUse[k]--
	}
	if p.inUse[k] == 0 {
		delete(p.inUse, k)
	}
	p.lastUsed[k] = time.Now()
	p.mu.Unlock()

	if time.Since(started) < p.longOpRenew {
		return
	}
	renewer, ok := p.inner.(LeaseRenewer)
	if !ok {
		return
	}
	// Detached from the operation's ctx: the renew has to happen even when the
	// operation was cut short by a deadline, which is exactly when the lease is
	// most likely to be stale.
	renewCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := renewer.RenewLease(renewCtx, sc.agentID, sc.projectID, sc.sessionID); err != nil {
		slog.Warn("sandbox lease renew after a long operation failed",
			"agent", sc.agentID, "session", sc.sessionID, "error", err)
	}
}

// SetWorkspace installs the durable blob store used to bootstrap each
// sandbox on first tool call. Pass nil to disable hydrate (sandboxes start
// with empty /workspace).
//
// Pools that hydrate themselves at create time (E2B uses one tar+exec
// round-trip for skills + workspace; see E2BExecutorPool.Get → Hydrate)
// receive the same store via SetWorkspace on the inner pool so they
// have a chance to fold it into the bulk upload — that's much faster
// and more reliable than the per-file fallback we still keep here for
// docker.
func (p *LifecyclePool) SetWorkspace(ws workspace.Store) {
	p.workspace = ws
	if sw, ok := p.inner.(workspaceAware); ok {
		sw.SetWorkspace(ws)
	}
}

// workspaceAware is implemented by inner pools that fold workspace
// hydration into their own create-time bulk upload (so LifecyclePool
// shouldn't double-hydrate via the per-file path).
type workspaceAware interface {
	SetWorkspace(ws workspace.Store)
}

// Start the idle sweep goroutine. Safe to call multiple times; only the
// first start actually kicks off the loop.
func (p *LifecyclePool) Start() {
	p.mu.Lock()
	first := !p.launched
	p.launched = true
	p.mu.Unlock()
	if !first {
		return
	}
	if p.idleTTL <= 0 {
		close(p.done) // nothing to do; keep Shutdown() cheap
		return
	}
	go p.loop()
}

func (p *LifecyclePool) loop() {
	defer close(p.done)
	t := time.NewTicker(p.sweep)
	defer t.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-t.C:
			p.evictIdle()
		}
	}
}

// evictIdle scans lastUsed and Release()s anything older than idleTTL.
// Held per-iteration lock; Release may be slow (destroys a container), so
// we release the map lock before the actual teardown to avoid blocking new
// Get()s on other agents.
func (p *LifecyclePool) evictIdle() {
	cutoff := time.Now().Add(-p.idleTTL)
	p.mu.Lock()
	toEvict := make([]sandboxScope, 0)
	for k, t := range p.lastUsed {
		// An operation is running against this scope: its lastUsed was stamped
		// when that operation started, so age here means "busy", not "idle".
		if p.inUse[k] > 0 {
			continue
		}
		if t.Before(cutoff) {
			toEvict = append(toEvict, p.scopes[k])
		}
	}
	// Remove from maps under lock so a racing Get doesn't mistake an
	// evicted sandbox for a live one. A scope we are going to SLEEP keeps its
	// hydrated flag: the sandbox still holds its filesystem, so re-uploading the
	// workspace on the next use would be pure waste. A scope we are going to
	// release loses it, so the next lazy-creation re-syncs from the store.
	sleeper, canSleep := p.inner.(ScopeSleeper)
	for _, sc := range toEvict {
		k := poolKey(sc.agentID, sc.projectID, sc.sessionID)
		delete(p.lastUsed, k)
		if !canSleep {
			delete(p.hydrated, k)
		}
		delete(p.scopes, k)
	}
	p.mu.Unlock()

	for _, sc := range toEvict {
		// Best-effort flush: if the executor implements
		// WorkspaceSnapshotter and we have a workspace store, upload
		// anything the sandbox wrote (that wasn't already written via
		// write_file) before it goes away or to sleep.
		p.flushIfSupported(sc)

		if canSleep && p.sleepOrRelease(sleeper, sc) {
			continue
		}
		if err := p.inner.Release(sc.agentID, sc.projectID, sc.sessionID); err != nil {
			slog.Warn("sandbox evict failed", "agent", sc.agentID, "session", sc.sessionID, "error", err)
			continue
		}
		slog.Info("sandbox evicted (idle)", "agent", sc.agentID, "session", sc.sessionID, "idleTTL", p.idleTTL)
	}
}

// sleepOrRelease puts an idle scope's sandbox to sleep. It reports true when the
// caller should stop — either because the instance is asleep, or because the
// attempt failed in a way that must NOT lead to destruction.
//
// The direction matters: releasing a sandbox we merely failed to pause would
// destroy a healthy instance (a provider that cannot pause, a transient API
// error). Only "there was nothing to sleep" — no cached executor, or one the
// sleeper already found gone and cleaned up — falls through to Release.
func (p *LifecyclePool) sleepOrRelease(sleeper ScopeSleeper, sc sandboxScope) bool {
	paused, err := sleeper.SleepScope(context.Background(), sc.agentID, sc.projectID, sc.sessionID)
	switch {
	case err == nil && paused:
		slog.Info("sandbox asleep (idle)",
			"agent", sc.agentID, "session", sc.sessionID, "idleTTL", p.idleTTL)
		return true
	case err == nil:
		// Nothing to sleep: fall through and release.
		return false
	default:
		// Keep the sandbox: never destroy what we could not sleep — and keep it
		// in the sweep set, or it would drop out of idle tracking entirely and
		// run (and bill) until the process exits. A sleep that fails now may work
		// at the next sweep.
		slog.Warn("sandbox sleep failed; leaving it running and retrying next sweep",
			"agent", sc.agentID, "session", sc.sessionID, "error", err)
		p.keepForNextSweep(sc)
		return true
	}
}

// keepForNextSweep restores the idle bookkeeping evictIdle removes before a
// teardown attempt, timestamped now so the next attempt happens one idleTTL from
// here rather than immediately. It is a no-op when a racing Get already
// re-registered the scope (which is the other reason an entry can reappear).
func (p *LifecyclePool) keepForNextSweep(sc sandboxScope) {
	k := poolKey(sc.agentID, sc.projectID, sc.sessionID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, live := p.lastUsed[k]; live {
		return
	}
	p.lastUsed[k] = time.Now()
	p.scopes[k] = sc
}

// extendBudget moves the sandbox's expiry past the end of an operation whose
// budget is long enough to matter. Best-effort: the operation still runs if the
// call fails — it just runs with the extra risk of being paused mid-flight, and
// the truncation that follows is classified like any other cut stream.
func (p *LifecyclePool) extendBudget(ctx context.Context, sc sandboxScope, opBudget time.Duration) {
	if opBudget < extendThreshold {
		return
	}
	ext, ok := p.inner.(ScopeExtender)
	if !ok {
		return
	}
	extendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := ext.ExtendScope(extendCtx, sc.agentID, sc.projectID, sc.sessionID, opBudget+extendSlack); err != nil {
		slog.Warn("could not extend the sandbox timeout before a long operation",
			"agent", sc.agentID, "session", sc.sessionID,
			"opBudget", opBudget, "error", err)
	}
}

// flushIfSupported snapshots the sandbox workspace and uploads anything
// that isn't already in the durable store. Skips silently when the backend
// doesn't implement WorkspaceSnapshotter (docker is the only current
// implementer besides E2B) or when no workspace.Store is configured.
func (p *LifecyclePool) flushIfSupported(sc sandboxScope) {
	if p.workspace == nil {
		return
	}
	ex, err := p.inner.Get(context.Background(), sc.agentID, sc.projectID, sc.sessionID)
	if err != nil {
		return
	}
	p.syncSnapshot(context.Background(), sc, ex, "evict")
}

// syncSnapshot does the actual snapshot+diff+Put work. Pulled out of
// flushIfSupported so post-exec sync (lazyExecutor.Exec) can reuse it
// without re-fetching the executor through the inner pool. `cause` is a
// log tag so we can tell evict-flushes from per-exec syncs in slog.
func (p *LifecyclePool) syncSnapshot(ctx context.Context, sc sandboxScope, ex Executor, cause string) {
	if p.workspace == nil {
		return
	}
	snapper, ok := ex.(WorkspaceSnapshotter)
	if !ok {
		return
	}
	files, err := snapper.SnapshotWorkspace(ctx)
	if err != nil {
		slog.Warn("sandbox sync: snapshot failed", "agent", sc.agentID, "session", sc.sessionID, "cause", cause, "error", err)
		return
	}
	written := 0
	for path, data := range files {
		// Skip files that the store already has with identical size —
		// avoids rewriting every file every sync when nothing changed.
		// Content equality would be stricter but requires a full
		// round-trip per file; size is usually enough.
		if info, err := p.workspace.Stat(ctx, sc.agentID, sc.projectID, sc.sessionID, path); err == nil && info.Size == int64(len(data)) {
			continue
		}
		if err := p.workspace.Put(ctx, sc.agentID, sc.projectID, sc.sessionID, path, bytesReader(data), int64(len(data)), ""); err != nil {
			slog.Warn("sandbox sync: put failed", "agent", sc.agentID, "session", sc.sessionID, "cause", cause, "path", path, "error", err)
			continue
		}
		written++
	}
	if written > 0 {
		slog.Info("sandbox synced to workspace store", "agent", sc.agentID, "session", sc.sessionID, "cause", cause, "files", written)
	}
}

// Get returns a lazy proxy: tool calls on it will fetch the underlying
// executor from the inner pool on demand (creating a new sandbox if
// needed) and tick the last-used timestamp.
//
// Contract matches ExecutorPool.Get so LifecyclePool is a drop-in wrapper.
func (p *LifecyclePool) Get(ctx context.Context, agentID, projectID, sessionID string) (Executor, error) {
	return &lazyExecutor{pool: p, scope: sandboxScope{agentID: agentID, projectID: projectID, sessionID: sessionID}}, nil
}

// Release forwards to the inner pool and drops the lastUsed entry. Useful
// for explicit teardown (agent deletion) — normal flow relies on idle
// eviction.
func (p *LifecyclePool) Release(agentID, projectID, sessionID string) error {
	k := poolKey(agentID, projectID, sessionID)
	p.mu.Lock()
	delete(p.lastUsed, k)
	delete(p.hydrated, k)
	delete(p.scopes, k)
	p.mu.Unlock()
	return p.inner.Release(agentID, projectID, sessionID)
}

// CloseAll stops the sweeper and tears down every live sandbox. Called on
// gateway shutdown; skipping this would leak E2B instances that cost money
// until their max-TTL expires.
func (p *LifecyclePool) CloseAll() {
	select {
	case <-p.stopCh:
		// already stopped
	default:
		close(p.stopCh)
	}
	p.mu.Lock()
	launched := p.launched
	p.mu.Unlock()
	if launched {
		<-p.done
	}
	p.inner.CloseAll()
	p.mu.Lock()
	p.lastUsed = make(map[string]time.Time)
	p.hydrated = make(map[string]bool)
	p.inUse = make(map[string]int)
	p.scopes = make(map[string]sandboxScope)
	p.mu.Unlock()
}

// inner fetches the underlying Executor, creating on first call. Separate
// from Get() so lazyExecutor can update lastUsed each time. On first
// creation (either fresh or post-eviction) it hydrates /workspace from the
// configured workspace.Store so exec'd commands see the files that
// write_file has produced in previous sessions.
func (p *LifecyclePool) getInner(ctx context.Context, sc sandboxScope) (Executor, error) {
	k := poolKey(sc.agentID, sc.projectID, sc.sessionID)
	p.mu.Lock()
	needsHydrate := !p.hydrated[k]
	p.lastUsed[k] = time.Now()
	p.scopes[k] = sc
	if needsHydrate {
		p.hydrated[k] = true // set eagerly so a concurrent second call doesn't double-hydrate
	}
	p.mu.Unlock()

	ex, err := p.inner.Get(ctx, sc.agentID, sc.projectID, sc.sessionID)
	if err != nil {
		// Roll back the hydrated flag so a retry will try again.
		p.mu.Lock()
		p.hydrated[k] = false
		p.mu.Unlock()
		return nil, err
	}
	// Policy C (docs/sandbox-scope-leak.md §9): a store listing that failed
	// leaves a usable sandbox with an EMPTY /workspace. Hand it out — a store
	// hiccup must not cost the scope its warm instance — but keep hydrated[k]
	// false so the next use re-hydrates, and let the tools declare the state to
	// the turn. Today's shape (set eagerly above, rolled back only when Get
	// itself fails) is why nothing ever retried.
	if ws, ok := ex.(interface{ WorkspaceStale() bool }); ok && ws.WorkspaceStale() {
		p.mu.Lock()
		p.hydrated[k] = false
		p.mu.Unlock()
	}
	// Skip the per-file fallback when the inner pool already pushed
	// /workspace as part of its own bulk hydrate (E2B does this — one
	// tar.gz over exec covers /skills and /workspace in one shot).
	// Otherwise (docker), copy each object via ex.WriteFile.
	if needsHydrate && p.workspace != nil {
		if _, selfHydrates := p.inner.(workspaceAware); !selfHydrates {
			hydrateWorkspace(ctx, p.workspace, ex, sc.agentID, sc.projectID, sc.sessionID, defaultSandboxRoot)
		}
	}
	return ex, nil
}

// lazyExecutor is what Get() hands back. Each tool call routes through
// pool.getInner which (a) refreshes the idle timer and (b) lazily creates
// the real sandbox if this is the first call since last eviction.
type lazyExecutor struct {
	pool  *LifecyclePool
	scope sandboxScope
}

func (l *lazyExecutor) Exec(ctx context.Context, command string, timeout time.Duration) (string, error) {
	ex, err := l.pool.getInner(ctx, l.scope)
	if err != nil {
		return "", err
	}
	// Hold the scope's in-use marker for the whole operation: an exec may run
	// longer than idleTTL (600s budgets against a 10-minute TTL), and without
	// this the sweeper would destroy the sandbox underneath it.
	started := time.Now()
	l.pool.beginUse(l.scope)
	l.pool.extendBudget(ctx, l.scope, timeout)
	// Deferred rather than called inline: the post-exec sync below reads the
	// sandbox, and a scope that is no longer marked in use could be swept
	// (paused) in the middle of it.
	defer l.pool.endUse(ctx, l.scope, started)
	out, execErr := ex.Exec(ctx, command, timeout)
	// Post-exec sync only for cloud sandboxes (RemoteWorkspace marker).
	// Docker's /workspace is bind-mounted to host so files appear
	// instantly with no sync needed; rerunning the snapshot+Put cycle
	// after every exec would just churn the workspace.Store
	// (especially expensive when it's S3-backed). E2B's /workspace
	// lives inside the cloud sandbox; without this pull, files the
	// skill writes (image-tool's /workspace/gen_xxx.webp) never
	// reach the host and the UI shows broken images.
	// Best-effort — never overrides the exec result.
	if _, remote := ex.(RemoteWorkspace); remote {
		l.pool.syncSnapshot(ctx, l.scope, ex, "post-exec")
	}
	return out, execErr
}

func (l *lazyExecutor) ReadFile(ctx context.Context, path string) (string, error) {
	ex, err := l.pool.getInner(ctx, l.scope)
	if err != nil {
		return "", err
	}
	started := time.Now()
	l.pool.beginUse(l.scope)
	defer l.pool.endUse(ctx, l.scope, started)
	return ex.ReadFile(ctx, path)
}

func (l *lazyExecutor) WriteFile(ctx context.Context, path, content string) (string, error) {
	ex, err := l.pool.getInner(ctx, l.scope)
	if err != nil {
		return "", err
	}
	started := time.Now()
	l.pool.beginUse(l.scope)
	defer l.pool.endUse(ctx, l.scope, started)
	out, writeErr := ex.WriteFile(ctx, path, content)
	// Mirror writes to the durable store on cloud sandboxes — same
	// reasoning as the post-exec sync above. Without this, write_file
	// (and apply_patch) calls that fall through to ex.WriteFile (any
	// absolute /workspace path — see file.go's isWorkspacePath, which
	// rejects abs paths) only land in the E2B sandbox and disappear on
	// idle eviction, never reaching the host workspace.Store the UI and
	// signed URLs read from. Targeted single-file Put rather than
	// syncSnapshot: we already have the bytes in memory, no need for a
	// full tar round-trip per write. Best-effort — never overrides the
	// write result.
	if writeErr == nil {
		if _, remote := ex.(RemoteWorkspace); remote {
			l.pool.mirrorSandboxWrite(ctx, l.scope, path, content)
		}
	}
	return out, writeErr
}

// mirrorSandboxWrite copies a single sandbox-side write into the durable
// workspace.Store. Skips paths outside /workspace (e.g. /tmp/, /home/user/)
// since those have no store mapping. Mirrors the sessionID-scoped Put that
// syncSnapshot uses, so write_file and exec-generated files land in the
// same store location.
func (p *LifecyclePool) mirrorSandboxWrite(ctx context.Context, sc sandboxScope, sandboxPath, content string) {
	if p.workspace == nil {
		return
	}
	const prefix = "/workspace/"
	if !strings.HasPrefix(sandboxPath, prefix) {
		return
	}
	key := strings.TrimPrefix(sandboxPath, prefix)
	if key == "" {
		return
	}
	if err := p.workspace.Put(ctx, sc.agentID, sc.projectID, sc.sessionID, key,
		bytesReader([]byte(content)), int64(len(content)), ""); err != nil {
		slog.Warn("sandbox sync: write_file mirror failed",
			"agent", sc.agentID, "project", sc.projectID, "session", sc.sessionID,
			"path", key, "error", err)
		return
	}
	slog.Debug("sandbox synced to workspace store", "agent", sc.agentID, "session", sc.sessionID, "cause", "post-write", "path", key)
}

func (l *lazyExecutor) ListDir(ctx context.Context, path string) (string, error) {
	ex, err := l.pool.getInner(ctx, l.scope)
	if err != nil {
		return "", err
	}
	started := time.Now()
	l.pool.beginUse(l.scope)
	defer l.pool.endUse(ctx, l.scope, started)
	return ex.ListDir(ctx, path)
}

// Close on a lazy proxy is a no-op — the underlying executor's lifetime is
// owned by the LifecyclePool, not by any individual caller holding a
// handle. Real teardown happens via LifecyclePool.Release / CloseAll.
func (l *lazyExecutor) Close() error { return nil }

// Backend reports the provider name by delegating to the inner pool, which
// knows it as a type-level constant. No need to materialize a real
// executor — the answer is static for the pool's lifetime.
func (l *lazyExecutor) Backend() string { return l.pool.inner.Backend() }

// Backend on LifecyclePool delegates to the wrapped inner pool. Mirrors
// the per-executor Backend so callers can ask "which provider is this?"
// at any layer of the wrapper stack.
func (p *LifecyclePool) Backend() string { return p.inner.Backend() }

// Ensure interfaces are satisfied.
var (
	_ Executor     = (*lazyExecutor)(nil)
	_ ExecutorPool = (*LifecyclePool)(nil)
)
