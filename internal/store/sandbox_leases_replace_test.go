package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/adapter"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// TestSandboxLeaseReplaceSandbox pins the one primitive a rebuild needs:
// overwrite the scope's sandbox identity in place, but only while this pod
// still owns it. Renew cannot express it (it CASes on the OLD sandbox_id, so
// it can only maintain the status quo) and Release-then-Acquire would open a
// window in which a sibling replica creates a third sandbox for the scope.
func TestSandboxLeaseReplaceSandbox(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	var st sandbox.SandboxLeaseStore = db
	const scope = "agt_replace:s:sess_1"
	ttl := time.Minute

	rec, acquired, err := st.AcquireSandboxLease(ctx, scope, "pod-a", "sb-a", "tok-a", "tpl-a", ttl)
	if err != nil || !acquired {
		t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
	}

	// A pod that no longer owns the scope must not be able to point it at
	// something else — that is the whole reason the CAS is on owner.
	if epoch, err := st.ReplaceSandboxLease(ctx, scope, "pod-b", "sb-evil", "tok-evil", "tpl-a", ttl); err != nil || epoch != 0 {
		t.Fatalf("foreign replace = (%d, %v), want (0, nil)", epoch, err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); got == nil || got.SandboxID != "sb-a" {
		t.Fatalf("foreign replace mutated the row: %+v", got)
	}

	// The owner replaces the identity; the epoch moves so a destroy request
	// still carrying the previous epoch fails closed.
	epoch, err := st.ReplaceSandboxLease(ctx, scope, "pod-a", "sb-b", "tok-b", "tpl-b", ttl)
	if err != nil || epoch <= rec.Epoch {
		t.Fatalf("owner replace = (%d, %v), want epoch > %d", epoch, err, rec.Epoch)
	}
	got, err := st.GetSandboxLease(ctx, scope)
	if err != nil || got == nil {
		t.Fatalf("GetSandboxLease after replace: rec=%+v err=%v", got, err)
	}
	if got.SandboxID != "sb-b" || got.EnvdToken != "tok-b" || got.Template != "tpl-b" || got.Epoch != epoch {
		t.Fatalf("row after replace = %+v, want sb-b/tok-b/tpl-b/epoch %d", got, epoch)
	}
	if deleted, err := st.ReleaseSandboxLease(ctx, scope, "pod-a", rec.Epoch); err != nil || deleted {
		t.Fatalf("stale-epoch release after replace: deleted=%v err=%v, want false", deleted, err)
	}

	// An expired row belongs to nobody (Acquire may take it), so it must not
	// be replaceable either.
	if _, err := db.db.ExecContext(ctx,
		"UPDATE sandbox_leases SET expires_at = ? WHERE scope_key = ?",
		time.Now().Add(-time.Minute).Unix(), scope); err != nil {
		t.Fatalf("expire row: %v", err)
	}
	if epoch, err := st.ReplaceSandboxLease(ctx, scope, "pod-a", "sb-c", "tok-c", "tpl-b", ttl); err != nil || epoch != 0 {
		t.Fatalf("replace on expired row = (%d, %v), want (0, nil)", epoch, err)
	}
}

// A rebuild mints a fresh envd_token, and it is exactly as sensitive as the
// one Acquire writes: the encryption decorator must cover this path too, so no
// plaintext token ever reaches the row.
func TestEncryptedSandboxLeaseStoreReplaceSandbox(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	crypt, err := adapter.NewAESGCMCryptor("lease-replace-secret")
	if err != nil {
		t.Fatalf("NewAESGCMCryptor: %v", err)
	}
	var st sandbox.SandboxLeaseStore = &EncryptedSandboxLeaseStore{Inner: db, Crypt: crypt}
	const scope = "agt_replace_crypto:s:sess_1"
	const rebuiltToken = "rebuilt-plain-token"

	if _, _, err := st.AcquireSandboxLease(ctx, scope, "pod-a", "sb-a", "tok-a", "tpl", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	epoch, err := st.ReplaceSandboxLease(ctx, scope, "pod-a", "sb-b", rebuiltToken, "tpl", time.Minute)
	if err != nil || epoch == 0 {
		t.Fatalf("replace: epoch=%d err=%v", epoch, err)
	}

	var stored string
	if err := db.db.QueryRowContext(ctx,
		"SELECT envd_token FROM sandbox_leases WHERE scope_key = ?", scope).Scan(&stored); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if stored == "" || strings.Contains(stored, rebuiltToken) {
		t.Fatalf("rebuilt envd_token stored in plaintext: %q", stored)
	}
	got, err := st.GetSandboxLease(ctx, scope)
	if err != nil || got == nil || got.SandboxID != "sb-b" || got.EnvdToken != rebuiltToken {
		t.Fatalf("round-trip after replace: rec=%+v err=%v", got, err)
	}
}
