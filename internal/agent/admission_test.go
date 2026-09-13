package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// A cron/goal tick that finds the session busy must be refused, not queued:
// its caller hands the queue slot back and retries later. Blocking there is
// how a user turn can eat an automatic turn's whole 300 s budget.
func TestRunTurnDefersAutomaticSourceWhenSessionIsBusy(t *testing.T) {
	a, _ := newGateAgent(t)
	msg := bus.InboundMessage{
		Channel: "web", UserID: "u_owner", ChatID: "chat-adm-1",
		Text: "tick", Source: bus.SourceCron,
	}

	sess := a.sessions.Get(sessionTriple(msg, msg.ProjectID))
	if !sess.AcquireTurn(context.Background()) {
		t.Fatal("could not take the turn slot for the test")
	}
	defer sess.ReleaseTurn()

	reply, err := a.RunTurn(context.Background(), msg)
	if !errors.Is(err, ErrTurnNotAdmitted) {
		t.Fatalf("RunTurn error = %v; want ErrTurnNotAdmitted", err)
	}
	if reply != "" {
		t.Fatalf("reply = %q; want empty for a refused turn", reply)
	}
	if msgs := sess.GetMessages(); len(msgs) != 0 {
		t.Fatalf("refused automatic turn wrote %d messages", len(msgs))
	}
}

// A user turn is queued instead (decision D2), and the dashboard is told
// about it with a "queued" event carrying its position in line.
func TestRunTurnQueuesUserSourceAndEmitsQueuedEvent(t *testing.T) {
	a, _ := newGateAgent(t)
	msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-adm-2", Text: "hello"}

	sess := a.sessions.Get(sessionTriple(msg, msg.ProjectID))
	if !sess.AcquireTurn(context.Background()) {
		t.Fatal("could not take the turn slot for the test")
	}

	evts := make(chan ChatEvent, 32)
	ctx := ContextWithChatEvents(context.Background(), evts)
	done := make(chan error, 1)
	go func() {
		_, err := a.RunTurn(ctx, msg)
		done <- err
	}()

	// The queued notice must arrive while the turn is still waiting.
	var queuedPosition float64
	deadline := time.Now().Add(2 * time.Second)
	for queuedPosition == 0 && time.Now().Before(deadline) {
		select {
		case e := <-evts:
			if e.Type == "queued" {
				if p, ok := e.Data["position"].(int); ok {
					queuedPosition = float64(p)
				}
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if queuedPosition == 0 {
		t.Fatal("no queued event while the turn waited")
	}
	if queuedPosition != 1 {
		t.Fatalf("queued position = %v; want 1 (next in line)", queuedPosition)
	}
	select {
	case err := <-done:
		t.Fatalf("queued turn returned early (err=%v)", err)
	default:
	}

	sess.ReleaseTurn()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("queued turn failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued turn never ran after release")
	}
	if msgs := sess.GetMessages(); len(msgs) != 2 {
		t.Fatalf("history after the queued turn = %d messages; want 2", len(msgs))
	}
}

// A steer stays a steer: the manual dashboard action folds into the running
// turn rather than waiting for a slot.
func TestTurnModeForSourcePolicy(t *testing.T) {
	cases := map[string]TurnMode{
		bus.SourceUser:        TurnStartOrQueue,
		bus.SourceCron:        TurnStartIfIdle,
		bus.SourceGoalContext: TurnStartIfIdle,
		bus.SourceHeartbeat:   TurnStartIfIdle,
		bus.SourceSubAgent:    TurnStartIfIdle,
	}
	for source, want := range cases {
		if got := turnModeForSource(source); got != want {
			t.Errorf("turnModeForSource(%q) = %v; want %v", source, got, want)
		}
	}
}
