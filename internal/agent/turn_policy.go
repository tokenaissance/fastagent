package agent

import (
	"context"
	"crypto/sha256"
	"log/slog"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/session"
)

// turnBoundaryStop is the one place that decides whether a turn must stop
// before spending another model round. Both loops read the same two facts:
// the lease was lost to a peer, or the user asked this possession to stop.
func (a *Agent) turnBoundaryStop(
	ctx context.Context,
	lease *turnLeaseGuard,
	sess *session.Session,
	msg bus.InboundMessage,
	iteration int,
) string {
	if lease.Lost() || sess.FenceLost() != nil {
		slog.Warn("turn: superseded, stopping",
			"agent", a.name, "chat_id", msg.ChatID, "iteration", iteration)
		return "superseded"
	}
	if lease.Cancelled(ctx) {
		slog.Info("turn: cancelled by request",
			"agent", a.name, "chat_id", msg.ChatID, "iteration", iteration)
		emitEvent(ctx, ChatEvent{Type: lostNoticeEvent, Data: map[string]any{
			"message": turnCancelledNotice,
			"ending":  EndingStopped,
			"reason":  "cancelled",
		}})
		return "cancelled"
	}
	return ""
}

type toolCallSignature struct {
	name string
	hash [32]byte
}

// detectToolLoop reports whether the same tool with the same arguments has
// been called three times in a row. The caller owns the warning message
// because it persists it into its own history projection.
func detectToolLoop(agentName string, calls []provider.ToolCall, last *toolCallSignature, consecutive *int) bool {
	for _, tc := range calls {
		sig := toolCallSignature{
			name: tc.Function.Name,
			hash: sha256.Sum256([]byte(tc.Function.Arguments)),
		}
		if sig.name == last.name && sig.hash == last.hash {
			*consecutive++
		} else {
			*consecutive = 1
			*last = sig
		}
		if *consecutive >= 3 {
			slog.Warn("tool loop detected", "agent", agentName, "tool", tc.Function.Name)
			return true
		}
	}
	return false
}

// extendIterationBudget decides whether the segment that just ended earned
// another round budget. It returns the new rounds and segments counters, the
// nudge to append, and whether an extension happened.
func extendIterationBudget(
	rt *agentRuntime,
	round, rounds, segmentsUsed int,
	segProgress bool,
) (int, int, provider.Message, bool) {
	if round != rounds-1 || !segProgress || segmentsUsed > rt.maxToolContinues {
		return rounds, segmentsUsed, provider.Message{}, false
	}
	nextSegments := segmentsUsed + 1
	return rounds + rt.maxToolIterations,
		nextSegments,
		iterationContinueNudge(rt.maxToolIterations, nextSegments, 1+rt.maxToolContinues),
		true
}
