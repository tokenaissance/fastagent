package sandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// TestE2BPoolCrossPodAdoption verifies the cross-pod lease end to end with a
// real e2b account: pool A creates the sandbox and registers the lease; pool
// B (a second "replica" sharing the same DB) adopts the same sandbox_id
// instead of creating a duplicate. Requires live e2b credentials.
func TestE2BPoolCrossPodAdoption(t *testing.T) {
	apiKey := os.Getenv("E2B_API_KEY")
	template := os.Getenv("E2B_TEMPLATE")
	if apiKey == "" || template == "" {
		// In CI this test must never pass silently: a green run without
		// credentials would be read as "cross-pod adoption verified".
		// Locally it stays skippable for contributor ergonomics.
		if os.Getenv("CI") != "" {
			t.Fatal("TestE2BPoolCrossPodAdoption requires E2B_API_KEY and E2B_TEMPLATE in CI")
		}
		t.Skip("set E2B_API_KEY and E2B_TEMPLATE env vars to run the live cross-pod adoption test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	db, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "leases-e2e.db")+"?cache=shared")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	newPool := func(owner string) *sandbox.E2BExecutorPool {
		return sandbox.NewE2BExecutorPool(apiKey, template, "", 5*time.Minute,
			sandbox.WithSandboxLeases(sandbox.E2BLeaseOptions{
				Store:    db,
				Owner:    owner,
				LeaseTTL: time.Minute,
			}))
	}
	poolA := newPool("pod-a")
	poolB := newPool("pod-b")
	defer func() {
		poolB.CloseAll()
		poolA.CloseAll()
	}()

	// Replica A creates + registers.
	exA, err := poolA.Get(ctx, "agt-e2e", "", "sess-e2e")
	if err != nil {
		t.Fatalf("pool A Get: %v", err)
	}
	rec1, err := db.GetSandboxLease(ctx, "agt-e2e:s:sess-e2e")
	if err != nil || rec1 == nil {
		t.Fatalf("lease after A create: rec=%+v err=%v", rec1, err)
	}
	if _, err := exA.Exec(ctx, "echo replica-a-ok", 30*time.Second); err != nil {
		t.Fatalf("pool A exec: %v", err)
	}

	// Replica B adopts the same sandbox; the lease sandbox_id must not change
	// and the adopted sandbox must be usable.
	exB, err := poolB.Get(ctx, "agt-e2e", "", "sess-e2e")
	if err != nil {
		t.Fatalf("pool B Get: %v", err)
	}
	rec2, err := db.GetSandboxLease(ctx, "agt-e2e:s:sess-e2e")
	if err != nil || rec2 == nil {
		t.Fatalf("lease after B adopt: rec=%+v err=%v", rec2, err)
	}
	if rec2.SandboxID != rec1.SandboxID {
		t.Fatalf("pool B created a second sandbox: before=%s after=%s", rec1.SandboxID, rec2.SandboxID)
	}
	if rec2.Epoch <= rec1.Epoch {
		t.Fatalf("adoption should bump epoch: before=%d after=%d", rec1.Epoch, rec2.Epoch)
	}
	if _, err := exB.Exec(ctx, "echo replica-b-ok", 30*time.Second); err != nil {
		t.Fatalf("pool B exec on adopted sandbox: %v", err)
	}

	// Eviction fencing across pods: pool A still holds its original epoch,
	// which B's renew bumped. A's Release must therefore NOT delete the
	// lease or destroy the sandbox B is actively using.
	if err := poolA.Release("agt-e2e", "", "sess-e2e"); err != nil {
		t.Fatalf("pool A release: %v", err)
	}
	rec3, err := db.GetSandboxLease(ctx, "agt-e2e:s:sess-e2e")
	if err != nil || rec3 == nil {
		t.Fatalf("lease missing after stale pod-A release: rec=%+v err=%v", rec3, err)
	}
	if rec3.SandboxID != rec1.SandboxID {
		t.Fatalf("lease sandbox changed after stale release: %s -> %s", rec1.SandboxID, rec3.SandboxID)
	}
	if _, err := exB.Exec(ctx, "echo replica-b-still-ok", 30*time.Second); err != nil {
		t.Fatalf("pool B exec after pod-A stale release: %v", err)
	}
}
