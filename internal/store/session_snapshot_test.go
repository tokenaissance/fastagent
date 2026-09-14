package store

import (
	"context"
	"testing"
	"time"
)

// ListSessionSnapshots is the read path behind `fastagent doctor sessions`:
// it has to return identity columns plus the working set, newest first, and
// narrow to one agent on request. Getting the ordering wrong would make a
// doctor report name the wrong session; getting the filter wrong would scan
// the whole deployment when an operator asked for one agent.
func TestListSessionSnapshotsOrderingAndFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	seed := func(userID, agentID, key, content string) {
		t.Helper()
		if err := db.SaveSession(ctx, userID, agentID, key, &SessionRecord{
			Channel: "web",
			ChatID:  key,
			Messages: []SessionMessage{
				{Role: "user", Content: content},
				{Role: "assistant", Content: "ok"},
			},
		}); err != nil {
			t.Fatalf("save %s: %v", key, err)
		}
	}
	seed("u_1", "agt_a", "s-old", "old")
	time.Sleep(5 * time.Millisecond)
	seed("u_1", "agt_a", "s-new", "new")
	seed("u_2", "agt_b", "s-other", "other")

	all, err := db.ListSessionSnapshots(ctx, "", 0)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("snapshots = %d; want 3", len(all))
	}
	if all[0].SessionKey != "s-other" {
		t.Fatalf("newest snapshot = %q; want s-other (ORDER BY updated_at DESC)", all[0].SessionKey)
	}
	for _, snap := range all {
		if snap.SessionKey == "s-new" && len(snap.Messages) != 2 {
			t.Fatalf("snapshot %s carried %d messages; want 2", snap.SessionKey, len(snap.Messages))
		}
	}

	one, err := db.ListSessionSnapshots(ctx, "agt_a", 1)
	if err != nil {
		t.Fatalf("list agt_a: %v", err)
	}
	if len(one) != 1 || one[0].AgentID != "agt_a" || one[0].SessionKey != "s-new" {
		t.Fatalf("agent-filtered+limited = %+v; want agt_a/s-new only", one)
	}
}
