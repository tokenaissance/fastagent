package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

func TestSandboxLeasesAcquireAdoptRenewRelease(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	var st sandbox.SandboxLeaseStore = db

	const scope = "agt_1:s:sess_1"
	ttl := time.Minute

	// Pod A creates the sandbox and wins the lease.
	rec, acquired, err := st.AcquireSandboxLease(ctx, scope, "pod-a", "sb-a", "token-a", "fastclaw-sandbox", ttl)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !acquired || rec == nil || rec.SandboxID != "sb-a" {
		t.Fatalf("first acquire: acquired=%v rec=%+v", acquired, rec)
	}

	// Pod B asks to create its own sandbox for the same scope; the lease is
	// still valid, so B loses the race and must adopt A's instance.
	rec, acquired, err = st.AcquireSandboxLease(ctx, scope, "pod-b", "sb-b", "token-b", "fastclaw-sandbox", ttl)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if acquired || rec == nil || rec.SandboxID != "sb-a" {
		t.Fatalf("second acquire should adopt sb-a: acquired=%v rec=%+v", acquired, rec)
	}

	// Pod B renews (adopts ownership) while continuing to use A's instance.
	if err := st.RenewSandboxLease(ctx, scope, "pod-b", ttl); err != nil {
		t.Fatalf("renew: %v", err)
	}

	// A's release must NOT delete the lease (B owns it now) → A must not
	// destroy the shared sandbox.
	deleted, err := st.ReleaseSandboxLease(ctx, scope, "pod-a")
	if err != nil {
		t.Fatalf("release pod-a: %v", err)
	}
	if deleted {
		t.Fatal("pod-a release deleted a lease owned by pod-b")
	}

	// B's release deletes the lease → B may destroy the sandbox.
	deleted, err = st.ReleaseSandboxLease(ctx, scope, "pod-b")
	if err != nil {
		t.Fatalf("release pod-b: %v", err)
	}
	if !deleted {
		t.Fatal("pod-b release did not delete its own lease")
	}
	if rec, _ := st.GetSandboxLease(ctx, scope); rec != nil {
		t.Fatalf("lease still present after release: %+v", rec)
	}
}

func TestSandboxLeaseExpiryAllowsTakeover(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	var st sandbox.SandboxLeaseStore = db

	const scope = "agt_2:s:sess_2"
	// Acquire with a zero TTL that still maps to a 1-second lease (store
	// enforces minimum), then wait for expiry.
	if _, acquired, err := st.AcquireSandboxLease(ctx, scope, "pod-a", "sb-old", "token-old", "tpl", time.Duration(0)); err != nil || !acquired {
		t.Fatalf("acquire: %v acquired=%v", err, acquired)
	}
	if rec, err := st.GetSandboxLease(ctx, scope); err != nil || rec == nil {
		t.Fatalf("expected live lease: rec=%+v err=%v", rec, err)
	}

	time.Sleep(1100 * time.Millisecond)
	if rec, err := st.GetSandboxLease(ctx, scope); err != nil || rec != nil {
		t.Fatalf("expired lease still returned: rec=%+v err=%v", rec, err)
	}

	// Expired → pod B can take over with a brand-new sandbox.
	rec, acquired, err := st.AcquireSandboxLease(ctx, scope, "pod-b", "sb-new", "token-new", "tpl", time.Minute)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if !acquired || rec == nil || rec.SandboxID != "sb-new" {
		t.Fatalf("takeover failed: acquired=%v rec=%+v", acquired, rec)
	}
}

func newTestSandboxLeaseDB(t *testing.T) *DBStore {
	t.Helper()
	db, err := NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "leases.db")+"?cache=shared")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}
