package goal

import (
	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// PublishContinuation pushes a regular continuation prompt onto the
// bus. Tagged with bus.SourceGoalContinuation, which makes
// HandleMessage's continuation-status gate eligible to drop the
// message if the goal was paused/cleared between this publish and
// delivery — exactly what we want for "user hit pause but the
// goroutine had already queued one more".
func PublishContinuation(mb *bus.MessageBus, g *Goal, prompt string) bool {
	return publish(mb, g, prompt, bus.SourceGoalContinuation)
}

// PublishBudgetLimit pushes the budget_limit wrap-up prompt onto
// the bus. Tagged with bus.SourceGoalBudgetLimit so HandleMessage's
// continuation-status gate lets it through even though the goal's
// status is now BudgetLimited (the gate's whole point is to drop
// stale Active-only continuations, not the wrap-up turn the budget
// transition explicitly published).
func PublishBudgetLimit(mb *bus.MessageBus, g *Goal, prompt string) bool {
	return publish(mb, g, prompt, bus.SourceGoalBudgetLimit)
}

// publish is the shared body. Returns true when the message was
// queued, false when the bus is full (caller decides whether to
// drop / retry / log). The bus is 100-deep and these messages are
// sparse, so a false return is a canary that something else is
// hot-spamming the bus.
func publish(mb *bus.MessageBus, g *Goal, prompt, source string) bool {
	if mb == nil || g == nil {
		return false
	}
	msg := bus.InboundMessage{
		Channel:     g.Channel,
		AccountID:   g.AccountID,
		ChatID:      g.ChatID,
		ProjectID:   g.ProjectID,
		UserID:      "goal",
		OwnerUserID: g.OwnerUserID,
		AgentID:     g.AgentID,
		Text:        prompt,
		PeerKind:    "dm",
		Source:      source,
	}
	select {
	case mb.Inbound <- msg:
		return true
	default:
		return false
	}
}
