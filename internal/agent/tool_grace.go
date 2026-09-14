package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
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

// withTurnDeadline hands the turn's clock across the grace boundary below. The
// value itself lives in the tools package (tools.WithTurnDeadline) because the
// tools are the ones that have to read it: a serialized tool waiting for its
// slot must stop waiting when the turn ends, and a tool that sizes its own work
// (delegate_task) must know how much of the turn is left. This context has no
// deadline of its own — that is the point of the grace — so reading
// ctx.Deadline() inside a tool answers "no ceiling" for every call in
// production, which is how the first version of the sub-agent clamp ended up as
// dead code with green tests.
func withTurnDeadline(toolCtx, turnCtx context.Context) context.Context {
	if deadline, ok := turnCtx.Deadline(); ok {
		return tools.WithTurnDeadline(toolCtx, deadline)
	}
	return toolCtx
}

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
		// Say it out loud: downstream this cancellation surfaces as a bare
		// "context canceled" (e.g. an exec stream cut mid-read, reported as
		// "e2b exec body read: context canceled"), which looks identical to a
		// sandbox or provider fault. Naming the cause and the window here is
		// what separates "the turn's budget ran out" from "the sandbox died".
		slog.Warn("turn ctx ended with a tool in flight; letting it finish within the grace window",
			"cause", ctx.Err(), "grace", grace.String())
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-timer.C:
			slog.Warn("tool still running after its grace window; cancelling it",
				"grace", grace.String())
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
