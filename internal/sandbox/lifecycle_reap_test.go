package sandbox

// The lifecycle layer owns two clocks: the idle sweep (fast, per scope) and the
// orphan reap (slow, fleet-wide). These tests pin that the second one is
// actually driven — a reaper that is implemented but never called is the same
// as no reaper at all, and the bug would only show up as a slow leak in prod.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// reapablePool is a fakePool that can also reap, signalling the first pass.
type reapablePool struct {
	fakePool
	passes int32
	err    error
	first  chan struct{}
	once   sync.Once
}

func (p *reapablePool) ReapOrphans(context.Context) (int, error) {
	atomic.AddInt32(&p.passes, 1)
	if p.first != nil {
		p.once.Do(func() { close(p.first) })
	}
	return 0, p.err
}

func (p *reapablePool) passCount() int { return int(atomic.LoadInt32(&p.passes)) }

func waitForReapPass(t *testing.T, inner *reapablePool) {
	t.Helper()
	select {
	case <-inner.first:
	case <-time.After(3 * time.Second):
		t.Fatal("the reaper never ran: the sweep loop does not drive it")
	}
}

func TestLifecyclePoolReapsOrphansOnItsOwnClock(t *testing.T) {
	inner := &reapablePool{first: make(chan struct{})}
	// idleTTL far longer than the test: the reap must be its own clock, not a
	// side effect of eviction.
	p := NewLifecyclePool(inner, time.Hour, 5*time.Millisecond)
	p.reapEvery = 5 * time.Millisecond
	p.Start()
	t.Cleanup(p.CloseAll)

	waitForReapPass(t, inner)
}

// A deployment that keeps its sandboxes alive (idleTTL=0) still wants its
// crashed pods' sandboxes collected: the loop must start for the reaper alone.
func TestLifecyclePoolReapsEvenWhenIdleEvictionIsOff(t *testing.T) {
	inner := &reapablePool{first: make(chan struct{})}
	p := NewLifecyclePool(inner, 0, 5*time.Millisecond)
	p.reapEvery = 5 * time.Millisecond
	p.Start()
	t.Cleanup(p.CloseAll)

	waitForReapPass(t, inner)
	if got := atomic.LoadInt32(&inner.releases); got != 0 {
		t.Fatalf("releases = %d, want 0: the sweep is off, only the reap runs", got)
	}
}

// A pool that cannot reap (docker) keeps the old "nothing to do" fast path
// instead of spinning a goroutine forever.
func TestLifecyclePoolWithoutAReaperStillShortCircuits(t *testing.T) {
	inner := &fakePool{}
	p := NewLifecyclePool(inner, 0, 5*time.Millisecond)
	p.Start()
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not finish a loop-less pool")
	}
}

// A failing pass must not stop the loop: the next tick retries.
func TestLifecyclePoolKeepsReapingAfterAFailedPass(t *testing.T) {
	inner := &reapablePool{first: make(chan struct{}), err: errors.New("provider down")}
	p := NewLifecyclePool(inner, time.Hour, 5*time.Millisecond)
	p.reapEvery = 5 * time.Millisecond
	p.Start()
	t.Cleanup(p.CloseAll)

	waitForReapPass(t, inner)
	deadline := time.After(3 * time.Second)
	for inner.passCount() < 2 {
		select {
		case <-deadline:
			t.Fatalf("reap passes = %d, want the loop to keep retrying", inner.passCount())
		case <-time.After(2 * time.Millisecond):
		}
	}
}
