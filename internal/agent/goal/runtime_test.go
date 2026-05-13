package goal

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// fakeStore is a no-op Store stand-in for runtime tests. Step 1
// doesn't exercise any Store call paths (maybeContinue is stubbed),
// but the constructors require something non-nil.
type fakeStore struct{}

func (fakeStore) CreateGoal(context.Context, *Goal) error { return nil }
func (fakeStore) GetGoalBySession(context.Context, string, string) (*Goal, error) {
	return nil, ErrNotFound
}
func (fakeStore) GetGoalByID(context.Context, string) (*Goal, error) { return nil, ErrNotFound }
func (fakeStore) UpdateGoal(context.Context, *Goal) error            { return nil }
func (fakeStore) UpdateObjective(context.Context, string, string) error {
	return nil
}
func (fakeStore) DeleteGoal(context.Context, string) error { return nil }
func (fakeStore) ListGoalsByOwner(context.Context, string, int) ([]*Goal, error) {
	return nil, nil
}

func newTestManager(t *testing.T) *GoalManager {
	t.Helper()
	m := NewGoalManager(fakeStore{}, bus.New())
	m.Start(context.Background())
	t.Cleanup(m.Shutdown)
	return m
}

// TestTriggerIsNonBlocking pins the contract that Trigger() never
// blocks even when the loop is busy. If Trigger ever started
// blocking, every PostTurn hook would have to spawn a goroutine to
// fire it — which would in turn make ordering unpredictable.
func TestTriggerIsNonBlocking(t *testing.T) {
	gr := NewGoalRuntime("s-1", "agent", "user", fakeStore{}, bus.New())
	// No Run() goroutine — the channel buffer is the safety net.
	for i := 0; i < 100; i++ {
		done := make(chan struct{})
		go func() {
			gr.Trigger()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(50 * time.Millisecond):
			t.Fatalf("Trigger blocked on call %d", i)
		}
	}
}

// TestStopExitsRunLoop verifies Run returns after Stop is called.
// Without this, GoalManager.StopSession + Shutdown would leak a
// goroutine per cleared session.
func TestStopExitsRunLoop(t *testing.T) {
	gr := NewGoalRuntime("s-1", "agent", "user", fakeStore{}, bus.New())
	done := make(chan struct{})
	go func() {
		gr.Run(context.Background())
		close(done)
	}()
	gr.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after Stop")
	}
}

// TestStopIsIdempotent guards against double-close panics on the
// done channel — StopSession and Shutdown can both target the same
// runtime if a goal is cleared while the agent is unloading.
func TestStopIsIdempotent(t *testing.T) {
	gr := NewGoalRuntime("s-1", "agent", "user", fakeStore{}, bus.New())
	gr.Stop()
	gr.Stop() // must not panic
	gr.Stop()
}

// TestCtxCancelExitsRunLoop confirms the manager's shared context
// kills its runtimes — used by Shutdown for "tear down everything".
func TestCtxCancelExitsRunLoop(t *testing.T) {
	gr := NewGoalRuntime("s-1", "agent", "user", fakeStore{}, bus.New())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		gr.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after ctx cancel")
	}
}

// TestManagerEnsureIsIdempotent: calling Ensure repeatedly for the
// same session_key must return the SAME runtime, not start a fresh
// one each time. Otherwise PostTurn hooks would create a runtime
// per turn and we'd have N goroutines per session.
func TestManagerEnsureIsIdempotent(t *testing.T) {
	m := newTestManager(t)
	gr1 := m.Ensure("s-1", "agent", "user")
	gr2 := m.Ensure("s-1", "agent", "user")
	if gr1 != gr2 {
		t.Fatal("Ensure returned different runtimes for the same session_key")
	}
	if m.ActiveCount() != 1 {
		t.Errorf("ActiveCount = %d, want 1", m.ActiveCount())
	}
}

