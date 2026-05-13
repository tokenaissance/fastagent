package goal

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// idleProbeInterval is the safety-net cadence at which a GoalRuntime
// re-checks its goal even if no Trigger() arrived. The primary path is
// event-driven (Trigger from HandleMessage / AfterToolCall / external
// mutation); this probe just catches the case where a trigger source
// is misconfigured and the goal would otherwise sit forever. Tuned
// large enough not to matter cost-wise.
const idleProbeInterval = 30 * time.Second

// runtimeIdleShutdown is how long a GoalRuntime can sit without any
// event activity before it self-terminates. Without an evict hook on
// session.Manager (see docs/design/goal.md §11 risk 10), this self-
// shutdown is how we avoid leaking one goroutine per session forever.
const runtimeIdleShutdown = 30 * time.Minute

// GoalRuntime drives the continuation loop for a single session that
// has an active goal. It owns no goal state of its own — the source
// of truth is the Store. The runtime's job is to react to events
// ("turn finished", "external mutation", "tool completed") and, when
// the gates align, inject a continuation prompt by publishing to the
// bus the same way cron does.
//
// One GoalRuntime per session_key. Created lazily by GoalManager when
// a goal first appears for that session, torn down when the goal
// enters a terminal state or the runtime sits idle long enough.
//
// Concurrency: trigger() is safe from any goroutine. continuation
// attempts are serialized by continuationLock (try-acquire — a
// concurrent attempt just skips, rather than queuing up duplicates).
// Accounting mutations are serialized by accountingLock (used by
// phase 2 step 3 when token deltas land).
type GoalRuntime struct {
	sessionKey  string
	agentID     string
	ownerUserID string

	store Store
	bus   *bus.MessageBus

	// continuationLock is a 1-permit semaphore — try-acquire so that
	// two near-simultaneous triggers collapse into one continuation
	// attempt instead of two racing. Implementation is a 1-buffered
	// channel: send=acquire, recv=release.
	continuationLock chan struct{}

	// accountingLock serializes "read goal → fold token delta → write
	// goal" so two AfterToolCall hooks on the same session can't
	// interleave and lose updates. Wired in step 3.
	accountingLock sync.Mutex

	// triggerCh wakes the Run loop. Buffered=1 so multiple events
	// while the loop is busy collapse to a single wake-up.
	triggerCh chan struct{}

	// done is closed by Stop() to signal Run() to exit.
	done chan struct{}

	// lastActivity is the wall-clock of the most recent event (trigger
	// or accounting). Read by the idle-shutdown check inside Run.
	// Protected by accountingLock.
	lastActivity time.Time
}

// NewGoalRuntime constructs a runtime bound to one session. Callers
// must Run() in a goroutine before calling Trigger(); GoalManager
// handles that lifecycle.
func NewGoalRuntime(sessionKey, agentID, ownerUserID string, st Store, mb *bus.MessageBus) *GoalRuntime {
	return &GoalRuntime{
		sessionKey:       sessionKey,
		agentID:          agentID,
		ownerUserID:      ownerUserID,
		store:            st,
		bus:              mb,
		continuationLock: make(chan struct{}, 1),
		triggerCh:        make(chan struct{}, 1),
		done:             make(chan struct{}),
		lastActivity:     time.Now(),
	}
}

// Trigger wakes the runtime to evaluate whether a continuation should
// fire. Non-blocking: if the runtime is already busy with a previous
// trigger, this one collapses into the existing wakeup. Safe from any
// goroutine.
func (gr *GoalRuntime) Trigger() {
	select {
	case gr.triggerCh <- struct{}{}:
	default:
		// Already a wakeup pending. The Run loop will see the latest
		// state when it gets there — no point queuing duplicates.
	}
}

// Stop signals the Run loop to exit. Idempotent; safe to call from a
// different goroutine than Run.
func (gr *GoalRuntime) Stop() {
	select {
	case <-gr.done:
		// already stopped
	default:
		close(gr.done)
	}
}

// Run is the main event loop. Blocks until Stop() or the runtime
// shuts itself down after runtimeIdleShutdown of inactivity. Designed
// to be called as a goroutine by GoalManager.
func (gr *GoalRuntime) Run(ctx context.Context) {
	slog.Info("goal runtime started", "session_key", gr.sessionKey, "agent_id", gr.agentID)
	defer slog.Info("goal runtime stopped", "session_key", gr.sessionKey, "agent_id", gr.agentID)

	probe := time.NewTicker(idleProbeInterval)
	defer probe.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-gr.done:
			return
		case <-gr.triggerCh:
			gr.maybeContinue(ctx)
		case <-probe.C:
			if gr.idleTooLong() {
				return
			}
			gr.maybeContinue(ctx)
		}
	}
}

