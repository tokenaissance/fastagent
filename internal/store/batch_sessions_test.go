package store

import (
	"context"
	"testing"
	"time"
)

func TestListAllSessionMetas(t *testing.T) {
	db := setupBatchTestStore(t)
	ctx := context.Background()

	// Seed sessions for two different (user, agent) pairs.
	sessions := []struct {
		userID, agentID, key, channel string
	}{
		{"u1", "a1", "sess-1", "web"},
		{"u1", "a1", "sess-2", "telegram"},
		{"u2", "a1", "sess-3", "web"},
		{"u2", "a2", "sess-4", "web"},
	}
	for _, s := range sessions {
		err := db.SaveSession(ctx, s.userID, s.agentID, s.key, &SessionRecord{
			Channel:  s.channel,
			Messages: []SessionMessage{{Role: "user", Content: "hello"}},
		})
		if err != nil {
			t.Fatalf("SaveSession(%s/%s/%s): %v", s.userID, s.agentID, s.key, err)
		}
	}

	metas, err := db.ListAllSessionMetas(ctx)
	if err != nil {
		t.Fatalf("ListAllSessionMetas: %v", err)
	}
	if len(metas) != 4 {
		t.Fatalf("got %d metas, want 4", len(metas))
	}

	// Verify all sessions are present with correct ownership.
	found := map[string]bool{}
	for _, m := range metas {
		found[m.UserID+"/"+m.AgentID+"/"+m.Key] = true
	}
	for _, s := range sessions {
		k := s.userID + "/" + s.agentID + "/" + s.key
		if !found[k] {
			t.Errorf("missing session %s", k)
		}
	}
}

func TestListAllSessionMetas_ExcludesEmptyOwnership(t *testing.T) {
	db := setupBatchTestStore(t)
	ctx := context.Background()

	// Session with empty user_id should be excluded.
	// We can't use SaveSession (it requires user_id), so insert directly.
	_, err := db.db.ExecContext(ctx,
		`INSERT INTO sessions (user_id, agent_id, session_key, channel, messages, message_count, updated_at)
		 VALUES ('', 'a1', 'orphan', 'web', '[]', 0, ?)`, time.Now().UTC())
	if err != nil {
		t.Fatalf("insert orphan: %v", err)
	}

	// Normal session.
	if err := db.SaveSession(ctx, "u1", "a1", "real", &SessionRecord{
		Channel:  "web",
		Messages: []SessionMessage{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	metas, err := db.ListAllSessionMetas(ctx)
	if err != nil {
		t.Fatalf("ListAllSessionMetas: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("got %d metas, want 1 (orphan excluded)", len(metas))
	}
	if metas[0].Key != "real" {
		t.Errorf("got key %q, want %q", metas[0].Key, "real")
	}
}

func TestBatchFirstUserMessages(t *testing.T) {
	db := setupBatchTestStore(t)
	ctx := context.Background()

	// Create two sessions.
	if err := db.SaveSession(ctx, "u1", "a1", "s1", &SessionRecord{
		Channel: "web", Messages: []SessionMessage{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSession(ctx, "u1", "a1", "s2", &SessionRecord{
		Channel: "web", Messages: []SessionMessage{},
	}); err != nil {
		t.Fatal(err)
	}

	// Append messages: s1 has assistant then user; s2 has user directly.
	msgs := []struct {
		userID, agentID, key string
		msg                  SessionMessage
	}{
		{"u1", "a1", "s1", SessionMessage{Role: "assistant", Content: "hi there"}},
		{"u1", "a1", "s1", SessionMessage{Role: "user", Content: "first question"}},
		{"u1", "a1", "s1", SessionMessage{Role: "user", Content: "second question"}},
		{"u1", "a1", "s2", SessionMessage{Role: "user", Content: "only question"}},
	}
	for _, m := range msgs {
		if err := db.AppendSessionMessage(ctx, m.userID, m.agentID, m.key, m.msg); err != nil {
			t.Fatalf("AppendSessionMessage: %v", err)
		}
	}

	result, err := db.BatchFirstUserMessages(ctx)
	if err != nil {
		t.Fatalf("BatchFirstUserMessages: %v", err)
	}

	// s1: first user message is "first question" (assistant msg skipped by WHERE role='user')
	key1 := "u1\x00a1\x00s1"
	if msg, ok := result[key1]; !ok {
		t.Error("missing s1 in result")
	} else if msg.Content != "first question" {
		t.Errorf("s1 content = %q, want %q", msg.Content, "first question")
	}

	// s2: first user message is "only question"
	key2 := "u1\x00a1\x00s2"
	if msg, ok := result[key2]; !ok {
		t.Error("missing s2 in result")
	} else if msg.Content != "only question" {
		t.Errorf("s2 content = %q, want %q", msg.Content, "only question")
	}
}

func TestBatchFirstUserMessages_Empty(t *testing.T) {
	db := setupBatchTestStore(t)
	ctx := context.Background()

	// No sessions, no messages.
	result, err := db.BatchFirstUserMessages(ctx)
	if err != nil {
		t.Fatalf("BatchFirstUserMessages: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("got %d results, want 0", len(result))
	}
}

func TestBatchFirstUserMessages_OnlyAssistantMessages(t *testing.T) {
	db := setupBatchTestStore(t)
	ctx := context.Background()

	if err := db.SaveSession(ctx, "u1", "a1", "s1", &SessionRecord{
		Channel: "web", Messages: []SessionMessage{},
	}); err != nil {
		t.Fatal(err)
	}
	// Only assistant messages — no user messages to return.
	if err := db.AppendSessionMessage(ctx, "u1", "a1", "s1", SessionMessage{
		Role: "assistant", Content: "hello",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := db.BatchFirstUserMessages(ctx)
	if err != nil {
		t.Fatalf("BatchFirstUserMessages: %v", err)
	}
	if _, ok := result["u1\x00a1\x00s1"]; ok {
		t.Error("should not have entry for session with no user messages")
	}
}
