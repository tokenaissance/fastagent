package taskqueue

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// A per-task budget must win over the queue default in both directions: a
// source with long work (cron) may outlive the default without wedging the
// default for everyone else, and a source that asks for less is cut sooner.
//
// This is the mechanism behind P5's per-source budgets
// (docs/session-turn-integrity.md): the policy lives in the gateway, the queue
// only honours the number it is handed.
func TestSubmitWithTimeoutOverridesQueueDefault(t *testing.T) {
	type outcome struct {
		err    error
		waited time.Duration
	}
	results := make(chan outcome, 2)

	q := NewQueue(4, 150*time.Millisecond, func(ctx context.Context, task *Task) (string, error) {
		start := time.Now()
		<-ctx.Done() // every task runs until its own budget ends
		results <- outcome{err: ctx.Err(), waited: time.Since(start)}
		return "", ctx.Err()
	})
	defer q.Stop()

	// Default budget: killed at ~150ms.
	q.Submit("agt", "web::c1", bus.InboundMessage{Channel: "web", ChatID: "c1", Text: "chat"}, "")
	// Cron budget: 2s, i.e. must NOT be killed by the 150ms default.
	q.SubmitWithTimeout("agt", "web::c2", bus.InboundMessage{Channel: "web", ChatID: "c2", Text: "tick", Source: bus.SourceCron}, "", 2*time.Second)

	first := <-results
	if first.err == nil {
		t.Fatal("task without an override should have hit the queue default")
	}
	if first.waited < 100*time.Millisecond || first.waited > time.Second {
		t.Fatalf("default-budget task ran %s; want ~150ms", first.waited)
	}

	select {
	case second := <-results:
		t.Fatalf("cron-budget task ended after %s; it must outlive the 150ms default", second.waited)
	case <-time.After(400 * time.Millisecond):
		// Still running past the default budget: the override is in force.
	}
	q.Stop()
}
