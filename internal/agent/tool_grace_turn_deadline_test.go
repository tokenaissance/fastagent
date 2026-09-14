package agent

// The grace boundary is where the turn's clock used to disappear. A tool
// context must outlive its turn (P5), so it deliberately has no deadline — which
// means any tool that sizes its own work inside the turn has to be handed the
// turn's deadline as a value instead. This is the seam, pinned on its own: the
// sub-agent clamp read ctx.Deadline() for a while and was therefore dead code in
// production while every unit test passed.

import (
	"context"
	"testing"
	"time"
)

func TestToolContextCarriesTheTurnsDeadlineAsAValue(t *testing.T) {
	turn, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	toolCtx, stop := toolGraceContext(turn, time.Minute)
	defer stop()

	if _, ok := toolCtx.Deadline(); ok {
		t.Fatal("a tool context must outlive its turn, so it must not inherit the deadline")
	}
	if _, ok := turnRemaining(toolCtx); ok {
		t.Fatal("an unstamped tool context has nothing to read — this is the shape that made the clamp dead code")
	}

	stamped := withTurnDeadline(toolCtx, turn)
	left, ok := turnRemaining(stamped)
	if !ok {
		t.Fatal("the stamped tool context must report the turn's clock")
	}
	if left <= 0 || left > 30*time.Second {
		t.Fatalf("turnRemaining = %s, want up to 30s", left)
	}

	// Cancelling the turn does not erase the fact of when it ends: the tool is
	// still allowed to finish, and its own budget decision must not change.
	cancel()
	if _, ok := turnRemaining(stamped); !ok {
		t.Fatal("a cancelled turn still has an end time")
	}

	// A caller with no turn clock at all (cron tick, CLI, tests) reports
	// nothing, which is not the same as "zero seconds left": nothing to clamp
	// against means the configured budget stands.
	if _, ok := turnRemaining(context.Background()); ok {
		t.Fatal("no deadline anywhere must not look like an expired one")
	}
}
