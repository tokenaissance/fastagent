package tools

import (
	"context"
	"time"
)

// The turn clock, for the tools that need it.
//
// A tool's context is prepared by the agent loop to OUTLIVE its turn: after the
// turn's context is cancelled the tool gets a grace window to land its result
// (P5). That is why the context has no deadline of its own — and why the turn's
// deadline has to travel as a value. Reading ctx.Deadline() inside a tool answers
// "no ceiling" for every call in production; the first version of the sub-agent
// budget clamp did exactly that and was dead code with green tests.
//
// Two tools need this, for two different reasons:
//
//   - a serialized tool waiting for its slot must stop waiting when the TURN
//     ends, not when the grace window does: a call that has not started has no
//     result to land, so the grace protects nothing (delegate_task, 2026-09-14,
//     waiting behind another session's sub-agent);
//   - a tool that sizes its own work inside the turn must know how much is left
//     (delegate_task's sub-agent wall budget).
type turnDeadlineKey struct{}

// WithTurnDeadline stamps a tool context with the deadline of the turn that
// started it. The loop is the only caller.
func WithTurnDeadline(ctx context.Context, deadline time.Time) context.Context {
	return context.WithValue(ctx, turnDeadlineKey{}, deadline)
}

// TurnDeadline is the deadline stamped above, if the caller had one.
func TurnDeadline(ctx context.Context) (time.Time, bool) {
	deadline, ok := ctx.Value(turnDeadlineKey{}).(time.Time)
	return deadline, ok
}

// TurnRemaining is how much of the turn is left. The stamped value first (the
// production path), then the context's own deadline — a caller that drives a
// tool directly (CLI, cron, tests) has no grace boundary in between. False means
// there is no turn clock at all, which is not the same as zero.
func TurnRemaining(ctx context.Context) (time.Duration, bool) {
	if deadline, ok := TurnDeadline(ctx); ok {
		return time.Until(deadline), true
	}
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline), true
	}
	return 0, false
}
