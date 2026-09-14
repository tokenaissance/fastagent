package taskqueue

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// Hot reload must reach the next task and leave running ones alone: an
// operator changing the budget expects the next turn to use it, and a turn
// that already started must not have its budget rewritten mid-flight.
func TestSetDefaultTimeoutAppliesToTheNextTask(t *testing.T) {
	seen := make(chan time.Duration, 2)
	q := NewQueue(2, 10*time.Second, func(ctx context.Context, _ *Task) (string, error) {
		deadline, _ := ctx.Deadline()
		seen <- time.Until(deadline)
		return "", nil
	})
	defer q.Stop()

	q.SetDefaultTimeout(45 * time.Second)
	if q.DefaultTimeout() != 45*time.Second {
		t.Fatalf("DefaultTimeout = %s; want 45s", q.DefaultTimeout())
	}
	q.Submit("agt", "web::c1", bus.InboundMessage{Channel: "web", ChatID: "c1"}, "")
	if got := <-seen; got < 40*time.Second {
		t.Fatalf("task budget = %s; want the reloaded ~45s", got)
	}

	// A per-task budget still wins over the reloaded default.
	q.SubmitWithTimeout("agt", "web::c2", bus.InboundMessage{Channel: "web", ChatID: "c2"}, "", 3*time.Second)
	if got := <-seen; got > 10*time.Second {
		t.Fatalf("per-task budget = %s; want ~3s even after a default reload", got)
	}
}

// Resizing the concurrency limit while a task holds a slot must not strand it
// (the holder releases into the semaphore it acquired from) and the new limit
// must be in force for later admissions.
func TestSetMaxConcurrentResizesWithoutStrandingInFlight(t *testing.T) {
	release := make(chan struct{})
	var running, maxRunning atomic.Int64
	handler := func(ctx context.Context, _ *Task) (string, error) {
		n := running.Add(1)
		for {
			cur := maxRunning.Load()
			if n <= cur || maxRunning.CompareAndSwap(cur, n) {
				break
			}
		}
		defer running.Add(-1)
		<-release
		return "", nil
	}
	q := NewQueue(1, 10*time.Second, handler)
	defer q.Stop()

	// One task holds the only slot of the ORIGINAL semaphore.
	q.Submit("agt", "web::a", bus.InboundMessage{Channel: "web", ChatID: "a"}, "")
	waitFor(t, "first task running", func() bool { return running.Load() == 1 })

	// Resize to 3 while it runs, then admit three more: without the local
	// semaphore capture the holder would release into the new channel and the
	// accounting would drift.
	q.SetMaxConcurrent(3)
	for _, chat := range []string{"b", "c", "d"} {
		q.Submit("agt", "web::"+chat, bus.InboundMessage{Channel: "web", ChatID: chat}, "")
	}
	waitFor(t, "resized limit admits three more", func() bool { return running.Load() == 4 })

	close(release)
	waitFor(t, "all tasks finished", func() bool { return running.Load() == 0 })
	if got := maxRunning.Load(); got != 4 {
		t.Fatalf("max concurrent = %d; want 4 (1 original slot + 3 after resize)", got)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
