package sandbox_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// newE2ELeaseStores returns two independent store handles (one per "pod")
// over the same underlying lease table. Postgres when
// FASTAGENT_TEST_PG_DSN is set, otherwise a shared sqlite file with two
// connections. Separate handles matter: in production each pod opens its
// own connection pool to the shared database.
func newE2ELeaseStores(t *testing.T, ctx context.Context) (dbA, dbB *store.DBStore) {
	t.Helper()
	newStore := func(dialect, dsn string) *store.DBStore {
		t.Helper()
		db, err := store.NewDBStore(dialect, dsn)
		if err != nil {
			t.Fatalf("NewDBStore(%s): %v", dialect, err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := db.Migrate(ctx); err != nil {
			t.Fatalf("Migrate(%s): %v", dialect, err)
		}
		return db
	}
	if dsn := os.Getenv("FASTAGENT_TEST_PG_DSN"); dsn != "" {
		return newStore("postgres", dsn), newStore("postgres", dsn)
	}
	fileDSN := "file:" + filepath.Join(t.TempDir(), "leases-e2e.db") + "?cache=shared"
	return newStore("sqlite", fileDSN), newStore("sqlite", fileDSN)
}

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

	dbA, dbB := newE2ELeaseStores(t, ctx)

	newPool := func(owner string, db *store.DBStore) *sandbox.E2BExecutorPool {
		return sandbox.NewE2BExecutorPool(apiKey, template, "", 5*time.Minute,
			sandbox.WithSandboxLeases(sandbox.E2BLeaseOptions{
				Store:    db,
				Owner:    owner,
				LeaseTTL: time.Minute,
			}))
	}
	poolA := newPool("pod-a", dbA)
	poolB := newPool("pod-b", dbB)
	defer func() {
		poolB.CloseAll()
		poolA.CloseAll()
	}()

	// Replica A creates + registers.
	exA, err := poolA.Get(ctx, "agt-e2e", "", "sess-e2e")
	if err != nil {
		t.Fatalf("pool A Get: %v", err)
	}
	rec1, err := dbA.GetSandboxLease(ctx, "agt-e2e:s:sess-e2e")
	if err != nil || rec1 == nil {
		t.Fatalf("lease after A create: rec=%+v err=%v", rec1, err)
	}
	// Pod A writes a marker into the shared /workspace. If pod B truly
	// adopts the SAME e2b instance (rather than creating a second sandbox
	// with the same lease row), B must be able to read the marker back.
	marker := fmt.Sprintf("lease-e2e-marker-%d", time.Now().UnixNano())
	writeCmd := fmt.Sprintf("printf '%%s' %q > /workspace/lease-e2e-marker && cat /workspace/lease-e2e-marker", marker)
	outA, err := exA.Exec(ctx, writeCmd, 30*time.Second)
	if err != nil {
		t.Fatalf("pool A exec: %v", err)
	}
	if !strings.Contains(outA, marker) {
		t.Fatalf("pod A marker write not confirmed: out=%q marker=%q", outA, marker)
	}

	// Replica B adopts the same sandbox; the lease sandbox_id must not change
	// and the adopted sandbox must be usable.
	exB, err := poolB.Get(ctx, "agt-e2e", "", "sess-e2e")
	if err != nil {
		t.Fatalf("pool B Get: %v", err)
	}
	rec2, err := dbA.GetSandboxLease(ctx, "agt-e2e:s:sess-e2e")
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
	outB, err := exB.Exec(ctx, "cat /workspace/lease-e2e-marker", 30*time.Second)
	if err != nil || !strings.Contains(outB, marker) {
		t.Fatalf("pod B could not read pod A's marker (different instance?): out=%q err=%v", outB, err)
	}

	// Eviction fencing across pods: pool A still holds its original epoch,
	// which B's renew bumped. A's Release must therefore NOT delete the
	// lease or destroy the sandbox B is actively using.
	if err := poolA.Release("agt-e2e", "", "sess-e2e"); err != nil {
		t.Fatalf("pool A release: %v", err)
	}
	rec3, err := dbA.GetSandboxLease(ctx, "agt-e2e:s:sess-e2e")
	if err != nil || rec3 == nil {
		t.Fatalf("lease missing after stale pod-A release: rec=%+v err=%v", rec3, err)
	}
	if rec3.SandboxID != rec1.SandboxID {
		t.Fatalf("lease sandbox changed after stale release: %s -> %s", rec1.SandboxID, rec3.SandboxID)
	}
	if _, err := exB.Exec(ctx, "echo replica-b-still-ok", 30*time.Second); err != nil {
		t.Fatalf("pool B exec after pod-A stale release: %v", err)
	}
	outC, err := exB.Exec(ctx, "cat /workspace/lease-e2e-marker", 30*time.Second)
	if err != nil || !strings.Contains(outC, marker) {
		t.Fatalf("marker lost after stale pod-A release: out=%q err=%v", outC, err)
	}
}
