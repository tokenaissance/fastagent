package agent

import (
	"context"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// sessionHasActiveGoal reports whether the session this inbound is
// for has a goal in Active state. Used as a hard precedence rule
// over auto-plan-mode: an active goal is an autonomous loop; plan-mode
// is a "wait for human approval" gate. The two cannot coexist on the
// same turn without breaking the goal's autonomy guarantee.
//
// Best-effort: a store error or missing session returns false (the
// caller treats absence as "no active goal"). The check is one
// indexed read per inbound turn — cheap enough to skip caching.
func (a *Agent) sessionHasActiveGoal(ctx context.Context, msg bus.InboundMessage) bool {
	if a.goalStore == nil || a.sessions == nil {
		return false
	}
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	if sess == nil {
		return false
	}
	g, err := a.goalStore.GetGoalBySession(ctx, a.name, sess.SessionKey())
	if err != nil || g == nil {
		return false
	}
	return g.Status == goal.StatusActive
}

// emitGoalIterationIfContinuation publishes a goal_iteration event when
// msg.Source is a goal-runtime continuation and a matching goal row
// exists. Lookup failures are silent — the gate just above us already
// guaranteed status==Active for SourceGoalContinuation, and the
// SourceGoalBudgetLimit path is exempt (we still want the iteration
// signal so the frontend renders the wrap-up turn). Called from both
// HandleMessage and HandleMessageStream right after the staleness gate.
func (a *Agent) emitGoalIterationIfContinuation(ctx context.Context, msg bus.InboundMessage) {
	if msg.Source != bus.SourceGoalContinuation && msg.Source != bus.SourceGoalBudgetLimit {
		return
	}
	if a.goalStore == nil || a.sessions == nil {
		return
	}
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	if sess == nil {
		return
	}
	g, err := a.goalStore.GetGoalBySession(ctx, a.name, sess.SessionKey())
	if err != nil || g == nil {
		return
	}
	emitEvent(ctx, goalIterationEvent(g))
}

// goal_* event constructors. One per ChatEvent.Type to keep the wire
// payload shape consistent across slash, REST, tool, and hook emit
// sites — the frontend's TypeScript shape mirrors goalView().

// goalToView projects a *goal.Goal into the JSON-friendly shape the
// frontend renders. Mirrors tools/goal.goalView so the model-visible
// payload and the frontend-visible payload stay in sync.
func goalToView(g *goal.Goal) map[string]any {
	if g == nil {
		return nil
	}
	v := map[string]any{
		"id":                g.ID,
		"agentId":           g.AgentID,
		"sessionKey":        g.SessionKey,
		"objective":         g.Objective,
		"status":            string(g.Status),
		"tokensUsed":        g.TokensUsed,
		"timeUsedSeconds":   g.TimeUsedSeconds,
		"iterations":        g.Iterations,
	}
	if g.TokenBudget != nil {
		v["tokenBudget"] = *g.TokenBudget
		if remaining, ok := g.RemainingTokens(); ok {
			v["remainingTokens"] = remaining
		}
	}
	return v
}

func goalCreatedEvent(g *goal.Goal) ChatEvent {
	return ChatEvent{
		Type: EventGoalCreated,
		Data: map[string]any{"goal": goalToView(g)},
	}
}

// goalStatusChangedEvent reports a status transition. reason is one of:
//   - user_paused / user_resumed       (slash or REST)
//   - model_completed                  (update_goal tool)
//   - budget_exhausted                 (token-accounting hook)
//   - safety_cap                       (GoalRuntime.maybeContinue cap-flip)
//   - external                         (REST mutation from another pod)
//
// The frontend doesn't need to localize these strings — they ride along
// for analytics / debug surfaces. The user-visible label comes from
// `status`.
func goalStatusChangedEvent(g *goal.Goal, reason string) ChatEvent {
	return ChatEvent{
		Type: EventGoalStatusChanged,
		Data: map[string]any{
			"goal":   goalToView(g),
			"status": string(g.Status),
			"reason": reason,
		},
	}
}

// goalIterationEvent marks the start of one continuation round. Fired
// from HandleMessage / HandleMessageStream when a Source=GoalContinuation
// inbound passes the staleness gate, so the frontend can show "Goal
// continuing… (round N)" inline progress and refresh tokens-used live.
func goalIterationEvent(g *goal.Goal) ChatEvent {
	return ChatEvent{
		Type: EventGoalIteration,
		Data: map[string]any{
			"goal":       goalToView(g),
			"iterations": g.Iterations,
			"tokensUsed": g.TokensUsed,
			"status":     string(g.Status),
		},
	}
}

func goalClearedEvent(goalID string) ChatEvent {
	return ChatEvent{
		Type: EventGoalCleared,
		Data: map[string]any{"goalId": goalID},
	}
}