// TestManagerEnsureReturnsNilBeforeStart: calling Ensure on a manager
// that was never Start()'d should return nil rather than silently
// spinning an orphan goroutine no one can stop. Defends against init
// ordering bugs.
func TestManagerEnsureReturnsNilBeforeStart(t *testing.T) {
	m := NewGoalManager(fakeStore{}, bus.New())
	if gr := m.Ensure("s-1", "agent", "user"); gr != nil {
		t.Fatal("Ensure returned non-nil before Start()")
	}
}

// TestManagerStopSessionRemovesRuntime: /goal clear should remove the
// session's runtime entirely. A second Ensure after StopSession must
// produce a fresh runtime (different pointer).
func TestManagerStopSessionRemovesRuntime(t *testing.T) {
	m := newTestManager(t)
	gr1 := m.Ensure("s-1", "agent", "user")
	m.StopSession("s-1")
	// Wait for Run goroutine to actually exit + self-deregister.
	waitFor(t, time.Second, func() bool { return m.ActiveCount() == 0 })

	gr2 := m.Ensure("s-1", "agent", "user")
	if gr2 == nil || gr2 == gr1 {
		t.Fatalf("second Ensure should return a fresh runtime, got gr2=%p gr1=%p", gr2, gr1)
	}
}

// TestManagerShutdownStopsAll: Shutdown must stop every runtime, and
// the Run goroutines must actually exit. Without this, agent unload
// would leak runtimes per session ever touched.
func TestManagerShutdownStopsAll(t *testing.T) {
	m := NewGoalManager(fakeStore{}, bus.New())
	m.Start(context.Background())

	m.Ensure("s-1", "agent", "user")
	m.Ensure("s-2", "agent", "user")
	m.Ensure("s-3", "agent", "user")
	if got := m.ActiveCount(); got != 3 {
		t.Fatalf("ActiveCount = %d, want 3", got)
	}

	m.Shutdown()
	waitFor(t, time.Second, func() bool { return m.ActiveCount() == 0 })
}

// Fresh runtime must not be idle — guards NewGoalRuntime seeding
// lastActivity to "now" rather than zero time.
func TestIdleTooLongFreshRuntimeIsNotIdle(t *testing.T) {
	gr := NewGoalRuntime("s-1", "agent", "user", fakeStore{}, bus.New())
	if gr.idleTooLong() {
		t.Error("fresh runtime should not be idleTooLong")
	}
}

// Stale runtime → idleTooLong true → Run loop exits. Paired with
// the maybeContinue "no-op probes don't refresh lastActivity"
// tests, this closes the runtime-leak story.
func TestIdleTooLongAfterStaleness(t *testing.T) {
	gr := NewGoalRuntime("s-1", "agent", "user", fakeStore{}, bus.New())
	gr.lastActivity = time.Now().Add(-2 * runtimeIdleShutdown)
	if !gr.idleTooLong() {
		t.Errorf("idleTooLong should be true after %v of staleness", 2*runtimeIdleShutdown)
	}
}

// `>` is strict — exactly-at-the-window stays alive one more
// tick. Pinned so > → >= can't slip in silently.
func TestIdleTooLongBoundary(t *testing.T) {
	gr := NewGoalRuntime("s-1", "agent", "user", fakeStore{}, bus.New())
	gr.lastActivity = time.Now().Add(-runtimeIdleShutdown + time.Millisecond)
	if gr.idleTooLong() {
		t.Error("idleTooLong should be false at the inside edge of the window")
	}
}

// TestTriggerConcurrentSafe is a smoke test that the Trigger ↔ Stop
// ↔ Run plumbing has no races under -race. The values don't matter;
// the test is for the race detector.
func TestTriggerConcurrentSafe(t *testing.T) {
	m := newTestManager(t)
	gr := m.Ensure("s-1", "agent", "user")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				gr.Trigger()
			}
		}()
	}
	wg.Wait()
}

// waitFor polls cond until it returns true or the timeout elapses.
// Used for "the goroutine should exit soon" assertions that don't
// have a direct done channel to wait on.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}
