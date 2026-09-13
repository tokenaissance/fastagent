package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// newTestSandboxLeasePostgresDB connects to a real Postgres instance.
// The test is skipped unless FASTAGENT_TEST_PG_DSN is set, e.g.
// "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable".
// sqlite serializes writes, so the production claim that the lease table
// supports concurrent cross-pod acquire without duplicates can only be
// proven against Postgres (EvalPlanQual semantics).
func newTestSandboxLeasePostgresDB(t *testing.T) *DBStore {
	t.Helper()
	dsn := os.Getenv("FASTAGENT_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set FASTAGENT_TEST_PG_DSN to run the Postgres sandbox-lease concurrency test")
	}
	db, err := NewDBStore("postgres", dsn)
	if err != nil {
		t.Fatalf("NewDBStore(postgres): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate(postgres): %v", err)
	}
	return db
}

// TestSandboxLeaseConcurrentAcquireSingleWinnerPostgres races many
// acquirers against one scope on a real Postgres database. Exactly one
// pod must win; every loser must observe the winner's sandbox_id (adopt),
// and the stored epoch must stay 1.
func TestSandboxLeaseConcurrentAcquireSingleWinnerPostgres(t *testing.T) {
	db := newTestSandboxLeasePostgresDB(t)
	var st sandbox.SandboxLeaseStore = db

	const scope = "agt_pg:s:sess_pg"
	const racers = 12
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := make(chan struct{})
	type result struct {
		record   *sandbox.SandboxLeaseRecord
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
			rec, acquired, err := st.AcquireSandboxLease(
				ctx, scope, fmt.Sprintf("pod-%d", i), fmt.Sprintf("sb-%d", i),
				fmt.Sprintf("tok-%d", i), "tpl", time.Minute)
			results <- result{record: rec, acquired: acquired, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	var winners []string
	var loserSandboxes = map[string]bool{}
	for r := range results {
		if r.err != nil {
			t.Fatalf("acquire error: %v", r.err)
		}
		if r.record == nil {
			t.Fatal("acquire returned nil record")
		}
		if r.acquired {
			winners = append(winners, r.record.SandboxID)
		}
		loserSandboxes[r.record.SandboxID] = true
	}
	if len(winners) != 1 {
		t.Fatalf("winners = %v, want exactly 1", winners)
	}
	if len(loserSandboxes) != 1 {
		t.Fatalf("losers observed %d distinct sandbox_ids, want 1 (the winner's)", len(loserSandboxes))
	}
	rec, err := st.GetSandboxLease(ctx, scope)
	if err != nil || rec == nil {
		t.Fatalf("final lease missing: rec=%+v err=%v", rec, err)
	}
	if rec.Epoch != 1 {
		t.Fatalf("winner epoch = %d, want 1", rec.Epoch)
	}
}

// TestSandboxLeaseStaleReleaseCannotDeletePostgres verifies the fencing
// guarantee on Postgres: after ownership moves to pod B, pod A's release
// with its old epoch must not delete the lease.
func TestSandboxLeaseStaleReleaseCannotDeletePostgres(t *testing.T) {
	db := newTestSandboxLeasePostgresDB(t)
	var st sandbox.SandboxLeaseStore = db
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const scope = "agt_pg_stale:s:sess_stale"
	recA, acquired, err := st.AcquireSandboxLease(
		ctx, scope, "pod-a", "sb-a", "tok-a", "tpl", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("pod-a acquire: acquired=%v err=%v", acquired, err)
	}
	epochB, err := st.RenewSandboxLease(ctx, scope, "pod-b", recA.SandboxID, time.Minute)
	if err != nil || epochB == 0 {
		t.Fatalf("pod-b renew: epoch=%d err=%v", epochB, err)
	}
	deleted, err := st.ReleaseSandboxLease(ctx, scope, "pod-a", recA.Epoch)
	if err != nil {
		t.Fatalf("pod-a stale release: %v", err)
	}
	if deleted {
		t.Fatal("stale pod-a release deleted a lease owned by pod-b")
	}
	deleted, err = st.ReleaseSandboxLease(ctx, scope, "pod-b", epochB)
	if err != nil || !deleted {
		t.Fatalf("pod-b release: deleted=%v err=%v", deleted, err)
	}
}

// TestSandboxLeaseMigrateIdempotentPostgres guards the "no manual migration
// needed" rollout claim on the production dialect: Migrate must be safe to
// run more than once.
func TestSandboxLeaseMigrateIdempotentPostgres(t *testing.T) {
	db := newTestSandboxLeasePostgresDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	var st sandbox.SandboxLeaseStore = db
	if _, err := st.GetSandboxLease(ctx, "agt_pg_idem:s:sess_idem"); err != nil {
		t.Fatalf("lease table unusable after second Migrate: %v", err)
	}
}

// TestSandboxLeaseStatePostgres runs the running/paused write path on the
// production dialect. The sqlite versions of these assertions cannot stand in:
// the retrofit reads information_schema there, the ALTER runs through a
// different planner, and the placeholder style differs. Production is Postgres,
// so this is where the new columns have to be proven.
func TestSandboxLeaseStatePostgres(t *testing.T) {
	db := newTestSandboxLeasePostgresDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var st sandbox.SandboxLeaseStore = db
	scope := fmt.Sprintf("agt_pg_state:s:sess_%d", time.Now().UnixNano())
	ttl := time.Minute

	rec, acquired, err := st.AcquireSandboxLease(ctx, scope, "pod-a", "sb-a", "tok-a", "tpl", ttl)
	if err != nil || !acquired {
		t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
	}
	if rec.State != "running" || rec.PausedAt != 0 {
		t.Fatalf("fresh lease = %q/%d, want running/0", rec.State, rec.PausedAt)
	}

	pausedAt := time.Now().Unix()
	if err := st.SetSandboxLeaseState(ctx, scope, "pod-a", "paused"); err != nil {
		t.Fatalf("pause annotation: %v", err)
	}
	got, err := st.GetSandboxLease(ctx, scope)
	if err != nil || got == nil {
		t.Fatalf("GetSandboxLease: rec=%+v err=%v", got, err)
	}
	if got.State != "paused" || got.PausedAt < pausedAt {
		t.Fatalf("state = %q/%d, want paused with a timestamp", got.State, got.PausedAt)
	}
	if err := st.SetSandboxLeaseState(ctx, scope, "pod-b", "running"); err != nil {
		t.Fatalf("foreign annotation: %v", err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); got.State != "paused" {
		t.Fatalf("a foreign owner changed the state to %q", got.State)
	}

	// Replace (a rebuild) resets the annotation: the row now describes a new
	// instance.
	if _, err := st.ReplaceSandboxLease(ctx, scope, "pod-a", "sb-b", "tok-b", "tpl", ttl); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); got.State != "running" || got.PausedAt != 0 {
		t.Fatalf("state after replace = %q/%d, want running/0", got.State, got.PausedAt)
	}
}

// The retrofit has to work on a table that predates the columns — production is
// exactly that table.
func TestSandboxLeaseStateMigrationPostgres(t *testing.T) {
	db := newTestSandboxLeasePostgresDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Rebuild the table in its pre-state shape, so the retrofit runs against a
	// real pre-existing table rather than a fresh one that already has the
	// columns (which would only exercise the early return).
	if _, err := db.db.ExecContext(ctx, `DROP TABLE sandbox_leases`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.db.ExecContext(ctx, `CREATE TABLE sandbox_leases (
		scope_key TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		sandbox_id TEXT NOT NULL,
		envd_token TEXT NOT NULL,
		template TEXT NOT NULL DEFAULT '',
		expires_at BIGINT NOT NULL,
		epoch BIGINT NOT NULL DEFAULT 0,
		updated_at BIGINT NOT NULL
	)`); err != nil {
		t.Fatalf("create old shape: %v", err)
	}
	for _, col := range []string{"state", "paused_at"} {
		if has, err := db.tableHasColumn(ctx, "sandbox_leases", col); err != nil || has {
			t.Fatalf("pre-state table unexpectedly has %s: has=%v err=%v", col, has, err)
		}
	}
	// Running it again must stay a no-op rather than erroring on an existing
	// column (Postgres ALTER TABLE ADD COLUMN has no IF NOT EXISTS fallback in
	// our DDL, so the guard is the lookup).
	for i := 0; i < 2; i++ {
		if err := db.migrateSandboxLeasesAddState(ctx); err != nil {
			t.Fatalf("retrofit run %d: %v", i+1, err)
		}
	}
	for _, col := range []string{"state", "paused_at"} {
		has, err := db.tableHasColumn(ctx, "sandbox_leases", col)
		if err != nil || !has {
			t.Fatalf("column %s after retrofit: has=%v err=%v", col, has, err)
		}
	}
	// And the migrated table takes the new writes.
	var st sandbox.SandboxLeaseStore = db
	scope := fmt.Sprintf("agt_pg_mig:s:sess_%d", time.Now().UnixNano())
	if _, _, err := st.AcquireSandboxLease(ctx, scope, "pod-a", "sb-a", "tok", "tpl", time.Minute); err != nil {
		t.Fatalf("acquire on retrofitted table: %v", err)
	}
	if err := st.SetSandboxLeaseState(ctx, scope, "pod-a", "paused"); err != nil {
		t.Fatalf("state write on retrofitted table: %v", err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); got == nil || got.State != "paused" {
		t.Fatalf("retrofitted row = %+v, want state paused", got)
	}
}