// idleTooLong reports whether no trigger / accounting activity has
// landed in runtimeIdleShutdown. When true, Run exits and the manager
// will recreate a fresh runtime if a new event arrives.
func (gr *GoalRuntime) idleTooLong() bool {
	gr.accountingLock.Lock()
	defer gr.accountingLock.Unlock()
	return time.Since(gr.lastActivity) > runtimeIdleShutdown
}

// markActivity bumps the idle-shutdown timer. Called by Trigger /
// accounting paths. Held briefly under accountingLock — that's the
// same lock token-delta folding will use, so the activity timestamp
// and the accounting baseline always advance together.
func (gr *GoalRuntime) markActivity() {
	gr.accountingLock.Lock()
	gr.lastActivity = time.Now()
	gr.accountingLock.Unlock()
}

// maybeContinue is the continuation entry point. Reads the current
// goal, runs the gate cascade, and (on success) publishes a
// continuation prompt onto the bus. The gates mirror Codex's
// goal_continuation_candidate_if_active:
//
//   - continuationLock: try-acquire; collapse concurrent triggers
//   - goal exists for this session
//   - goal status is Active (BudgetLimited / Complete / Paused all
//     no-op here; budget_limit publishes happen on the transition
//     edge in the token-accounting hook, not on every later trigger)
//   - goal has routing info recorded (legacy rows from before the
//     routing migration would otherwise publish a malformed inbound)
//
// Past those gates the prompt is rendered fresh each iteration —
// that's deliberate: tokens_used / time_used_seconds have moved on,
// so the budget snapshot the model sees is always current.
//
// Errors land at warn level rather than blocking the run loop;
// continuation is best-effort and a transient store glitch just
// means the next trigger will retry.
func (gr *GoalRuntime) maybeContinue(ctx context.Context) {
	// Try-acquire the continuation lock. Two near-simultaneous
	// triggers collapse into one publish instead of racing onto the
	// bus and stacking two duplicate prompts.
	select {
	case gr.continuationLock <- struct{}{}:
		defer func() { <-gr.continuationLock }()
	default:
		return
	}
	// NB: markActivity is deliberately NOT called here. Calling it
	// unconditionally meant every 30s probe — even on a goal that
	// had long since gone Complete / Paused / Cleared — refreshed
	// lastActivity and defeated the runtimeIdleShutdown backstop,
	// so terminal-state runtimes leaked forever. Activity is now
	// only marked at the real-work sites below, so a runtime whose
	// goal no longer needs continuation will idle-shut down after
	// runtimeIdleShutdown.

	g, err := gr.store.GetGoalBySession(ctx, gr.agentID, gr.sessionKey)
	if errors.Is(err, ErrNotFound) {
		return
	}
	if err != nil {
		slog.Warn("goal runtime: load goal failed",
			"agent_id", gr.agentID, "session_key", gr.sessionKey, "error", err)
		return
	}
	if g.Status != StatusActive {
		return
	}
	if g.Channel == "" && g.ChatID == "" {
		// Legacy row from before the routing migration backfilled
		// these fields. Without routing info the publish below would
		// emit a message no channel adapter knows how to route.
		slog.Warn("goal runtime: skipping continuation — goal has no routing info",
			"agent_id", gr.agentID, "session_key", gr.sessionKey, "goal_id", g.ID)
		return
	}

	// Safety cap: stop a runaway goal that consumes near-zero
	// tokens per turn (so the budget gate never triggers) but
	// somehow keeps the loop spinning. Without a budget set the
	// model can in principle loop forever on "let me audit once
	// more"; SafetyMaxIterations is the design's documented
	// hard backstop. Trip it the same way budget-exhausted goals
	// exit — flip to BudgetLimited and publish the wrap-up prompt
	// so the model gets one final turn to summarize honestly.
	if g.SafetyMaxIterations > 0 && g.Iterations >= g.SafetyMaxIterations {
		slog.Warn("goal runtime: safety iteration cap reached",
			"agent_id", gr.agentID, "session_key", gr.sessionKey,
			"iterations", g.Iterations, "cap", g.SafetyMaxIterations)
		g.Status = StatusBudgetLimited
		if err := gr.store.UpdateGoal(ctx, g); err != nil {
			slog.Warn("goal runtime: failed to persist safety-cap transition",
				"agent_id", gr.agentID, "session_key", gr.sessionKey, "error", err)
			return
		}
		gr.markActivity()
		if !PublishBudgetLimit(gr.bus, g, BudgetLimitPrompt(g)) {
			slog.Warn("goal runtime: bus full, dropped safety-cap wrap-up",
				"agent_id", gr.agentID, "session_key", gr.sessionKey)
		}
		return
	}

	prompt := ContinuationPrompt(g)
	gr.markActivity()
	if !PublishContinuation(gr.bus, g, prompt) {
		slog.Warn("goal runtime: bus full, dropped continuation",
			"agent_id", gr.agentID, "session_key", gr.sessionKey)
	}
}

