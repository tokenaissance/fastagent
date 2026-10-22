package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSessionQueueOrdersAndCancels(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	now := time.Now()
	for i := 1; i <= 3; i++ {
		e := &SessionQueueEntry{
			ID:        fmt.Sprintf("turn-%d", i),
			UserID:    "u1",
			AgentID:   "a1",
			SessionID: "s1",
			Text:      fmt.Sprintf("msg-%d", i),
			CreatedAt: now,
			ExpiresAt: now.Add(time.Minute),
		}
		seq, err := db.EnqueueSessionQueue(ctx, e)
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		if seq != int64(i) {
			t.Fatalf("seq %d = %d; want %d", i, seq, i)
		}
	}

	list, err := db.ListSessionQueue(ctx, "u1", "a1", "s1")
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}
	for i, e := range list {
		if e.Seq != int64(i+1) || e.Text != fmt.Sprintf("msg-%d", i+1) {
			t.Fatalf("entry %d = %+v", i, e)
		}
	}

	if ok, err := db.CancelSessionQueue(ctx, "turn-2"); err != nil || !ok {
		t.Fatalf("cancel: ok=%v err=%v", ok, err)
	}
	if ok, err := db.CancelSessionQueue(ctx, "turn-2"); err != nil || ok {
		t.Fatalf("second cancel: ok=%v err=%v; want false", ok, err)
	}
	got, err := db.GetSessionQueue(ctx, "turn-2")
	if err != nil || got.State != SessionQueueCanceled {
		t.Fatalf("get canceled: %+v err=%v", got, err)
	}
	if _, err := db.GetSessionQueue(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing entry err = %v; want ErrNotFound", err)
	}
}

func TestSessionQueueSeqIsUniqueUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := db.EnqueueSessionQueue(ctx, &SessionQueueEntry{
				ID:        fmt.Sprintf("t-%d", i),
				UserID:    "u1",
				AgentID:   "a1",
				SessionID: "s1",
				Text:      "x",
				ExpiresAt: time.Now().Add(time.Minute),
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent enqueue: %v", err)
		}
	}
	list, err := db.ListSessionQueue(ctx, "u1", "a1", "s1")
	if err != nil || len(list) != 10 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}
	for i, e := range list {
		if e.Seq != int64(i+1) {
			t.Fatalf("seq[%d] = %d; want %d", i, e.Seq, i+1)
		}
	}
}

func TestSessionQueueCleanupKeepsLiveEntries(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	old := time.Now().Add(-2 * time.Hour)
	for _, e := range []*SessionQueueEntry{
		{ID: "live", UserID: "u1", AgentID: "a1", SessionID: "s1", CreatedAt: old, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: "done", UserID: "u1", AgentID: "a1", SessionID: "s1", CreatedAt: old, ExpiresAt: time.Now().Add(time.Hour)},
	} {
		if _, err := db.EnqueueSessionQueue(ctx, e); err != nil {
			t.Fatalf("enqueue %s: %v", e.ID, err)
		}
	}
	if err := db.MarkSessionQueueStarted(ctx, "done"); err != nil {
		t.Fatalf("start: %v", err)
	}
	n, err := db.CleanupSessionQueue(ctx, time.Now().Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("cleanup n=%d err=%v; want 1", n, err)
	}
	if _, err := db.GetSessionQueue(ctx, "live"); err != nil {
		t.Fatalf("live entry was removed: %v", err)
	}
	if _, err := db.GetSessionQueue(ctx, "done"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("done entry err=%v; want ErrNotFound", err)
	}
}
