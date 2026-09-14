package agent

// The turn's clock is the ceiling nobody escapes, and it is the one number the
// caller of delegate_task never looked at. A 25-minute request inside a
// 45-minute turn is fine; two of them in one round (they run serially) is not —
// the second cannot finish, and until it is clamped the only visible symptom is
// a tool row that says "Queued (waiting on prior sub-agent)…" until the turn
// dies (2026-09-14).
//
// The clamp reserves subagentTurnMargin so that the sub-agent's own budget
// expires FIRST: that is what lets it salvage a tools-free round instead of
// being cut down mid-flight by the parent.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// observedBudget is what the sub-agent's own context actually got.
func observedBudget(t *testing.T, capture *time.Duration) func(context.Context, int, []provider.Tool) (*provider.Response, error) {
	t.Helper()
	return func(ctx context.Context, _ int, _ []provider.Tool) (*provider.Response, error) {
		if dl, ok := ctx.Deadline(); ok {
			*capture = time.Until(dl)
		}
		return &provider.Response{Content: "brief"}, nil
	}
}

func TestSubagentBudgetIsClampedToTheTurnDeadline(t *testing.T) {
	var seen time.Duration
	a := newSubagentTestAgent(t, &scriptedProvider{respond: observedBudget(t, &seen)})

	const turn = 10 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), turn)
	defer cancel()

	out, err := a.RunSubagent(ctx, tools.SubagentRequest{
		Task: "research", MaxIterations: 2, WallTimeout: 25 * time.Minute,
	})
	if err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if out != "brief" {
		t.Fatalf("out = %q", out)
	}

	want := turn - subagentTurnMargin
	if seen > want+5*time.Second || seen < want-5*time.Second {
		t.Fatalf("sub-agent budget = %s, want ~%s (the turn's remaining time, minus the %s reserved to finish the turn)",
			seen.Round(time.Second), want.Round(time.Second), subagentTurnMargin)
	}
	if seen >= 25*time.Minute {
		t.Fatal("the requested budget was passed through untouched — that is the bug")
	}
}

func TestSubagentBudgetUnderTheTurnCeilingIsUntouched(t *testing.T) {
	var seen time.Duration
	a := newSubagentTestAgent(t, &scriptedProvider{respond: observedBudget(t, &seen)})

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()

	if _, err := a.RunSubagent(ctx, tools.SubagentRequest{
		Task: "research", MaxIterations: 2, WallTimeout: 5 * time.Minute,
	}); err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if seen > 6*time.Minute || seen < 4*time.Minute {
		t.Fatalf("a budget that fits the turn must be left alone, got %s", seen.Round(time.Second))
	}
}

// A parent with no clock (cron turn, CLI, test) has nothing to clamp against.
func TestSubagentBudgetWithoutATurnDeadlineIsUntouched(t *testing.T) {
	var seen time.Duration
	a := newSubagentTestAgent(t, &scriptedProvider{respond: observedBudget(t, &seen)})

	if _, err := a.RunSubagent(context.Background(), tools.SubagentRequest{
		Task: "research", MaxIterations: 2, WallTimeout: 3 * time.Minute,
	}); err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
	if seen > 4*time.Minute || seen < 2*time.Minute {
		t.Fatalf("budget = %s, want the requested 3m", seen.Round(time.Second))
	}
}

// When the turn cannot afford a sub-agent at all, saying so is the whole
// deliverable: starting one would produce the same non-answer with more delay,
// and the parent still has to answer within its own clock.
func TestSubagentRefusesWhenTheTurnHasNoRoomLeft(t *testing.T) {
	prov := &scriptedProvider{}
	a := newSubagentTestAgent(t, prov)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	out, err := a.RunSubagent(ctx, tools.SubagentRequest{
		Task: "research", MaxIterations: 2, WallTimeout: 25 * time.Minute,
	})
	if err == nil {
		t.Fatal("a turn with no room must refuse the delegation")
	}
	if !strings.Contains(err.Error(), "re-issue") || !strings.Contains(err.Error(), "1m0s") {
		t.Fatalf("the refusal must name the remaining time and the next step, got %q", err)
	}
	if out != "" {
		t.Fatalf("nothing was collected, so the text must be empty, got %q", out)
	}
	if n := prov.callCount(); n != 0 {
		t.Fatalf("the sub-agent ran %d model rounds for a turn that had already ended", n)
	}
}
