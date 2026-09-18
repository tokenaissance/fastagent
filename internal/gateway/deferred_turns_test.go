package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// A parked automatic turn is submitted once its session is idle, keeps the
// chat's FIFO order, and is dropped (not submitted forever) past the budget.
func TestDeferredTurnsDrainPolicy(t *testing.T) {
	var submitted []bus.InboundMessage
	var keys []string
	busy := true
	d := newDeferredTurns(
		func(agentID, chatKey string, msg bus.InboundMessage, accountID string) {
			submitted = append(submitted, msg)
			keys = append(keys, chatKey)
		},
		func(_ context.Context, _ string, _ bus.InboundMessage) bool { return busy },
		nil,
	)

	d.park("agt_1", "web::chat-a", bus.InboundMessage{Channel: "web", ChatID: "chat-a", Source: bus.SourceCron, Text: "tick-1"}, "")
	d.park("agt_1", "web::chat-a", bus.InboundMessage{Channel: "web", ChatID: "chat-a", Source: bus.SourceCron, Text: "tick-2"}, "")
	d.park("agt_1", "web::chat-b", bus.InboundMessage{Channel: "web", ChatID: "chat-b", Source: bus.SourceGoalContext, Text: "goal"}, "")
	if d.count() != 3 {
		t.Fatalf("parked = %d; want 3", d.count())
	}

	// Still busy: nothing may be submitted (a tick that fired into a writing
	// session is exactly what we are preventing).
	d.drain(context.Background())
	if len(submitted) != 0 {
		t.Fatalf("submitted %d messages while every session was busy", len(submitted))
	}
	if d.count() != 3 {
		t.Fatalf("parked = %d after a busy drain; want 3", d.count())
	}

	// chat-a idle: only its head goes, so tick-2 cannot overtake tick-1.
	busy = false
	d.drain(context.Background())
	if len(submitted) != 2 {
		t.Fatalf("submitted = %v; want one per idle chat (2)", submitted)
	}
	if submitted[0].Text != "tick-1" {
		t.Fatalf("first submission = %q; want tick-1 (FIFO head)", submitted[0].Text)
	}
	for i, k := range keys {
		_ = i
		if k == "" {
			t.Fatal("submit called without a chat key")
		}
	}
	if d.count() != 1 {
		t.Fatalf("parked = %d after the drain; want 1 (tick-2 still queued)", d.count())
	}

	// tick-2 is submitted on the next pass; nothing is left behind.
	d.drain(context.Background())
	if d.count() != 0 {
		t.Fatalf("parked = %d; want 0", d.count())
	}
}

func TestDeferredTurnsDropsMessagesPastBudget(t *testing.T) {
	var submitted int
	now := time.Now()
	d := newDeferredTurns(
		func(string, string, bus.InboundMessage, string) { submitted++ },
		func(context.Context, string, bus.InboundMessage) bool { return true }, // always busy
		nil,
	)
	d.now = func() time.Time { return now }
	d.park("agt_1", "web::chat-a", bus.InboundMessage{Channel: "web", ChatID: "chat-a", Source: bus.SourceCron}, "")

	d.now = func() time.Time { return now.Add(d.maxWait + time.Second) }
	d.drain(context.Background())

	if submitted != 0 {
		t.Fatalf("submitted %d messages after the budget expired; want 0", submitted)
	}
	if d.count() != 0 {
		t.Fatalf("parked = %d; want the expired message dropped", d.count())
	}
}

// A dropped scheduled task is the user's silent failure: they asked for
// something at 9am, the session stayed busy, and the tick disappeared. It is
// now said out loud in that chat — naming the job — while the harness's own
// sources (which re-fire on their next cycle) stay out of the user's way
// (docs 10 §3.5, G12).
func TestDeferredTurnsAnnouncesADroppedScheduledTask(t *testing.T) {
	type note struct {
		msg  bus.InboundMessage
		text string
	}
	var notes []note
	now := time.Now()
	d := newDeferredTurns(
		func(string, string, bus.InboundMessage, string) { t.Fatal("a dropped turn must not be submitted") },
		func(context.Context, string, bus.InboundMessage) bool { return true }, // always busy
		func(msg bus.InboundMessage, text string) { notes = append(notes, note{msg, text}) },
	)
	d.now = func() time.Time { return now }
	d.park("agt_1", "web::chat-a", bus.InboundMessage{
		Channel: "web", ChatID: "chat-a", AgentID: "agt_1", Source: bus.SourceCron,
		Text: "[Cron Job: morning-brief] This is a scheduled task trigger.",
	}, "")
	d.park("agt_1", "web::chat-b", bus.InboundMessage{
		Channel: "web", ChatID: "chat-b", AgentID: "agt_1", Source: bus.SourceHeartbeat,
		Text: "[Heartbeat — 2026-09-18 09:00:00 +0800] tasks",
	}, "")

	d.now = func() time.Time { return now.Add(d.maxWait + time.Second) }
	d.drain(context.Background())

	if len(notes) != 1 {
		t.Fatalf("notes = %d; want exactly one (the cron drop, not the heartbeat)", len(notes))
	}
	if notes[0].msg.ChatID != "chat-a" {
		t.Fatalf("the note went to %q; want the chat whose task was dropped", notes[0].msg.ChatID)
	}
	for _, want := range []string{"morning-brief", "did not run", "busy"} {
		if !strings.Contains(notes[0].text, want) {
			t.Fatalf("the note does not contain %q:\n%s", want, notes[0].text)
		}
	}
}

// A trigger text without the scheduler's marker must still produce a sentence,
// never a dangling quote (docs 10 §3.5, G12).
func TestDroppedCronNoteWithoutAJobName(t *testing.T) {
	got := droppedCronNote("no marker here", 5*time.Minute)
	if strings.Contains(got, `""`) {
		t.Fatalf("the note rendered an empty name:\n%s", got)
	}
	if !strings.Contains(got, "A scheduled task") {
		t.Fatalf("the note lost its subject:\n%s", got)
	}
}
