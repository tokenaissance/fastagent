package store

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// The running/paused marker is advisory (a paused sandbox can be woken by
// traffic), but it still has to mean what it says when it is written: adoption
// resumes rather than rebuilds, and the reaper will look for long-paused
// instances. These pin the reads and writes that keep it honest.
func TestSandboxLeaseStateTracking(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	var st sandbox.SandboxLeaseStore = db
	const scope = "agt_state:s:sess_1"
	ttl := time.Minute

	rec, acquired, err := st.AcquireSandboxLease(ctx, scope, "pod-a", "sb-a", "tok-a", "tpl", ttl)
	if err != nil || !acquired {
		t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
	}
	if rec.State != "running" || rec.PausedAt != 0 {
		t.Fatalf("fresh lease state = %q/%d, want running/0", rec.State, rec.PausedAt)
	}

	// A pod that no longer owns the scope must not be able to annotate it.
	if err := st.SetSandboxLeaseState(ctx, scope, "pod-b", "paused"); err != nil {
		t.Fatalf("foreign state write: %v", err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); got.State != "running" {
		t.Fatalf("foreign write changed the state to %q", got.State)
	}

	pausedAt := time.Now().Unix()
	if err := st.SetSandboxLeaseState(ctx, scope, "pod-a", "paused"); err != nil {
		t.Fatalf("owner state write: %v", err)
	}
	got, err := st.GetSandboxLease(ctx, scope)
	if err != nil || got == nil {
		t.Fatalf("GetSandboxLease: rec=%+v err=%v", got, err)
	}
	if got.State != "paused" || got.PausedAt < pausedAt {
		t.Fatalf("state after pause = %q/%d, want paused with a timestamp", got.State, got.PausedAt)
	}

	// Renewing must not silently resurrect a paused sandbox's state: the
	// instance is still asleep, only the ownership moved.
	if _, err := st.RenewSandboxLease(ctx, scope, "pod-b", "sb-a", ttl); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); got.State != "paused" {
		t.Fatalf("renew reset the state to %q", got.State)
	}

	// Resuming flips it back.
	if err := st.SetSandboxLeaseState(ctx, scope, "pod-b", "running"); err != nil {
		t.Fatalf("resume state write: %v", err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); got.State != "running" || got.PausedAt != 0 {
		t.Fatalf("state after resume = %q/%d, want running/0", got.State, got.PausedAt)
	}

	// A rebuild replaces the instance, so whatever the old one was doing is
	// irrelevant: the row describes the new sandbox.
	if err := st.SetSandboxLeaseState(ctx, scope, "pod-b", "paused"); err != nil {
		t.Fatalf("pause before replace: %v", err)
	}
	if _, err := st.ReplaceSandboxLease(ctx, scope, "pod-b", "sb-b", "tok-b", "tpl", ttl); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); got.State != "running" || got.SandboxID != "sb-b" {
		t.Fatalf("state after replace = %q/%s, want running/sb-b", got.State, got.SandboxID)
	}
}

// A production database already has the table, so CREATE TABLE IF NOT EXISTS
// will not add the columns — the retrofit has to, exactly once.
func TestSandboxLeaseStateMigrationRetrofitsExistingTable(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)

	// Rebuild the table in its pre-state shape.
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

	for i := 0; i < 2; i++ { // idempotent
		if err := db.migrateSandboxLeasesAddState(ctx); err != nil {
			t.Fatalf("migration run %d: %v", i+1, err)
		}
	}
	for _, col := range []string{"state", "paused_at"} {
		has, err := db.tableHasColumn(ctx, "sandbox_leases", col)
		if err != nil || !has {
			t.Fatalf("column %s after migration: has=%v err=%v", col, has, err)
		}
	}

	// And the migrated table is usable in the new shape.
	var st sandbox.SandboxLeaseStore = db
	if _, _, err := st.AcquireSandboxLease(ctx, "agt_mig:s:s", "pod-a", "sb-a", "tok", "tpl", time.Minute); err != nil {
		t.Fatalf("acquire on migrated table: %v", err)
	}
	if got, _ := st.GetSandboxLease(ctx, "agt_mig:s:s"); got == nil || got.State != "running" {
		t.Fatalf("migrated row = %+v, want state running", got)
	}
}
