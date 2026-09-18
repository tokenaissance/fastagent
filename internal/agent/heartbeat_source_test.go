package agent

// G14 (docs 10 §4): HEARTBEAT.md used to have two sources. The heartbeat TURN's
// prompt resolves it through ContextBuilder.loadFileForUser (store row first,
// then the on-disk copy), while the TRIGGER read <home>/HEARTBEAT.md straight
// off the pod's disk. With a relational store wired, the agent therefore
// reviewed one file and the tick executed another: a panel edit changed what the
// agent SAW without changing what fired, and a pod-local edit did the reverse.
//
// These tests pin the two halves that must agree — what the tick reads, and what
// the prompt shows — plus the fallback that keeps CLI/no-store shapes working.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func heartbeatFixture(t *testing.T) (*Heartbeat, *ContextBuilder, string /*owner*/) {
	t.Helper()
	ctx := context.Background()
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	now := time.Now().UTC()
	owner := &store.UserRecord{ID: "u_owner_hb", Username: "owner", Email: "o@example.com",
		PasswordHash: "x", Role: "user", Status: "active", AgentQuota: -1, CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, owner); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.SaveAgent(ctx, &store.AgentRecord{ID: "agt_hb", UserID: owner.ID, Name: "hb-agent",
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("save agent: %v", err)
	}

	home := t.TempDir()
	cb := NewContextBuilder(home, nil, "")
	cb.store = NewMemoryStoreAdapter(db)
	cb.agentID = "agt_hb"
	cb.userID = owner.ID

	ag := &Agent{ctxBuilder: cb, ownerUserID: owner.ID, homePath: home}
	return &Heartbeat{agent: ag}, cb, owner.ID
}

// The store row is what the prompt shows, so it is what the tick must read —
// even when the pod's disk holds a different file (the shape that caused G14).
func TestHeartbeatReadsWhatThePromptShows(t *testing.T) {
	ctx := context.Background()
	hb, cb, owner := heartbeatFixture(t)

	if err := cb.store.SaveWorkspaceFile(ctx, "agt_hb", owner, "HEARTBEAT.md",
		[]byte("STORE TASKS")); err != nil {
		t.Fatalf("seed store HEARTBEAT.md: %v", err)
	}
	// The pod-local copy differs — exactly the divergence that used to decide
	// which file fired.
	if err := os.WriteFile(filepath.Join(hb.agent.home(), "HEARTBEAT.md"), []byte("DISK TASKS"), 0o644); err != nil {
		t.Fatalf("seed disk HEARTBEAT.md: %v", err)
	}

	tick := hb.loadHeartbeatTasks()
	prompt := cb.loadFileForUser("HEARTBEAT.md", owner)
	if tick != prompt {
		t.Fatalf("the tick reads %q while the prompt shows %q — the two sources are back", tick, prompt)
	}
	if tick != "STORE TASKS" {
		t.Fatalf("tick read %q; want the store row the prompt shows", tick)
	}
}

// No store row (fresh agent, or a CLI install with no relational store): the
// on-disk copy is the only file there is, and it must still drive the tick.
func TestHeartbeatFallsBackToTheDiskCopy(t *testing.T) {
	hb, cb, owner := heartbeatFixture(t)
	if err := os.WriteFile(filepath.Join(hb.agent.home(), "HEARTBEAT.md"), []byte("DISK ONLY"), 0o644); err != nil {
		t.Fatalf("seed disk HEARTBEAT.md: %v", err)
	}
	if got := hb.loadHeartbeatTasks(); got != "DISK ONLY" {
		t.Fatalf("tick read %q; want the disk copy", got)
	}
	if prompt := cb.loadFileForUser("HEARTBEAT.md", owner); prompt != "DISK ONLY" {
		t.Fatalf("prompt shows %q; want the same disk copy", prompt)
	}
}

// Neither source: the tick sends no turn at all (empty string short-circuits it).
func TestHeartbeatWithNoFileSendsNothing(t *testing.T) {
	hb, _, _ := heartbeatFixture(t)
	if got := hb.loadHeartbeatTasks(); got != "" {
		t.Fatalf("tick read %q; want empty", got)
	}
}
