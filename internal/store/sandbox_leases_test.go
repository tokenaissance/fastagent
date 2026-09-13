package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
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
	if !acquired || rec == nil || rec.SandboxID != "sb-a" || rec.Epoch != 1 {
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
	epoch, err := st.RenewSandboxLease(ctx, scope, "pod-b", "sb-a", ttl)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if epoch <= rec.Epoch {
		t.Fatalf("renew epoch = %d, want > %d", epoch, rec.Epoch)
	}

	// A's release must NOT delete the lease (B owns it now) → A must not
	// destroy the shared sandbox.
	deleted, err := st.ReleaseSandboxLease(ctx, scope, "pod-a", rec.Epoch)
	if err != nil {
		t.Fatalf("release pod-a: %v", err)
	}
	if deleted {
		t.Fatal("pod-a release deleted a lease owned by pod-b")
	}

	// B's release deletes the lease → B may destroy the sandbox.
	deleted, err = st.ReleaseSandboxLease(ctx, scope, "pod-b", epoch)
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

func TestSandboxLeaseStaleEpochCannotRelease(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	var st sandbox.SandboxLeaseStore = db

	const scope = "agt_3:s:sess_3"
	rec, acquired, err := st.AcquireSandboxLease(ctx, scope, "pod-a", "sb-a", "tok-a", "tpl", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: %v acquired=%v", err, acquired)
	}
	// A renews (bumps epoch) and then a delayed eviction from its older
	// snapshot tries to release with the stale epoch.
	newEpoch, err := st.RenewSandboxLease(ctx, scope, "pod-a", "sb-a", time.Minute)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if newEpoch <= rec.Epoch {
		t.Fatalf("epoch did not advance: old=%d new=%d", rec.Epoch, newEpoch)
	}
	deleted, err := st.ReleaseSandboxLease(ctx, scope, "pod-a", rec.Epoch)
	if err != nil {
		t.Fatalf("stale release: %v", err)
	}
	if deleted {
		t.Fatal("stale epoch release deleted a renewed lease")
	}
	// The current epoch can still release.
	deleted, err = st.ReleaseSandboxLease(ctx, scope, "pod-a", newEpoch)
	if err != nil || !deleted {
		t.Fatalf("current-epoch release: deleted=%v err=%v", deleted, err)
	}
}

func TestSandboxLeaseConcurrentAcquireSingleWinner(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	var st sandbox.SandboxLeaseStore = db

	const scope = "agt_4:s:sess_4"
	const racers = 6
	start := make(chan struct{})
	type result struct {
		acquired bool
		err      error
	}
	results := make(chan result, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, acquired, err := st.AcquireSandboxLease(
				ctx, scope, fmt.Sprintf("pod-%d", i), fmt.Sprintf("sb-%d", i),
				fmt.Sprintf("tok-%d", i), "tpl", time.Minute)
			results <- result{acquired: acquired, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	winners := 0
	for r := range results {
		if r.err != nil {
			t.Fatalf("acquire error: %v", r.err)
		}
		if r.acquired {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
	rec, err := st.GetSandboxLease(ctx, scope)
	if err != nil || rec == nil {
		t.Fatalf("final lease missing: rec=%+v err=%v", rec, err)
	}
	if rec.Epoch != 1 {
		t.Fatalf("winner epoch = %d, want 1", rec.Epoch)
	}
}

// TestSandboxLeaseRenewCASMissReturnsZero: renew must only succeed when the
// row still points at the same sandbox_id and has not expired. A renew for a
// sandbox_id that never held the lease returns epoch 0 and the caller must
// not treat it as ownership.
func TestSandboxLeaseRenewCASMissReturnsZero(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	var st sandbox.SandboxLeaseStore = db

	const scope = "agt_cas:s:sess_cas"
	rec, acquired, err := st.AcquireSandboxLease(
		ctx, scope, "pod-a", "sb-a", "tok-a", "tpl", time.Minute)
	if err != nil || !acquired || rec.Epoch != 1 {
		t.Fatalf("acquire: rec=%+v acquired=%v err=%v", rec, acquired, err)
	}
	// Wrong sandbox_id → CAS miss.
	epoch, err := st.RenewSandboxLease(ctx, scope, "pod-a", "sb-other", time.Minute)
	if err != nil || epoch != 0 {
		t.Fatalf("wrong-sandbox renew: epoch=%d err=%v, want 0/nil", epoch, err)
	}
	// Correct sandbox_id still renews afterwards (row untouched by the miss).
	epoch, err = st.RenewSandboxLease(ctx, scope, "pod-a", "sb-a", time.Minute)
	if err != nil || epoch <= rec.Epoch {
		t.Fatalf("correct renew: epoch=%d err=%v, want > %d", epoch, err, rec.Epoch)
	}
}

// TestSandboxLeaseReleaseStaleOrMissingRowIsSafe: a release that does not
// match owner+epoch must return false without error, and releasing a scope
// that has no lease must also be a no-op.
func TestSandboxLeaseReleaseStaleOrMissingRowIsSafe(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	var st sandbox.SandboxLeaseStore = db

	const scope = "agt_rel:s:sess_rel"
	if deleted, err := st.ReleaseSandboxLease(ctx, scope, "pod-a", 1); err != nil || deleted {
		t.Fatalf("release missing row: deleted=%v err=%v, want false/nil", deleted, err)
	}

	rec, _, err := st.AcquireSandboxLease(
		ctx, scope, "pod-a", "sb-a", "tok-a", "tpl", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// Stale epoch (never granted) cannot delete.
	if deleted, err := st.ReleaseSandboxLease(ctx, scope, "pod-a", rec.Epoch+99); err != nil || deleted {
		t.Fatalf("stale-epoch release: deleted=%v err=%v, want false/nil", deleted, err)
	}
	// Correct owner+epoch deletes exactly once; a second delete is a no-op.
	if deleted, err := st.ReleaseSandboxLease(ctx, scope, "pod-a", rec.Epoch); err != nil || !deleted {
		t.Fatalf("owner release: deleted=%v err=%v, want true/nil", deleted, err)
	}
	if deleted, err := st.ReleaseSandboxLease(ctx, scope, "pod-a", rec.Epoch); err != nil || deleted {
		t.Fatalf("double release: deleted=%v err=%v, want false/nil", deleted, err)
	}
}

// TestSandboxLeaseRenewEpochMonotonic: every successful renew bumps the
// fencing epoch, and release only succeeds with the latest one.
func TestSandboxLeaseRenewEpochMonotonic(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	var st sandbox.SandboxLeaseStore = db

	const scope = "agt_mono:s:sess_mono"
	rec, _, err := st.AcquireSandboxLease(
		ctx, scope, "pod-a", "sb-a", "tok-a", "tpl", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	prev := rec.Epoch
	for i := 0; i < 3; i++ {
		epoch, err := st.RenewSandboxLease(ctx, scope, "pod-b", "sb-a", time.Minute)
		if err != nil || epoch <= prev {
			t.Fatalf("renew %d: epoch=%d err=%v, want > %d", i, epoch, err, prev)
		}
		prev = epoch
	}
	if deleted, err := st.ReleaseSandboxLease(ctx, scope, "pod-b", prev); err != nil || !deleted {
		t.Fatalf("latest-epoch release: deleted=%v err=%v", deleted, err)
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
