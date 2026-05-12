package goal

// AccountingResult reports what FoldUsage did to a goal. delta is the
// number of goal-tokens (non_cached_input + output) the call consumed;
// exhausted reports whether the cumulative TokensUsed has reached or
// passed TokenBudget. Both feed downstream behavior: the caller
// persists the goal, logs the delta, and — when exhausted is true —
// flips the goal to BudgetLimited and queues the budget_limit
// continuation.
type AccountingResult struct {
	Delta     int64
	Exhausted bool
}

// FoldUsage applies one model call's Usage to a goal, in place. It is
// the pure-function half of token accounting: no I/O, no time, no
// store. The AfterModelCall hook composes this with goal.Store reads
// + writes to do the full update.
//
// Behavior:
//   - Returns Delta=0, Exhausted=false when the goal is non-Active —
//     paused / budget_limited / complete goals don't get billed.
//     This is the gate that prevents continuation-turn tokens from
//     counting against the original budget after the runtime has
//     already flipped the goal to budget_limited.
//   - Returns Delta=0 when the call delta computes to zero (e.g. an
//     all-cached prompt-only call). Caller can skip the store write.
//   - Increments TokensUsed by Delta. Updates LastAccountedTokenUsage
//     to the cumulative-style snapshot (current InputTokens etc.)
//     so a later refactor to delta-style accounting has the baseline
//     ready without another schema migration.
//   - Flips the goal's Status to BudgetLimited when TokensUsed
//     crosses TokenBudget. Unbounded goals (TokenBudget=nil) never
//     exhaust.
//
// FoldUsage mutates g rather than returning a copy because callers
// (the hook) need to persist the same record they're folding into,
// and copy-then-merge would just invite drift.
func FoldUsage(g *Goal, u *TokenUsage) AccountingResult {
	if g == nil || u == nil {
		return AccountingResult{}
	}
	if g.Status != StatusActive {
		return AccountingResult{}
	}
	delta := GoalTokenDelta(*u, TokenUsage{})
	if delta == 0 {
		return AccountingResult{}
	}
	g.TokensUsed += delta
	// Snapshot the cumulative usage for future delta-style accounting.
	// Today FoldUsage is called once per model call with the per-call
	// Usage (not cumulative), so this just stores the most recent
	// per-call shape — useful for debugging / dashboards and
	// transparent on the future refactor when SDK exposes cumulative.
	g.LastAccountedTokenUsage = *u
	g.Iterations++

	exhausted := false
	if g.TokenBudget != nil && g.TokensUsed >= *g.TokenBudget {
		g.Status = StatusBudgetLimited
		exhausted = true
	}
	return AccountingResult{Delta: delta, Exhausted: exhausted}
}
