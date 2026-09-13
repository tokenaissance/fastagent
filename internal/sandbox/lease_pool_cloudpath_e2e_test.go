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

// TestE2BPoolCrossPodRebuild covers the rebuild half of the lease contract
// against a real e2b account. A sandbox that dies must be replaced in place,
// and the lease — not the dead instance — is what every replica uses after
// that. Without the fix, the row kept the dead sandbox_id and the next
// reconcile read its own stale row as a takeover: it adopted the corpse back
// and closed the healthy replacement, once per call, forever.
//
// The death is staged by destroying the instance out of band while the pool
// still holds its handle; e2b answers envd for a destroyed sandbox with
// `502 sandbox not found`, which is the same signal a provider-side timeout
// produces in production.
func TestE2BPoolCrossPodRebuild(t *testing.T) {
	apiKey := os.Getenv("E2B_API_KEY")
	template := os.Getenv("E2B_TEMPLATE")
	if apiKey == "" || template == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TestE2BPoolCrossPodRebuild requires E2B_API_KEY and E2B_TEMPLATE in CI")
		}
		t.Skip("set E2B_API_KEY and E2B_TEMPLATE env vars to run the live rebuild e2e")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	dbA, dbB := newE2ELeaseStores(t, ctx)
	newPool := func(owner string, db *store.DBStore) *sandbox.E2BExecutorPool {
		return sandbox.NewE2BExecutorPool(apiKey, template, "", 10*time.Minute,
			sandbox.WithSandboxLeases(sandbox.E2BLeaseOptions{
				Store:    db,
				Owner:    owner,
				LeaseTTL: 5 * time.Minute,
			}))
	}
	poolA := newPool("pod-a", dbA)
	poolB := newPool("pod-b", dbB)
	defer func() {
		poolB.CloseAll()
		poolA.CloseAll()
	}()

	const scope = "agt-rebuild:s:sess-rebuild"
	exAAny, err := poolA.Get(ctx, "agt-rebuild", "", "sess-rebuild")
	if err != nil {
		t.Fatalf("pool A Get: %v", err)
	}
	exA := exAAny.(*sandbox.E2BExecutor)
	rec1, err := dbA.GetSandboxLease(ctx, scope)
	if err != nil || rec1 == nil {
		t.Fatalf("lease after A create: rec=%+v err=%v", rec1, err)
	}

	// Destroy the instance behind the pool's back: the handle stays cached,
	// so the next exec is what discovers the loss.
	if err := exA.Close(); err != nil {
		t.Fatalf("destroy sandbox %s: %v", rec1.SandboxID, err)
	}
	if _, err := exA.Exec(ctx, "echo rebuilt-ok", 60*time.Second); err != nil {
		t.Fatalf("exec after sandbox loss (expected a transparent rebuild): %v", err)
	}

	// The next Get is where the pool republishes the replacement.
	if _, err := poolA.Get(ctx, "agt-rebuild", "", "sess-rebuild"); err != nil {
		t.Fatalf("pool A Get after rebuild: %v", err)
	}
	rec2, err := dbA.GetSandboxLease(ctx, scope)
	if err != nil || rec2 == nil {
		t.Fatalf("lease after rebuild: rec=%+v err=%v", rec2, err)
	}
	if rec2.SandboxID == rec1.SandboxID {
		t.Fatalf("lease still names the destroyed sandbox %s: the rebuild was never published", rec1.SandboxID)
	}

	// Write a marker into the rebuilt instance, then serve again — the lease
	// must keep pointing at that same instance instead of minting a new one
	// per call.
	marker := fmt.Sprintf("rebuild-e2e-marker-%d", time.Now().UnixNano())
	writeCmd := fmt.Sprintf("printf '%%s' %q > /workspace/rebuild-e2e-marker && cat /workspace/rebuild-e2e-marker", marker)
	if out, err := exA.Exec(ctx, writeCmd, 30*time.Second); err != nil || !strings.Contains(out, marker) {
		t.Fatalf("marker write after rebuild: out=%q err=%v", out, err)
	}
	if _, err := poolA.Get(ctx, "agt-rebuild", "", "sess-rebuild"); err != nil {
		t.Fatalf("pool A Get (reuse): %v", err)
	}
	rec3, err := dbA.GetSandboxLease(ctx, scope)
	if err != nil || rec3 == nil {
		t.Fatalf("lease after reuse: rec=%+v err=%v", rec3, err)
	}
	if rec3.SandboxID != rec2.SandboxID {
		t.Fatalf("pool mints a new sandbox per call: %s -> %s", rec2.SandboxID, rec3.SandboxID)
	}

	// Replica B adopts the rebuilt instance (not the destroyed one) and must
	// read the marker A wrote after its rebuild — same sandbox, no duplicate.
	exBAny, err := poolB.Get(ctx, "agt-rebuild", "", "sess-rebuild")
	if err != nil {
		t.Fatalf("pool B Get: %v", err)
	}
	exB := exBAny.(*sandbox.E2BExecutor)
	outB, err := exB.Exec(ctx, "cat /workspace/rebuild-e2e-marker", 30*time.Second)
	if err != nil || !strings.Contains(outB, marker) {
		t.Fatalf("pod B is not on pod A's rebuilt sandbox: out=%q err=%v", outB, err)
	}
	rec4, err := dbB.GetSandboxLease(ctx, scope)
	if err != nil || rec4 == nil {
		t.Fatalf("lease after B adopt: rec=%+v err=%v", rec4, err)
	}
	if rec4.SandboxID != rec2.SandboxID {
		t.Fatalf("adoption moved the lease to a different sandbox: %s -> %s", rec2.SandboxID, rec4.SandboxID)
	}
}
