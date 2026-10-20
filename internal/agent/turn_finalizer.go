package agent

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// turnOutcome names the two terminal results the finalizer accepts. A turn
// that the user stopped, or that a peer superseded, has no outcome: it must
// not finalize, because a PostTurn continuation would fight that decision.
type turnOutcome int

const (
	turnOutcomeSucceeded turnOutcome = iota
	turnOutcomeFailed
)

// turnFinalizeInput carries every value the PostTurn side effects need. The
// struct is the contract: a call site can not finalize a turn while omitting
// the message history, the tool-call count, the chatter memory, or the
// success/failure outcome.
type turnFinalizeInput struct {
	msg           bus.InboundMessage
	messages      []provider.Message
	toolCallCount int
	chatterMemory *Memory
	outcome       turnOutcome
}

// turnFinalizer owns the PostTurn side effects of exactly one admitted turn.
//
// Invariants:
//
//	F1  At most one finalize call per turn has an effect. sync.Once enforces it.
//	F2  Every path that produces a result calls Finalize exactly once:
//	    reply, streaming fallback, iteration-cap delivery, and failure.
//	F3  Decision paths do not call Finalize. A user stop or a superseded lease
//	    ends the turn without PostTurn hooks.
//	F4  A failed outcome runs the hooks on recordCtx with
//	    postTurnAfterFailureBudget, so an expired turn deadline does not skip
//	    token accounting or the goal continuation.
//	F5  The finalizer does not write the session. Callers append while the
//	    session lease is held, before they finalize; the streaming path hands
//	    the release to the goroutine that persists the reply.
//
// F1 and F2 make streaming and non-streaming terminal behavior identical for
// the same input; F3 keeps the two deliberate no-hook ends out of that rule.
//
// F4 carries the production rationale for failed turns: the goal continuation
// is a PostTurn hook, so a failed turn that skipped PostTurn stopped the goal
// chain until the user typed "continue" (measured 2026-09-28: an 86-minute
// stall after a 300 s budget cut, and the same break after a provider 402).
type turnFinalizer struct {
	agent *Agent
	once  sync.Once
	ran   atomic.Bool
}

func newTurnFinalizer(a *Agent) *turnFinalizer {
	return &turnFinalizer{agent: a}
}

// Finalize runs the PostTurn side effects for this turn. It returns true only
// for the call that ran them.
func (f *turnFinalizer) Finalize(ctx context.Context, in turnFinalizeInput) bool {
	executed := false
	f.once.Do(func() {
		executed = true
		f.ran.Store(true)
		if in.outcome == turnOutcomeFailed {
			hookCtx, cancel := recordCtx(ctx, postTurnAfterFailureBudget)
			defer cancel()
			f.agent.runPostTurn(hookCtx, in.msg, in.messages, in.toolCallCount, in.chatterMemory)
			return
		}
		f.agent.runPostTurn(ctx, in.msg, in.messages, in.toolCallCount, in.chatterMemory)
	})
	return executed
}

// Finalized reports whether the turn's PostTurn side effects have run.
func (f *turnFinalizer) Finalized() bool {
	return f.ran.Load()
}

// finalizeSucceeded is the shorthand for result paths that produced a reply.
func (f *turnFinalizer) finalizeSucceeded(ctx context.Context, in turnFinalizeInput) bool {
	in.outcome = turnOutcomeSucceeded
	return f.Finalize(ctx, in)
}

// finalizeFailed is the shorthand for result paths that produced a failure.
func (f *turnFinalizer) finalizeFailed(ctx context.Context, in turnFinalizeInput) bool {
	in.outcome = turnOutcomeFailed
	return f.Finalize(ctx, in)
}
