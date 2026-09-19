package agent

import (
	"context"
	"errors"
	"log/slog"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// TurnMode states what a caller wants to happen when the target session
// already has a turn in flight. It mirrors Codex's `TurnInputMode` so every
// producer declares its intent instead of relying on whatever the transport
// happens to do (docs/session-turn-integrity.md, P2).
type TurnMode int

const (
	// TurnStartOrQueue waits for the session's turn slot, then runs the turn.
	//
	// This is the default for user-facing entry points: the dashboard keeps
	// steering as a deliberate action (decision D2), so a second message
	// queues behind the running turn instead of changing what the model is
	// doing mid-flight. The wait is bounded by the caller's ctx, and the
	// dashboard is told about it through the "queued" event.
	TurnStartOrQueue TurnMode = iota

	// TurnStartIfIdle refuses instead of waiting.
	//
	// Automatic work (cron tick, goal continuation, heartbeat, subagent
	// delivery) must not hold a queue worker and its turn budget hostage
	// while a user turn finishes: the caller gets ErrTurnNotAdmitted and
	// reschedules at the next idle point.
	TurnStartIfIdle
)

// ErrTurnNotAdmitted reports that a TurnStartIfIdle request found the session
// busy. Nothing was written to the session; the producer decides when to try
// again.
var ErrTurnNotAdmitted = errors.New("turn not admitted: session is busy")

// turnModeForSource maps a runtime source tag onto the admission mode. The
// default source ("") is a real user turn.
func turnModeForSource(source string) TurnMode {
	switch source {
	case bus.SourceCron, bus.SourceGoalContext, bus.SourceHeartbeat, bus.SourceSubAgent:
		return TurnStartIfIdle
	default:
		return TurnStartOrQueue
	}
}

// RunTurn is the admission-aware turn entry point. It differs from
// HandleMessage in exactly one way: automatic sources that find the session
// busy are refused immediately instead of joining the queue, so the caller
// (the gateway task queue) can park the work and hand its slot back.
//
// Everything else — slash commands, quota, plan mode, the ReAct loop — is
// HandleMessage's behaviour, including its wait when a race slips through
// between this check and the slot acquisition.
func (a *Agent) RunTurn(ctx context.Context, msg bus.InboundMessage) (string, error) {
	if a.sessions != nil && turnModeForSource(msg.Source) == TurnStartIfIdle {
		sess := a.sessions.Get(sessionTriple(msg, msg.ProjectID))
		// The verdict must come from the cross-replica fact, not from this
		// pod's memory: an automatic turn (cron / goal / heartbeat / sub-agent)
		// that finds the session busy on ANOTHER replica must defer, not queue
		// — queueing is what holds a task-queue worker and its turn budget
		// hostage (docs/session-turn-integrity.md P2; the 09-19 review found
		// this path reading only the in-process gate).
		if live, _ := a.lease().Live(context.Background(), a.turnLeaseKey(sess)); live != nil {
			slog.Info("turn admission: automatic turn deferred (a peer holds the session)",
				"agent", a.name, "channel", msg.Channel, "chat_id", msg.ChatID,
				"source", msg.Source, "holder", live.Holder, "waiters", sess.TurnWaiters())
			return "", ErrTurnNotAdmitted
		}
		if sess.TurnActive() {
			slog.Info("turn admission: automatic turn deferred",
				"agent", a.name, "channel", msg.Channel, "chat_id", msg.ChatID,
				"source", msg.Source, "waiters", sess.TurnWaiters())
			return "", ErrTurnNotAdmitted
		}
	}
	return a.HandleMessage(ctx, msg), nil
}
