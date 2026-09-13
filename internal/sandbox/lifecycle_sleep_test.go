package sandbox

// Idle handling for long operations. Two failure modes motivated this:
//
//  1. lastUsed is stamped when an operation STARTS, so an exec longer than
//     idleTTL looked idle and its sandbox was destroyed underneath it — the
//     crypto batch's 600s budget against a 10-minute TTL sat right on that line.
//  2. Destroying an idle sandbox is the wrong default now that e2b can pause
//     one: a paused instance keeps its filesystem and memory, is not billed and
//     does not count toward the concurrency limit, so the next caller resumes it
//     instead of paying for a replacement.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sleepablePool is an ExecutorPool that can also sleep scopes, recording what
// each path was asked to do.
type sleepablePool struct {
	fakePool
	sleeps   int32
	sleepErr error
	// paused=false with no error models "nothing to sleep".
	nothingToSleep bool
}

func (p *sleepablePool) SleepScope(context.Context, string, string, string) (bool, error) {
	atomic.AddInt32(&p.sleeps, 1)
	if p.sleepErr != nil {
		return false, p.sleepErr
	}
	if p.nothingToSleep {
		return false, nil
	}
	return true, nil
}

func (p *sleepablePool) sleepCount() int { return int(atomic.LoadInt32(&p.sleeps)) }

// blockingExecutor holds Exec open until released, so the sweeper gets a chance
// to act while an operation is in flight.
type blockingExecutor struct {
	fakeExecutor
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *blockingExecutor) Exec(context.Context, string, time.Duration) (string, error) {
	e.once.Do(func() { close(e.entered) })
	<-e.release
	return "done", nil
}

func newBlockingPool() (*blockingPool, *blockingExecutor) {
	be := &blockingExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	return &blockingPool{be: be}, be
}

// blockingPool hands back the same blocking executor for any scope.
type blockingPool struct {
	be *blockingExecutor
}

func (p *blockingPool) Get(context.Context, string, string, string) (Executor, error) {
	return p.be, nil
}
func (p *blockingPool) Release(string, string, string) error { return nil }
func (p *blockingPool) CloseAll()                            {}
func (p *blockingPool) Backend() string                      { return "blocking" }

// The defect this guards: an operation outliving idleTTL must not have its
// sandbox reclaimed under it.
func TestLifecycleDoesNotEvictWhileAnOperationRuns(t *testing.T) {
	inner, be := newBlockingPool()
	lp := NewLifecyclePool(inner, 40*time.Millisecond, 10*time.Millisecond)
	lp.Start()
	defer lp.CloseAll()

	ex, err := lp.Get(context.Background(), "agt", "", "sess")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ex.Exec(context.Background(), "sleep 1", time.Second)
		done <- err
	}()
	<-be.entered

	// Well past idleTTL, with the sweeper ticking every 10ms.
	time.Sleep(150 * time.Millisecond)
	lp.mu.Lock()
	inFlight := lp.inUse[poolKey("agt", "", "sess")]
	_, tracked := lp.lastUsed[poolKey("agt", "", "sess")]
	lp.mu.Unlock()
	if inFlight == 0 || !tracked {
		t.Fatal("a running operation must keep its scope marked in use and tracked")
	}

	close(be.release)
	if err := <-done; err != nil {
		t.Fatalf("exec: %v", err)
	}
}

// Idle now means sleep, not destroy.
func TestLifecycleIdleSleepsInsteadOfReleasing(t *testing.T) {
	inner := &sleepablePool{fakePool: *newFakePool()}
	lp := NewLifecyclePool(inner, 30*time.Millisecond, 10*time.Millisecond)
	lp.longOpRenew = time.Hour // the trailing lease renew is covered elsewhere
	lp.Start()
	defer lp.CloseAll()

	ex, err := lp.Get(context.Background(), "erin", "", "s")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Get only hands back a lazy executor; the scope is registered on first use.
	if _, err := ex.Exec(context.Background(), "true", time.Second); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for inner.sleepCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if inner.sleepCount() == 0 {
		t.Fatal("an idle scope must be put to sleep")
	}
	if got := atomic.LoadInt32(&inner.fakePool.releases); got != 0 {
		t.Fatalf("releases = %d, want 0: a sleepable sandbox must not be destroyed", got)
	}
}

// A sandbox we could not sleep must survive: destroying it would be worse than
// leaving it running.
func TestLifecycleKeepsSandboxItCouldNotSleep(t *testing.T) {
	inner := &sleepablePool{fakePool: *newFakePool(), sleepErr: errors.New("pause API is unavailable")}
	lp := NewLifecyclePool(inner, 30*time.Millisecond, 10*time.Millisecond)
	lp.longOpRenew = time.Hour
	lp.Start()
	defer lp.CloseAll()

	ex, err := lp.Get(context.Background(), "erin", "", "s")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Get only hands back a lazy executor; the scope is registered on first use.
	if _, err := ex.Exec(context.Background(), "true", time.Second); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if inner.sleepCount() == 0 {
		t.Fatal("the sweeper should have attempted a sleep")
	}
	if got := atomic.LoadInt32(&inner.fakePool.releases); got != 0 {
		t.Fatalf("releases = %d, want 0: a failed sleep must not become a destroy", got)
	}
}

// Nothing to sleep (the sleeper already cleaned up, or no executor is cached)
// falls through to the normal release path.
func TestLifecycleFallsBackToReleaseWhenThereIsNothingToSleep(t *testing.T) {
	inner := &sleepablePool{fakePool: *newFakePool(), nothingToSleep: true}
	lp := NewLifecyclePool(inner, 30*time.Millisecond, 10*time.Millisecond)
	lp.longOpRenew = time.Hour
	lp.Start()
	defer lp.CloseAll()

	ex, err := lp.Get(context.Background(), "erin", "", "s")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Get only hands back a lazy executor; the scope is registered on first use.
	if _, err := ex.Exec(context.Background(), "true", time.Second); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&inner.fakePool.releases) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&inner.fakePool.releases); got != 1 {
		t.Fatalf("releases = %d, want 1 when there was nothing to sleep", got)
	}
}

func TestSleepOrReleaseDecisionTable(t *testing.T) {
	sc := sandboxScope{agentID: "a", sessionID: "s"}
	cases := []struct {
		name     string
		pool     *sleepablePool
		wantStop bool
	}{
		{"slept", &sleepablePool{}, true},
		{"nothing to sleep", &sleepablePool{nothingToSleep: true}, false},
		{"sleep failed", &sleepablePool{sleepErr: fmt.Errorf("transient")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lp := NewLifecyclePool(&fakePool{live: map[string]*fakeExecutor{}}, time.Minute, time.Minute)
			if got := lp.sleepOrRelease(tc.pool, sc); got != tc.wantStop {
				t.Fatalf("sleepOrRelease = %v, want %v", got, tc.wantStop)
			}
		})
	}
}
