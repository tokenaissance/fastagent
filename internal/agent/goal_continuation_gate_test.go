package agent

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// The PostTurn goal hook fires while the turn still holds the session slot —
// the slot's defer is the outermost one, so the hook runs first. It must
// therefore publish the continuation onto the bus; running the next turn
// inline would ask for the slot the caller is already holding and deadlock the
// session (docs/session-turn-integrity.md, clause W).
func TestGoalContinuationDoesNotDeadlock(t *testing.T) {
	a, _ := newGateAgent(t)
	st := &memGoalStore{}
	a.WireGoals(st)

	msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-goal", Text: "start"}
	sess := a.sessions.Get(sessionTriple(msg, msg.ProjectID))
	if err := st.CreateGoal(context.Background(), &goal.Goal{
		ID:          "g-deadlock",
		AgentID:     a.name,
		OwnerUserID: a.ownerUserID,
		SessionKey:  sess.SessionKey(),
		Channel:     msg.Channel,
		ChatID:      msg.ChatID,
		Objective:   "keep going",
		Status:      goal.StatusActive,
	}); err != nil {
		t.Fatalf("seed goal: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.HandleMessage(context.Background(), msg)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("turn never returned: the continuation ran inline and deadlocked on the session slot it holds")
	}

	// The hook published the continuation rather than running it.
	var cont bus.InboundMessage
	select {
	case cont = <-a.messageBus.Inbound:
	case <-time.After(2 * time.Second):
		t.Fatal("no continuation was published for the active goal")
	}
	if cont.Source != bus.SourceGoalContext {
		t.Fatalf("continuation source = %q; want %q", cont.Source, bus.SourceGoalContext)
	}
	if cont.ChatID != msg.ChatID {
		t.Fatalf("continuation chat_id = %q; want %q — it has to land in the session that owns the goal",
			cont.ChatID, msg.ChatID)
	}

	// The slot is free again: this is what the deadlock would have prevented,
	// so a continuation that can run now is the positive half of the assertion.
	contDone := make(chan struct{})
	go func() {
		defer close(contDone)
		a.HandleMessage(context.Background(), cont)
	}()
	select {
	case <-contDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the continuation could not take the session slot: the previous turn never released it")
	}
	if msgs := sess.GetMessages(); len(msgs) < 4 {
		t.Fatalf("history after the continuation = %v; want the continuation turn appended too", messageRoles(msgs))
	}
}