// GoalManager owns the per-session GoalRuntime goroutines. One
// manager per Agent; sessions come and go but the manager is the
// stable handle the agent loop calls into.
//
// Lifecycle:
//   - Ensure(sessionKey, ...) is called whenever a session is touched
//     (goal-touching path); idempotent.
//   - StopSession(sessionKey) is called when a goal is cleared or the
//     session is being deleted.
//   - Shutdown() stops every runtime; called when the agent unloads.
type GoalManager struct {
	store Store
	bus   *bus.MessageBus

	mu       sync.Mutex
	runtimes map[string]*GoalRuntime
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewGoalManager constructs an inactive manager. Call Start() before
// using; that wires up the shared context so Shutdown() can cancel
// every runtime at once.
func NewGoalManager(st Store, mb *bus.MessageBus) *GoalManager {
	return &GoalManager{
		store:    st,
		bus:      mb,
		runtimes: make(map[string]*GoalRuntime),
	}
}

// Start prepares the manager for use. Safe to call once; subsequent
// calls reset the shared context and are equivalent to Shutdown +
// Start (mostly useful in tests).
func (m *GoalManager) Start(parent context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
	m.ctx, m.cancel = context.WithCancel(parent)
}

// Ensure returns the runtime for sessionKey, creating + starting it
// if it doesn't exist yet. Returns nil if the manager hasn't been
// Start()'d. Safe to call repeatedly.
func (m *GoalManager) Ensure(sessionKey, agentID, ownerUserID string) *GoalRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return nil
	}
	if gr, ok := m.runtimes[sessionKey]; ok {
		return gr
	}
	gr := NewGoalRuntime(sessionKey, agentID, ownerUserID, m.store, m.bus)
	m.runtimes[sessionKey] = gr
	// Capture m.ctx under the lock; if we read it from the goroutine
	// after the lock is released, a racing Shutdown could clear it
	// out from under us and the runtime would call Run(nil), which
	// panics on <-ctx.Done(). Pass the captured ctx by value instead.
	ctx := m.ctx
	go func() {
		gr.Run(ctx)
		// Self-removal once Run exits, so a future Ensure() spins a
		// fresh runtime instead of returning a corpse.
		m.mu.Lock()
		if current, ok := m.runtimes[sessionKey]; ok && current == gr {
			delete(m.runtimes, sessionKey)
		}
		m.mu.Unlock()
	}()
	return gr
}

// Get returns the existing runtime for sessionKey, or nil if none.
// Doesn't create. Used by trigger sites that want to no-op when no
// goal is active.
func (m *GoalManager) Get(sessionKey string) *GoalRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runtimes[sessionKey]
}

// StopSession terminates the runtime for one session (e.g. /goal
// clear ran). Idempotent.
func (m *GoalManager) StopSession(sessionKey string) {
	m.mu.Lock()
	gr := m.runtimes[sessionKey]
	delete(m.runtimes, sessionKey)
	m.mu.Unlock()
	if gr != nil {
		gr.Stop()
	}
}

// Shutdown stops every runtime. Called when the agent unloads. Safe
// to call multiple times; second call is a no-op.
func (m *GoalManager) Shutdown() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.ctx = nil
	runtimes := m.runtimes
	m.runtimes = make(map[string]*GoalRuntime)
	m.mu.Unlock()
	for _, gr := range runtimes {
		gr.Stop()
	}
	if cancel != nil {
		cancel()
	}
}

// ActiveCount returns the number of running runtimes. Exposed for
// metrics / tests / debug; not load-bearing.
func (m *GoalManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runtimes)
}
