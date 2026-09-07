package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// fakeLeaseStore implements sandbox.SandboxLeaseStore without a database
// so wiring decisions can be tested without sqlite or Postgres.
type fakeLeaseStore struct{}

func (fakeLeaseStore) GetSandboxLease(context.Context, string) (*sandbox.SandboxLeaseRecord, error) {
	return nil, nil
}

func (fakeLeaseStore) AcquireSandboxLease(
	context.Context, string, string, string, string, string, time.Duration,
) (*sandbox.SandboxLeaseRecord, bool, error) {
	return nil, false, nil
}

func (fakeLeaseStore) RenewSandboxLease(
	context.Context, string, string, string, time.Duration,
) (int64, error) {
	return 0, nil
}

func (fakeLeaseStore) ReleaseSandboxLease(context.Context, string, string, int64) (bool, error) {
	return false, nil
}

func TestSandboxLeaseOptsSelection(t *testing.T) {
	const owner = "host:pid"
	tests := []struct {
		name    string
		leases  sandbox.SandboxLeaseStore
		ownerID string
		wantNil bool
	}{
		{"no store", nil, owner, true},
		{"no owner", fakeLeaseStore{}, "", true},
		{"neither", nil, "", true},
		{"both", fakeLeaseStore{}, owner, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sandboxLeaseOpts(tc.leases, tc.ownerID)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("sandboxLeaseOpts = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("sandboxLeaseOpts = nil, want option")
			}
			if got.Owner != owner {
				t.Fatalf("Owner = %q, want %q", got.Owner, owner)
			}
		})
	}
}

func TestSandboxLeaseStoreFrom(t *testing.T) {
	if got := sandboxLeaseStoreFrom(nil); got != nil {
		t.Fatalf("nil store: got %+v, want nil", got)
	}
	type unrelated struct{ store.Store }
	if got := sandboxLeaseStoreFrom(unrelated{}); got != nil {
		t.Fatalf("non-lease store: got %+v, want nil", got)
	}
	db, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "wiring.db"))
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got := sandboxLeaseStoreFrom(db); got == nil {
		t.Fatal("DBStore does not expose SandboxLeaseStore")
	}
}

func TestBuildSystemSandboxPoolWiring(t *testing.T) {
	disabled := config.SandboxCfg{Enabled: false, Backend: "e2b"}
	if pool := buildSystemSandboxPool(disabled, nil, fakeLeaseStore{}, "host:pid"); pool != nil {
		t.Fatalf("disabled sandbox pool = %v, want nil", pool)
	}

	enabled := config.SandboxCfg{Enabled: true, Backend: "e2b", E2BTemplate: "base"}
	pool := buildSystemSandboxPool(enabled, nil, fakeLeaseStore{}, "host:pid")
	if pool == nil {
		t.Fatal("enabled sandbox pool = nil, want pool")
	}
	t.Cleanup(pool.CloseAll)
	if got := pool.Backend(); got != "e2b" {
		t.Fatalf("Backend() = %q, want e2b", got)
	}
}
