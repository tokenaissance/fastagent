package goal

import (
	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// PublishContinuation pushes a continuation prompt onto the bus as
// an InboundMessage addressed to the goal's originating chat. The
// shape mirrors what cron emits — same Source-tagged pattern, same
// "system user" convention — so the gateway routes it back through
// the same matchAgent → HandleMessage path the original turn used.
//
// Returns true when the message was queued, false when the bus is
// full (caller decides whether to drop / retry / log). The bus is
// 100-deep and continuations are sparse, so a false return is a
// canary that something else is hot-spamming the bus.
func PublishContinuation(mb *bus.MessageBus, g *Goal, prompt string) bool {
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
		Source:      bus.SourceGoalContinuation,
	}
	select {
	case mb.Inbound <- msg:
		return true
	default:
		return false
	}
}
