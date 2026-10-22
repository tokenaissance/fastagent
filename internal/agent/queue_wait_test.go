package agent

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestQueueHeadWaitUsesStoreSequence(t *testing.T) {
	a, _ := newGateAgent(t)
	db, err := store.NewDBStore("sqlite", "file:queue-wait?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	a.dataStore = db

	enqueue := func(id string) {
		if _, err := db.EnqueueSessionQueue(context.Background(), &store.SessionQueueEntry{
			ID: id, UserID: "u1", AgentID: "a1", SessionID: "s1",
			Text: "text-" + id, ExpiresAt: time.Now().Add(time.Minute),
		}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	enqueue("turn-1")
	enqueue("turn-2")

	ctx2 := ContextWithTurnID(context.Background(), "turn-2")
	if !a.queueHeadWait(ctx2) {
		t.Fatal("second ticket must wait behind the first")
	}
	if pos, ok := a.queuePosition(ctx2); !ok || pos != 2 {
		t.Fatalf("position = %d ok=%v; want 2", pos, ok)
	}

	if ok, err := db.CancelSessionQueue(context.Background(), "turn-1"); err != nil || !ok {
		t.Fatalf("cancel head: ok=%v err=%v", ok, err)
	}
	if a.queueHeadWait(ctx2) {
		t.Fatal("after the head is canceled, the second ticket is the head")
	}
	if pos, ok := a.queuePosition(ctx2); !ok || pos != 1 {
		t.Fatalf("position after cancel = %d ok=%v; want 1", pos, ok)
	}

	ctx1 := ContextWithTurnID(context.Background(), "turn-1")
	if !a.queueCanceled(ctx1) {
		t.Fatal("a canceled entry must report canceled from the store")
	}
	if a.queueCanceled(ctx2) {
		t.Fatal("a live entry must not report canceled")
	}
}
