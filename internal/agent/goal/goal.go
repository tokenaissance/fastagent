// Package goal implements persisted thread goals — a `/goal <objective>`
// becomes a long-running, audit-driven loop where the runtime keeps
// injecting continuation prompts until the model marks the goal complete,
// the token budget runs out, or the user pauses or clears it.
//
// The design is modeled on OpenAI Codex CLI's /goal (codex-rs/core/src/
// goals.rs). See docs/design/goal.md for the rationale.
package goal

import "time"

// Status is the lifecycle state of a goal. Only four values exist —
// "unmet" is intentionally absent: a goal that cannot complete simply
// stays Active until the user pauses or clears it, with the model
// explaining the blocker in its natural-language reply.
type Status string

const (
	StatusActive        Status = "active"
	StatusPaused        Status = "paused"
	StatusBudgetLimited Status = "budget_limited"
	StatusComplete      Status = "complete"
)

// Valid reports whether s is a known Status.
func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusPaused, StatusBudgetLimited, StatusComplete:
		return true
	}
	return false
}

// IsTerminal reports whether s is a terminal-ish state — the GoalRuntime
// goroutine should stop probing for continuations once a goal enters one
// of these. (BudgetLimited can be reverted via /goal resume but until
// that happens it behaves the same as Complete for continuation
// purposes.)
func (s Status) IsTerminal() bool {
	return s == StatusComplete || s == StatusBudgetLimited
}

// TokenUsage is the per-turn usage snapshot we account against the
// goal's budget. Mirrors the SDK's types.Usage shape so we can copy
// values across without re-mapping.
type TokenUsage struct {
	InputTokens              int64
	OutputTokens             int64
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
}

// Goal is the persisted record of an active or finished goal. One goal
// per (agent, session) — enforced by a UNIQUE index on the underlying
// table.
type Goal struct {
	ID          string
	AgentID     string
	SessionKey  string
	OwnerUserID string

	// Routing tuple stamped at create time. GoalRuntime.maybeContinue
	// publishes the continuation prompt back onto the same bus
	// address the original turn arrived on, mirroring how cron jobs
	// store their (Channel, AccountID, ChatID) so the fired reminder
	// lands in the right chat. ProjectID is included so workspace.Store
	// + sandbox routing pick the same per-project scoping the original
	// turn used. Denormalized rather than looked up via session.Store
	// so a continuation works even if the original session row was
	// later deleted / archived.
	Channel   string
	AccountID string
	ChatID    string
	ProjectID string

	Objective string
	Status    Status

	// TokenBudget is the cap, in goal-tokens (non_cached_input + output).
	// nil means unbounded.
	TokenBudget *int64
	TokensUsed  int64

	// LastAccountedTokenUsage is the cumulative SDK Usage at the moment
	// we last folded a delta into TokensUsed. Subtract this from the
	// current cumulative usage to get the next delta.
	LastAccountedTokenUsage TokenUsage

	// TimeUsedSeconds accumulates wall-clock time during Active state
	// only. Paused / budget_limited / complete don't add to it.
	TimeUsedSeconds int64
	LastAccountedAt time.Time

	// SafetyMaxIterations is a defense against goal-runtime bugs that
	// could spin continuation injections without burning tokens. Normal
	// goals exit via budget exhaustion or update_goal long before this.
	SafetyMaxIterations int
	Iterations          int

	CreatedAt time.Time
	UpdatedAt time.Time
}

// RemainingTokens returns budget − used (≥0). When the goal has no
// budget, ok is false.
func (g *Goal) RemainingTokens() (remaining int64, ok bool) {
	if g.TokenBudget == nil {
		return 0, false
	}
	return max(0, *g.TokenBudget-g.TokensUsed), true
}

// BudgetExhausted reports whether the goal has hit or passed its token
// budget. A goal without a budget never exhausts.
func (g *Goal) BudgetExhausted() bool {
	if g.TokenBudget == nil {
		return false
	}
	return g.TokensUsed >= *g.TokenBudget
}

// GoalTokenDelta computes how many goal-tokens (non_cached_input +
// output) accrued between two cumulative usage snapshots. Mirrors
// codex-rs/core/src/goals.rs:1581 (goal_token_delta_for_usage).
//
// Rationale:
//   - Cached input is cheap; counting it would let cache-warm sessions
//     burn budget without actually doing work.
//   - Reasoning output isn't counted either, to match Codex. May revisit
//     if real-world usage shows budgets going stale for thinking-heavy
//     providers.
//
// provider.Usage.InputTokens is already the *uncached* prompt total —
// the Anthropic adapter copies its `input_tokens` (which excludes
// cache_read_input_tokens) directly, and the OpenAI adapter subtracts
// prompt_tokens_details.cached_tokens before storing. So we just sum
// InputTokens + OutputTokens here; CacheReadInputTokens stays on the
// TokenUsage snapshot for debugging / dashboards only.
func GoalTokenDelta(curr, baseline TokenUsage) int64 {
	inputDelta := nonNeg(curr.InputTokens - baseline.InputTokens)
	outputDelta := nonNeg(curr.OutputTokens - baseline.OutputTokens)
	return inputDelta + outputDelta
}

func nonNeg(v int64) int64 {
	return max(0, v)
}
