package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func openEpochStore(t *testing.T) *store.DBStore {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "epoch.db")
	st, err := store.New(&store.StorageConfig{Type: "sqlite", DSN: dsn, AutoMigrate: true}, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	db, ok := st.(*store.DBStore)
	if !ok {
		t.Fatalf("store is %T, want *DBStore", st)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// A system-scope save cannot name a user, so it rides the same table under a
// reserved key. Replicas must see it exactly once, like any user's marker —
// this is the signal that replaces "wait for the 30-minute idle eviction".
func TestSystemReloadMarkerCrossesInstances(t *testing.T) {
	st := openEpochStore(t)
	a := newAgentReloadEpochs(st)
	b := newAgentReloadEpochs(st)
	ctx := context.Background()

	if err := a.Bump(ctx, systemReloadKey); err != nil {
		t.Fatalf("bump system marker: %v", err)
	}
	if changed, _ := a.Poll(ctx); len(changed) != 0 {
		t.Fatal("the writer must not react to its own marker")
	}
	changed, err := b.Poll(ctx)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(changed) != 1 || changed[0] != systemReloadKey {
		t.Fatalf("replica B changed = %v, want [%s]", changed, systemReloadKey)
	}
	if changed, _ := b.Poll(ctx); len(changed) != 0 {
		t.Fatal("replica B must not act on the marker twice")
	}
	// And it stays independent of the per-user markers.
	if err := a.Bump(ctx, "u1"); err != nil {
		t.Fatalf("bump user marker: %v", err)
	}
	changed, _ = b.Poll(ctx)
	if len(changed) != 1 || changed[0] != "u1" {
		t.Fatalf("replica B changed = %v, want [u1]", changed)
	}
}

// TestAgentReloadEpochsPerUserCrossInstance simulates two gateway
// replicas sharing one store: replica A bumps user u1, replica B
// notices exactly u1 on its next poll, and neither reloads twice.
func TestAgentReloadEpochsPerUserCrossInstance(t *testing.T) {
	st := openEpochStore(t)
	a := newAgentReloadEpochs(st)
	b := newAgentReloadEpochs(st)
	ctx := context.Background()

	if changed, _ := a.Poll(ctx); len(changed) != 0 {
		t.Fatal("fresh instance should not see a change")
	}
	if changed, _ := b.Poll(ctx); len(changed) != 0 {
		t.Fatal("fresh instance should not see a change")
	}

	if err := a.Bump(ctx, "u1"); err != nil {
		t.Fatalf("bump: %v", err)
	}
	// Writer marked its own bump as seen.
	if changed, _ := a.Poll(ctx); len(changed) != 0 {
		t.Fatal("writer must not reload its own bump")
	}
	// Replica B notices exactly u1 once.
	changed, _ := b.Poll(ctx)
	if len(changed) != 1 || changed[0] != "u1" {
		t.Fatalf("replica B changed = %v, want [u1]", changed)
	}
	if changed, _ := b.Poll(ctx); len(changed) != 0 {
		t.Fatal("replica B must not reload twice")
	}

	// A different user's bump does not touch u1.
	if err := a.Bump(ctx, "u2"); err != nil {
		t.Fatalf("bump u2: %v", err)
	}
	changed, _ = b.Poll(ctx)
	if len(changed) != 1 || changed[0] != "u2" {
		t.Fatalf("replica B changed = %v, want [u2] only", changed)
	}
}

func TestAgentReloadEpochsSeedSkipsOldValue(t *testing.T) {
	st := openEpochStore(t)
	old := newAgentReloadEpochs(st)
	if err := old.Bump(context.Background(), "u1"); err != nil {
		t.Fatalf("bump: %v", err)
	}
	// A brand-new instance (restart) seeds last=current → no reload.
	restarted := newAgentReloadEpochs(st)
	if changed, _ := restarted.Poll(context.Background()); len(changed) != 0 {
		t.Fatal("restart must not reload an old epoch")
	}
	// A watcher that was alive BEFORE the bump notices it exactly once.
	watcher := newAgentReloadEpochs(st)
	if changed, _ := watcher.Poll(context.Background()); len(changed) != 0 {
		t.Fatal("watcher fresh boot should not reload")
	}
	restarted.now = func() time.Time { return time.Now().Add(time.Hour) }
	if err := restarted.Bump(context.Background(), "u1"); err != nil {
		t.Fatalf("bump2: %v", err)
	}
	changed, _ := watcher.Poll(context.Background())
	if len(changed) != 1 || changed[0] != "u1" {
		t.Fatalf("changed = %v, want [u1]", changed)
	}
}
