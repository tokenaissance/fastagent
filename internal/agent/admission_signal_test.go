package agent

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// The admission signal is what lets the dashboard tell "still queued, safe to
// withdraw" from "already started, too late" — so it must stay closed until
// the turn actually holds the session slot.
func TestWithAdmissionSignalClosesWhenTurnStarts(t *testing.T) {
	a, _ := newGateAgent(t)
	msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-sig", Text: "hello"}

	sess := a.sessions.Get(sessionTriple(msg, msg.ProjectID))
	if !sess.AcquireTurn(context.Background()) {
		t.Fatal("could not take the turn slot for the test")
	}

	base, started := WithAdmissionSignal(context.Background())
	done := make(chan struct{})
	go func() {
		a.HandleMessage(base, msg)
		close(done)
	}()

	time.Sleep(150 * time.Millisecond)
	select {
	case <-started:
		t.Fatal("admission signal fired while the turn was still queued")
	default:
	}

	sess.ReleaseTurn()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("admission signal never fired after the turn started")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn never finished")
	}
}
