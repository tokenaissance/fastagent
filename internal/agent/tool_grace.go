package agent

import (
	"context"
	"sync"
	"time"
)

// toolGraceDefault bounds how long an in-flight tool may keep running after
// its turn's context is cancelled — a turn budget expiring (task-queue
// timeout, IM/web deadline) or a client Stop.
//
// Without a grace the cancellation reaches the tool immediately, the round
// records nothing and the turn ends with an unanswered tool_use that only
// the prompt projection can fill with a synthetic "stopped" reply. A grace
// lets the tool finish and the history record its actual answer instead —
// the difference between "the model sees what the tool returned" and "the
// model sees that it was interrupted".
const toolGraceDefault = 60 * time.Second

// toolGraceContext returns a context for one round of tool execution that
// keeps running for up to grace after the turn's context is cancelled. The
// returned stop func MUST be called when the round ends.
//
// The turn itself is not extended: the loop's own ctx stays cancelled, so no
// further model round starts — only the work already in flight is allowed to
// land.
func toolGraceContext(ctx context.Context, grace time.Duration) (context.Context, context.CancelFunc) {
	if grace <= 0 {
		return ctx, func() {}
	}
	toolCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	var once sync.Once
	stop := func() { once.Do(cancel) }
	if ctx.Done() == nil {
		// The turn can never be cancelled, so there is nothing to outlive:
		// the caller's stop is the only way this context ends.
		return toolCtx, stop
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-toolCtx.Done():
			return
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancel()
		case <-toolCtx.Done():
		}
	}()
	return toolCtx, stop
}

// graceWindow is the agent's effective tool grace: the configured value, or
// the default when unset (tests pin it small; production keeps 60s).
func (a *Agent) graceWindow() time.Duration {
	if a != nil && a.toolGrace > 0 {
		return a.toolGrace
	}
	return toolGraceDefault
}
