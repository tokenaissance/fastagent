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
