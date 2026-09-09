package session

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// TestListWebSessions_MultipleOwnersBatchPreviews proves the per-owner batch
// path: one listing caller (parent) surfaces several child-owned sessions
// plus its own session, each resolving the first user message from the
// append-only archive.
func TestListWebSessions_MultipleOwnersBatchPreviews(t *testing.T) {
	db := newSessionE2EDB(t)
	defer db.Close()
	ctx := context.Background()

	const (
		parentID = "batch_parent"
		agentID  = "batch_agent"
		child1   = "batch_child1"
		child2   = "batch_child2"
	)
	createSessionE2EUser(t, db, parentID, "user", "")
	createSessionE2EUser(t, db, child1, "app_user", parentID)
	createSessionE2EUser(t, db, child2, "app_user", parentID)

	cases := []struct {
		owner, key, wantPreview string
	}{
		{parentID, "sbk_parent", "parent opening"},
		{child1, "sbk_child1", "child1 opening"},
		{child2, "sbk_child2", "child2 opening"},
	}
	for _, c := range cases {
		if err := db.SaveSession(ctx, c.owner, agentID, c.key, &store.SessionRecord{
			Channel:  "web",
			Messages: []store.SessionMessage{{Role: "assistant", Content: "assistant first"}},
		}); err != nil {
			t.Fatalf("save %s: %v", c.key, err)
		}
		// Chronological archive: assistant first, then the user opening turn.
		for _, m := range []store.SessionMessage{
			{Role: "assistant", Content: "assistant first", Timestamp: time.Now().UTC()},
			{Role: "user", Content: c.wantPreview, Timestamp: time.Now().UTC()},
		} {
			if err := db.AppendSessionMessage(ctx, c.owner, agentID, c.key, m); err != nil {
				t.Fatalf("append %s: %v", c.key, err)
			}
		}
	}

	adapter := NewStoreAdapter(db, parentID)
	sessions, err := adapter.ListWebSessions(ctx, agentID)
	if err != nil {
		t.Fatalf("ListWebSessions: %v", err)
	}
	byKey := map[string]WebSession{}
	for _, s := range sessions {
		byKey[s.ID] = s
	}
	for _, c := range cases {
		ws, ok := byKey[c.key]
		if !ok {
			t.Fatalf("missing %q; got %v", c.key, keysOf(byKey))
		}
		if ws.Preview != c.wantPreview {
			t.Errorf("%s preview = %q, want %q", c.key, ws.Preview, c.wantPreview)
		}
	}
}

// TestListWebSessions_BlobOnlyFallback covers legacy sessions that have a
// stored session blob but no append-only archive rows: the batch returns
// nothing for them and ListWebSessions must fall back to the blob path.
func TestListWebSessions_BlobOnlyFallback(t *testing.T) {
	db := newSessionE2EDB(t)
	defer db.Close()
	ctx := context.Background()

	const (
		ownerID = "blob_owner"
		agentID = "blob_agent"
		key     = "sbk_blob"
	)
	createSessionE2EUser(t, db, ownerID, "user", "")
	if err := db.SaveSession(ctx, ownerID, agentID, key, &store.SessionRecord{
		Channel: "web",
		Messages: []store.SessionMessage{
			{Role: "user", Content: "blob-only opening", Timestamp: time.Now().UTC()},
		},
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	adapter := NewStoreAdapter(db, ownerID)
	sessions, err := adapter.ListWebSessions(ctx, agentID)
	if err != nil {
		t.Fatalf("ListWebSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != key {
		t.Fatalf("got %v, want single blob-only session %q", sessions, key)
	}
	if sessions[0].Preview != "blob-only opening" {
		t.Fatalf("preview = %q, want %q", sessions[0].Preview, "blob-only opening")
	}
}

// TestListWebSessions_SkipsSessionsWithoutUserTurn ensures sessions whose
// first messages are assistant-only are omitted, matching the pre-batch
// behavior.
func TestListWebSessions_SkipsSessionsWithoutUserTurn(t *testing.T) {
	db := newSessionE2EDB(t)
	defer db.Close()
	ctx := context.Background()

	const (
		ownerID = "empty_owner"
		agentID = "empty_agent"
		key     = "sbk_empty"
	)
	createSessionE2EUser(t, db, ownerID, "user", "")
	if err := db.SaveSession(ctx, ownerID, agentID, key, &store.SessionRecord{
		Channel:  "web",
		Messages: []store.SessionMessage{{Role: "assistant", Content: "welcome"}},
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if err := db.AppendSessionMessage(ctx, ownerID, agentID, key,
		store.SessionMessage{Role: "assistant", Content: "welcome", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatalf("append: %v", err)
	}

	adapter := NewStoreAdapter(db, ownerID)
	sessions, err := adapter.ListWebSessions(ctx, agentID)
	if err != nil {
		t.Fatalf("ListWebSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("assistant-only session should be omitted, got %v", sessions)
	}
}
