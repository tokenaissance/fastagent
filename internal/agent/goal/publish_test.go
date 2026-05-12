package goal

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

func TestPublishContinuationRoutesToBus(t *testing.T) {
	mb := bus.New()
	g := &Goal{
		AgentID:     "agent-A",
		OwnerUserID: "user-1",
		Channel:     "web",
		AccountID:   "",
		ChatID:      "chat-1",
		ProjectID:   "proj-X",
		Objective:   "do the thing",
	}
	if !PublishContinuation(mb, g, "Continue working") {
		t.Fatal("PublishContinuation returned false on an empty bus")
	}
	select {
	case got := <-mb.Inbound:
		// The publish path must stamp Source so HandleMessage's
		// trigger-hook gate ("only fire on user-source turns") doesn't
		// recursively kick off another continuation from this one.
		if got.Source != bus.SourceGoalContinuation {
			t.Errorf("Source = %q, want %q", got.Source, bus.SourceGoalContinuation)
		}
		// Routing must match the goal — landing on the wrong channel
		// would put the prompt in front of someone else.
		if got.Channel != "web" || got.ChatID != "chat-1" || got.ProjectID != "proj-X" {
			t.Errorf("routing mismatch: channel=%q chat=%q project=%q",
				got.Channel, got.ChatID, got.ProjectID)
		}
		if got.AgentID != "agent-A" || got.OwnerUserID != "user-1" {
			t.Errorf("agent/owner mismatch: agent=%q owner=%q", got.AgentID, got.OwnerUserID)
		}
		if !strings.Contains(got.Text, "Continue working") {
			t.Errorf("Text = %q, missing prompt body", got.Text)
		}
	default:
		t.Fatal("nothing on bus after PublishContinuation reported success")
	}
}

func TestPublishContinuationNilSafe(t *testing.T) {
	if PublishContinuation(nil, &Goal{}, "x") {
		t.Error("nil bus should make publish return false")
	}
	if PublishContinuation(bus.New(), nil, "x") {
		t.Error("nil goal should make publish return false")
	}
}

// TestPublishContinuationDropsWhenBusFull guards the design choice:
// PublishContinuation never blocks. A full bus returns false rather
// than stalling the GoalRuntime goroutine.
func TestPublishContinuationDropsWhenBusFull(t *testing.T) {
	mb := bus.New()
	// Inbound is a 100-buffered channel; saturate it.
	for i := 0; i < 100; i++ {
		mb.Inbound <- bus.InboundMessage{}
	}
	g := &Goal{Channel: "web", ChatID: "c"}
	if PublishContinuation(mb, g, "x") {
		t.Error("PublishContinuation should return false when bus is full")
	}
}
