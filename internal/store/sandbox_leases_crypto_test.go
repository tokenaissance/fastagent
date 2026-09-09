package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/adapter"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// TestEncryptedSandboxLeaseStoreRoundTrip proves the gateway-assembled
// wrapper encrypts envd_token before it reaches the database and decrypts
// it on read, using the same AES-GCM cryptor family as MCP OAuth tokens.
func TestEncryptedSandboxLeaseStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	crypt, err := adapter.NewAESGCMCryptor("lease-test-secret")
	if err != nil {
		t.Fatalf("NewAESGCMCryptor: %v", err)
	}
	enc := &EncryptedSandboxLeaseStore{Inner: db, Crypt: crypt}
	var st sandbox.SandboxLeaseStore = enc

	const scope = "agt_crypto:s:sess_crypto"
	const plainToken = "plain-secret-token"
	rec, acquired, err := st.AcquireSandboxLease(
		ctx, scope, "pod-a", "sb-a", plainToken, "tpl", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
	}
	if rec.EnvdToken != plainToken {
		t.Fatalf("acquire returned token %q, want plaintext %q", rec.EnvdToken, plainToken)
	}

	// The raw row must not contain the plaintext token.
	var stored string
	if err := db.db.QueryRowContext(ctx,
		"SELECT envd_token FROM sandbox_leases WHERE scope_key = ?", scope).Scan(&stored); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if stored == "" || strings.Contains(stored, plainToken) {
		t.Fatalf("envd_token stored in plaintext: %q", stored)
	}

	// Reads decrypt back to the original token.
	got, err := st.GetSandboxLease(ctx, scope)
	if err != nil || got == nil {
		t.Fatalf("GetSandboxLease: rec=%+v err=%v", got, err)
	}
	if got.EnvdToken != plainToken {
		t.Fatalf("Get returned token %q, want %q", got.EnvdToken, plainToken)
	}

	// A different key must fail closed on read (never return ciphertext as
	// if it were the token).
	wrong, err := adapter.NewAESGCMCryptor("wrong-secret")
	if err != nil {
		t.Fatalf("NewAESGCMCryptor(wrong): %v", err)
	}
	wrongStore := &EncryptedSandboxLeaseStore{Inner: db, Crypt: wrong}
	if _, err := wrongStore.GetSandboxLease(ctx, scope); err == nil {
		t.Fatal("decrypt with wrong key should fail")
	}
}

// TestEncryptedSandboxLeaseStoreRotation simulates a FASTAGENT_OAUTH_SECRET
// rotation: rows written under the old key become unreadable, and after the
// lease expires a pod holding the new key can reclaim the scope cleanly.
func TestEncryptedSandboxLeaseStoreRotation(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)

	key1, err := adapter.NewAESGCMCryptor("old-secret")
	if err != nil {
		t.Fatalf("NewAESGCMCryptor(old): %v", err)
	}
	key2, err := adapter.NewAESGCMCryptor("new-secret")
	if err != nil {
		t.Fatalf("NewAESGCMCryptor(new): %v", err)
	}
	oldStore := &EncryptedSandboxLeaseStore{Inner: db, Crypt: key1}
	newStore := &EncryptedSandboxLeaseStore{Inner: db, Crypt: key2}

	const scope = "agt_rotate:s:sess_rotate"
	rec, acquired, err := oldStore.AcquireSandboxLease(
		ctx, scope, "pod-a", "sb-a", "token-under-old-key", "tpl", time.Second)
	if err != nil || !acquired || rec.EnvdToken != "token-under-old-key" {
		t.Fatalf("old-key acquire: rec=%+v acquired=%v err=%v", rec, acquired, err)
	}
	// Immediately after rotation the new key cannot read the row.
	if _, err := newStore.GetSandboxLease(ctx, scope); err == nil {
		t.Fatal("new key read old row unexpectedly succeeded")
	}
	// Once the lease expires, the new key reclaims and replaces it.
	time.Sleep(1200 * time.Millisecond)
	rec2, acquired2, err := newStore.AcquireSandboxLease(
		ctx, scope, "pod-b", "sb-b", "token-under-new-key", "tpl", time.Minute)
	if err != nil || !acquired2 {
		t.Fatalf("new-key reclaim: acquired=%v err=%v", acquired2, err)
	}
	if rec2.EnvdToken != "token-under-new-key" {
		t.Fatalf("reclaimed token = %q, want new token", rec2.EnvdToken)
	}
	got, err := newStore.GetSandboxLease(ctx, scope)
	if err != nil || got == nil || got.EnvdToken != "token-under-new-key" {
		t.Fatalf("new-key read after reclaim: rec=%+v err=%v", got, err)
	}
}

// TestEncryptedSandboxLeaseStorePostgres proves the same base64-in-TEXT
// encryption and rotation semantics on the production dialect.
func TestEncryptedSandboxLeaseStorePostgres(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeasePostgresDB(t)

	key1, err := adapter.NewAESGCMCryptor("pg-old-secret")
	if err != nil {
		t.Fatalf("NewAESGCMCryptor(old): %v", err)
	}
	key2, err := adapter.NewAESGCMCryptor("pg-new-secret")
	if err != nil {
		t.Fatalf("NewAESGCMCryptor(new): %v", err)
	}
	oldStore := &EncryptedSandboxLeaseStore{Inner: db, Crypt: key1}
	newStore := &EncryptedSandboxLeaseStore{Inner: db, Crypt: key2}

	const scope = "agt_pg_crypto:s:sess_crypto"
	const oldToken = "pg-token-old"
	const newToken = "pg-token-new"
	if _, acquired, err := oldStore.AcquireSandboxLease(
		ctx, scope, "pod-a", "sb-a", oldToken, "tpl", time.Second); err != nil || !acquired {
		t.Fatalf("old-key acquire: acquired=%v err=%v", acquired, err)
	}
	var stored string
	if err := db.db.QueryRowContext(ctx,
		"SELECT envd_token FROM sandbox_leases WHERE scope_key = $1", scope).Scan(&stored); err != nil {
		t.Fatalf("raw row: %v", err)
	}
	if stored == "" || strings.Contains(stored, oldToken) {
		t.Fatalf("envd_token stored in plaintext on Postgres: %q", stored)
	}
	if _, err := newStore.GetSandboxLease(ctx, scope); err == nil {
		t.Fatal("new key read old row unexpectedly succeeded on Postgres")
	}
	time.Sleep(1200 * time.Millisecond)
	rec, acquired, err := newStore.AcquireSandboxLease(
		ctx, scope, "pod-b", "sb-b", newToken, "tpl", time.Minute)
	if err != nil || !acquired || rec.EnvdToken != newToken {
		t.Fatalf("new-key reclaim: rec=%+v acquired=%v err=%v", rec, acquired, err)
	}
}
