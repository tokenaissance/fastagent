package goal

import "testing"

func TestFoldUsageActiveGoalAccumulates(t *testing.T) {
	budget := int64(1_000_000)
	g := &Goal{
		Status:      StatusActive,
		TokenBudget: &budget,
		TokensUsed:  100,
	}
	// provider.Usage.InputTokens is already the uncached billable
	// portion (Anthropic excludes cache_read_input_tokens by
	// definition; OpenAI's parser subtracts cached_tokens before
	// landing the value). So 200+30 = 230 here, with CacheRead carried
	// alongside for informational use only.
	r := FoldUsage(g, &TokenUsage{InputTokens: 200, CacheReadInputTokens: 50, OutputTokens: 30})
	if r.Delta != 230 {
		t.Errorf("Delta = %d, want 230", r.Delta)
	}
	if r.Exhausted {
		t.Errorf("not exhausted (used %d, budget %d)", g.TokensUsed, *g.TokenBudget)
	}
	if g.TokensUsed != 330 {
		t.Errorf("TokensUsed = %d, want 330 (100 + 230)", g.TokensUsed)
	}
	if g.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", g.Iterations)
	}
}

func TestFoldUsageNonActiveSkipped(t *testing.T) {
	// Continuation turns fire AfterModelCall too; FoldUsage must not
	// keep billing tokens against a budget_limited or complete goal.
	for _, s := range []Status{StatusPaused, StatusBudgetLimited, StatusComplete} {
		g := &Goal{Status: s, TokensUsed: 100}
		r := FoldUsage(g, &TokenUsage{InputTokens: 500, OutputTokens: 100})
		if r.Delta != 0 {
			t.Errorf("status=%s: Delta = %d, want 0 (skipped)", s, r.Delta)
		}
		if g.TokensUsed != 100 {
			t.Errorf("status=%s: TokensUsed mutated from 100 to %d", s, g.TokensUsed)
		}
	}
}

func TestFoldUsageNilSafe(t *testing.T) {
	// Hook adapter pre-filters but FoldUsage is also reachable from
	// tests / future call sites; nil inputs must be a no-op rather
	// than a panic.
	if r := FoldUsage(nil, &TokenUsage{InputTokens: 10}); r.Delta != 0 {
		t.Errorf("nil goal: Delta = %d, want 0", r.Delta)
	}
	if r := FoldUsage(&Goal{Status: StatusActive}, nil); r.Delta != 0 {
		t.Errorf("nil usage: Delta = %d, want 0", r.Delta)
	}
}

func TestFoldUsageZeroDeltaSkipsBookkeeping(t *testing.T) {
	// An all-cached prompt-only call has no billable input — the
	// provider's adapter has already subtracted cached_tokens from
	// InputTokens, so we get InputTokens=0 here. Iterations shouldn't
	// tick and TokensUsed shouldn't move.
	g := &Goal{Status: StatusActive, TokensUsed: 50}
	r := FoldUsage(g, &TokenUsage{InputTokens: 0, CacheReadInputTokens: 100})
	if r.Delta != 0 {
		t.Errorf("Delta = %d, want 0 (all input cached, no output)", r.Delta)
	}
	if g.TokensUsed != 50 || g.Iterations != 0 {
		t.Errorf("zero-delta should not bump TokensUsed (%d) or Iterations (%d)",
			g.TokensUsed, g.Iterations)
	}
}

func TestFoldUsageFlipsToBudgetLimitedAtCap(t *testing.T) {
	budget := int64(100)
	g := &Goal{Status: StatusActive, TokenBudget: &budget, TokensUsed: 90}
	r := FoldUsage(g, &TokenUsage{InputTokens: 5, OutputTokens: 7})
	// delta=12 → used=102 ≥ 100 → exhausted
	if !r.Exhausted {
		t.Fatal("expected exhausted=true")
	}
	if g.Status != StatusBudgetLimited {
		t.Errorf("status = %q, want budget_limited", g.Status)
	}
	if g.TokensUsed != 102 {
		t.Errorf("TokensUsed = %d, want 102 (90 + 12)", g.TokensUsed)
	}
}

func TestFoldUsageUnboundedNeverExhausts(t *testing.T) {
	g := &Goal{Status: StatusActive, TokensUsed: 1_000_000} // TokenBudget intentionally nil
	r := FoldUsage(g, &TokenUsage{OutputTokens: 999_999})
	if r.Exhausted {
		t.Fatal("unbounded goal should never report exhausted")
	}
	if g.Status != StatusActive {
		t.Errorf("status = %q, want active (unbounded)", g.Status)
	}
}

func TestFoldUsageExactBoundaryExhausts(t *testing.T) {
	// "tokens_used >= token_budget" — equality also exhausts. Otherwise
	// a goal sitting at exactly the budget would keep ticking off
	// no-op turns forever.
	budget := int64(100)
	g := &Goal{Status: StatusActive, TokenBudget: &budget, TokensUsed: 95}
	r := FoldUsage(g, &TokenUsage{OutputTokens: 5})
	if !r.Exhausted {
		t.Errorf("used=%d, budget=%d should exhaust", g.TokensUsed, *g.TokenBudget)
	}
}
