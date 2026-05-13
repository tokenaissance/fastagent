package agent

import (
	"context"
	"errors"
	"log/slog"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// NewTokenAccountingHook returns an AfterModelCall hook that folds
// the call's Usage into the active goal for the in-flight session
// and persists the result. Returns nil when st is nil — callers can
// register the result unconditionally without a guard.
//
// The hook gates on three things before doing any work:
//   - HookContext.GoalSessionKey must be non-empty (turn happened
//     inside a chat context)
//   - HookContext.Response.Usage must be non-nil (provider reported
//     token counts)
//   - HookContext.Error must be nil (a failed call has no usage
//     worth folding, even when the provider helpfully returns one)
//
// Anything past those gates routes through goal.FoldUsage and then
// st.UpdateGoal. The hook is fire-and-forget on errors: a store
// failure here would otherwise leak into the agent's response path,
// and the goal will just see the same delta on the next call.
// Errors are logged at warn so they're visible in operations.
//
// When the fold flips a goal to BudgetLimited, the hook publishes
// the budget_limit prompt directly. This is the only path on which
// budget_limit fires — GoalRuntime.maybeContinue handles only the
// Active → continuation case. The transition is observed exactly
// once (FoldUsage's "non-active goals are skipped" gate guarantees
// the same delta doesn't re-trigger on the next call).
func NewTokenAccountingHook(st goal.Store, mb *bus.MessageBus, agentID string) HookFunc {
	if st == nil {
		return nil
	}
	return func(ctx context.Context, hc *HookContext) {
		if hc.Point != AfterModelCall {
			return
		}
		if hc.Error != nil {
			return
		}
		if hc.GoalSessionKey == "" {
			return
		}
		if hc.Response == nil || hc.Response.Usage == nil {
			return
		}

		g, err := st.GetGoalBySession(ctx, agentID, hc.GoalSessionKey)
		if errors.Is(err, goal.ErrNotFound) {
			return
		}
		if err != nil {
			slog.Warn("goal accounting: load goal failed",
				"agent", agentID, "session_key", hc.GoalSessionKey, "error", err)
			return
		}
		if g.Status != goal.StatusActive {
			// Continuation turns for budget_limited / complete goals
			// still fire AfterModelCall; FoldUsage's own gate would
			// reject them anyway, but skipping here saves a store
			// round-trip and keeps the log line below honest.
			return
		}

		usage := providerUsageToGoal(hc.Response.Usage)
		result := goal.FoldUsage(g, &usage)
		if result.Delta == 0 && !result.Exhausted {
			// Nothing changed (e.g. all-cached prompt). Skip the
			// persist round-trip — we'd just rewrite the same row.
			return
		}

		if err := st.UpdateGoal(ctx, g); err != nil {
			slog.Warn("goal accounting: persist failed",
				"agent", agentID, "session_key", hc.GoalSessionKey,
				"delta", result.Delta, "exhausted", result.Exhausted, "error", err)
			return
		}
		if result.Exhausted {
			slog.Info("goal budget exhausted",
				"agent", agentID, "session_key", hc.GoalSessionKey,
				"tokens_used", g.TokensUsed, "token_budget", *g.TokenBudget)
			// Publish the budget_limit prompt. PublishBudgetLimit
			// tags the inbound with SourceGoalBudgetLimit so
			// HandleMessage's "drop stale continuations" gate
			// lets it through — the gate would otherwise see
			// status=BudgetLimited and drop the wrap-up turn.
			prompt := goal.BudgetLimitPrompt(g)
			if !goal.PublishBudgetLimit(mb, g, prompt) {
				slog.Warn("goal accounting: bus full, budget_limit prompt dropped",
					"agent", agentID, "session_key", hc.GoalSessionKey)
			}
		}
	}
}

// providerUsageToGoal copies provider.Usage (int fields) into
// goal.TokenUsage (int64 fields). Separate type so the goal package
// doesn't have to import provider; the cost is one trivial copy at
// the hook boundary.
func providerUsageToGoal(u *provider.Usage) goal.TokenUsage {
	if u == nil {
		return goal.TokenUsage{}
	}
	return goal.TokenUsage{
		InputTokens:              int64(u.InputTokens),
		OutputTokens:             int64(u.OutputTokens),
		CacheReadInputTokens:     int64(u.CacheReadInputTokens),
		CacheCreationInputTokens: int64(u.CacheCreationInputTokens),
	}
}
