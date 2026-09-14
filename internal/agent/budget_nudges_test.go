package agent

// The three "you are out of budget" messages and the two same-shape/two-audience
// warnings now live next to each other in loop.go. Co-locating them is only worth
// anything if the differences survive: these tests pin that each message states
// its OWN reason, so a later cleanup that merges them into one text fails here
// instead of confusing a sub-agent in production.

import (
	"strings"
	"testing"
	"time"
)

func TestBudgetNudgesStateTheirOwnReason(t *testing.T) {
	cap := capReachedNudge(20).Content
	cont := iterationContinueNudge(20, 2, 2).Content
	wall := budgetNudge(15 * time.Minute).Content

	if !strings.Contains(cap, "20") || !strings.Contains(cap, "Tools are now disabled") {
		t.Fatalf("capReachedNudge must state the budget and that tools are off: %q", cap)
	}
	if !strings.Contains(cont, "the turn continues") || strings.Contains(cont, "Tools are now disabled") {
		t.Fatalf("iterationContinueNudge must say the turn continues, not that it is over: %q", cont)
	}
	if !strings.Contains(wall, "wall-time budget") {
		t.Fatalf("budgetNudge must name the wall clock, not rounds: %q", wall)
	}
	for _, pair := range [][2]string{{cap, cont}, {cap, wall}, {cont, wall}} {
		if pair[0] == pair[1] {
			t.Fatal("two budget messages collapsed into one text — the audiences differ")
		}
	}
}

func TestLoopDetectedWarningDiffersByAudience(t *testing.T) {
	main := loopDetectedWarning(false).Content
	sub := loopDetectedWarning(true).Content

	for _, msg := range []string{main, sub} {
		if !strings.Contains(msg, "Loop detected") {
			t.Fatalf("warning must name the problem: %q", msg)
		}
	}
	if !strings.Contains(main, "try a different approach") {
		t.Fatalf("main loop wants another approach: %q", main)
	}
	if !strings.Contains(sub, "produce the deliverable") {
		t.Fatalf("a sub-agent must stop exploring and hand back its artifact: %q", sub)
	}
}

func TestFailedRoundsNudgeDiffersByAudience(t *testing.T) {
	main := failedRoundsNudge(3, false).Content
	sub := failedRoundsNudge(3, true).Content

	for _, msg := range []string{main, sub} {
		if !strings.Contains(msg, "3") || !strings.Contains(msg, "Stop calling tools") {
			t.Fatalf("nudge must state the round count and the instruction: %q", msg)
		}
	}
	if !strings.Contains(main, "answer the user directly") {
		t.Fatalf("main loop answers the user: %q", main)
	}
	if !strings.Contains(sub, "produce the deliverable") {
		t.Fatalf("sub-agent produces its parent's artifact: %q", sub)
	}
}
