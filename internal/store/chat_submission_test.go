package store

import (
	"context"
	"testing"
	"time"
)

func TestAcceptChatSubmissionClaimsAndEnqueuesAtomically(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	entry := &SessionQueueEntry{
		ID: "t1", UserID: "u1", AgentID: "a1", SessionID: "s1",
		Text: "first", ExpiresAt: time.Now().Add(time.Minute),
	}
	v, first, seq, err := db.AcceptChatSubmission(ctx, "scope", "digest", "t1", entry, time.Minute)
	if err != nil || v != IdempotencyMiss || first != "t1" || seq != 1 {
		t.Fatalf("first = %q %q %d %v", v, first, seq, err)
	}
	queued, err := db.ListSessionQueue(ctx, "u1", "a1", "s1")
	if err != nil || len(queued) != 1 || queued[0].Text != "first" {
		t.Fatalf("queue = %+v err=%v", queued, err)
	}

	// A duplicate returns the first turn and creates no second row.
	v, first, _, err = db.AcceptChatSubmission(ctx, "scope", "digest", "t2", &SessionQueueEntry{
		ID: "t2", UserID: "u1", AgentID: "a1", SessionID: "s1",
		Text: "retry", ExpiresAt: time.Now().Add(time.Minute),
	}, time.Minute)
	if err != nil || v != IdempotencyDuplicate || first != "t1" {
		t.Fatalf("duplicate = %q %q %v", v, first, err)
	}
	queued, _ = db.ListSessionQueue(ctx, "u1", "a1", "s1")
	if len(queued) != 1 {
		t.Fatalf("duplicate created a queue row: %d", len(queued))
	}

	if v, _, _, err := db.AcceptChatSubmission(ctx, "scope", "other", "t3", &SessionQueueEntry{
		ID: "t3", UserID: "u1", AgentID: "a1", SessionID: "s1",
		Text: "different", ExpiresAt: time.Now().Add(time.Minute),
	}, time.Minute); err != nil || v != IdempotencyConflict {
		t.Fatalf("conflict = %q %v", v, err)
	}
}

func TestUpdateSessionQueueTextOnlyWhileQueued(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	entry := &SessionQueueEntry{ID: "t1", UserID: "u1", AgentID: "a1", SessionID: "s1", Text: "raw", ExpiresAt: time.Now().Add(time.Minute)}
	if _, _, _, err := db.AcceptChatSubmission(ctx, "s", "d", "t1", entry, time.Minute); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := db.UpdateSessionQueueText(ctx, "t1", "annotated"); err != nil {
		t.Fatalf("update queued: %v", err)
	}
	got, _ := db.GetSessionQueue(ctx, "t1")
	if got.Text != "annotated" {
		t.Fatalf("queued text = %q", got.Text)
	}
	if err := db.MarkSessionQueueStarted(ctx, "t1"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := db.UpdateSessionQueueText(ctx, "t1", "late"); err != nil {
		t.Fatalf("update started: %v", err)
	}
	got, _ = db.GetSessionQueue(ctx, "t1")
	if got.Text != "" {
		t.Fatalf("started row text = %q; want cleared", got.Text)
	}
}
