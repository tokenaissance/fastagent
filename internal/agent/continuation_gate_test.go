package agent

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// TestContinuationGateDropsAfterPause is the load-bearing fix for
// the "one extra reply after /goal pause" race. The agent's
// continuation goroutine queues N+1 while continuation N is still
// in flight; the user's pause lands behind it in the bus FIFO.
// Without the gate, N+1 is processed and the user sees an unwanted
// extra reply. With the gate, HandleMessage sees status != Active
// and drops it.
func TestContinuationGateDropsAfterPause(t *testing.T) {
	a := newSlashTestAgent(t)
	a.slashGoal(webMsg(), strongArgs())
	// Pause turns status → Paused. Subsequent continuations on the
	// same session should be dropped at the gate.
	a.slashGoal(webMsg(), []string{"pause"})

	cont := bus.InboundMessage{
		Channel:   "web",
		AccountID: "",
		ChatID:    "chat-1",
		Source:    bus.SourceGoalContinuation,
	}
	if a.shouldProcessGoalContinuation(context.Background(), cont) {
		t.Error("continuation must be dropped when goal is paused")
	}
}

// TestContinuationGateDropsAfterClear: same fix path for the clear
// branch. /goal clear deletes the row; a still-in-flight
// continuation must be dropped, not processed against a goal that
// no longer exists.
func TestContinuationGateDropsAfterClear(t *testing.T) {
	a := newSlashTestAgent(t)
	a.slashGoal(webMsg(), strongArgs())
	a.slashGoal(webMsg(), []string{"clear"})

	cont := bus.InboundMessage{
		Channel:   "web",
		AccountID: "",
		ChatID:    "chat-1",
		Source:    bus.SourceGoalContinuation,
	}
	if a.shouldProcessGoalContinuation(context.Background(), cont) {
		t.Error("continuation must be dropped when goal is cleared")
	}
}

// TestContinuationGatePassesActive: the happy path — an Active
// goal lets the continuation through. The gate must not over-
// trigger and drop legitimate continuations.
func TestContinuationGatePassesActive(t *testing.T) {
	a := newSlashTestAgent(t)
	a.slashGoal(webMsg(), strongArgs())

	cont := bus.InboundMessage{
		Channel:   "web",
		AccountID: "",
		ChatID:    "chat-1",
		Source:    bus.SourceGoalContinuation,
	}
	if !a.shouldProcessGoalContinuation(context.Background(), cont) {
		t.Error("continuation should pass through when goal is active")
	}
}

// TestBudgetLimitBypassesGate: SourceGoalBudgetLimit must always
// pass — it's the wrap-up turn the token-accounting hook published
// precisely because status flipped to BudgetLimited. Treating the
// two Source values identically would drop the wrap-up.
func TestBudgetLimitBypassesGate(t *testing.T) {
	a := newSlashTestAgent(t)
	// Seed a budget_limited goal: create active, then directly flip
	// the row to budget_limited (simulating the hook's transition).
	a.slashGoal(webMsg(), strongArgs())
	key := a.resolveSessionKey(webMsg())
	g, _ := a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	g.Status = goal.StatusBudgetLimited
	_ = a.goalStore.UpdateGoal(context.Background(), g)

	bl := bus.InboundMessage{
		Channel:   "web",
		AccountID: "",
		ChatID:    "chat-1",
		Source:    bus.SourceGoalBudgetLimit,
	}
	if !a.shouldProcessGoalContinuation(context.Background(), bl) {
		t.Error("budget_limit prompt must bypass the continuation-status gate")
	}

	// And the contrast — a GoalContinuation on the same row would
	// be dropped, because the gate is what differentiates the two.
	cont := bl
	cont.Source = bus.SourceGoalContinuation
	if a.shouldProcessGoalContinuation(context.Background(), cont) {
		t.Error("regular continuation on a budget_limited goal should be dropped")
	}
}

// TestUserSourceAlwaysPasses: the gate must not interfere with
// genuine user turns. The whole feature would be broken otherwise.
func TestUserSourceAlwaysPasses(t *testing.T) {
	a := newSlashTestAgent(t)
	a.slashGoal(webMsg(), strongArgs())
	a.slashGoal(webMsg(), []string{"pause"})

	usr := bus.InboundMessage{
		Channel:   "web",
		AccountID: "",
		ChatID:    "chat-1",
		Source:    bus.SourceUser,
		Text:      "wait, can you do X instead",
	}
	if !a.shouldProcessGoalContinuation(context.Background(), usr) {
		t.Error("user-source messages must always pass the gate")
	}
}

// TestGateNoOpWithoutStore: an agent without /goal wired must not
// try to load goals from a nil store on every message.
func TestGateNoOpWithoutStore(t *testing.T) {
	a := newSlashTestAgent(t)
	// Force-disable the feature by zeroing the store + manager that
	// newSlashTestAgent wired.
	a.goalStore = nil
	a.goalManager = nil

	cont := bus.InboundMessage{
		Channel: "web",
		ChatID:  "chat-1",
		Source:  bus.SourceGoalContinuation,
	}
	if !a.shouldProcessGoalContinuation(context.Background(), cont) {
		t.Error("gate must be a no-op when /goal isn't wired")
	}
}
