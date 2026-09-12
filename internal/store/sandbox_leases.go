package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/cryptoutil"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// The DBStore implements sandbox.SandboxLeaseStore so the gateway can hand
// the same Postgres (or sqlite) handle to the e2b pool on every replica.
// The row format is intentionally dialect-neutral: epoch seconds for
// expires_at/updated_at work on both sqlite and Postgres.

// GetSandboxLease implements sandbox.SandboxLeaseStore.
func (d *DBStore) GetSandboxLease(ctx context.Context, scopeKey string) (*sandbox.SandboxLeaseRecord, error) {
	if scopeKey == "" {
		return nil, nil
	}
	now := time.Now().Unix()
	row := d.handle().QueryRowContext(ctx,
		fmt.Sprintf(`SELECT sandbox_id, envd_token, template, expires_at, epoch
			FROM sandbox_leases
			WHERE scope_key = %s AND expires_at > %s`, d.ph(1), d.ph(2)),
		scopeKey, now)
	var rec sandbox.SandboxLeaseRecord
	if err := row.Scan(&rec.SandboxID, &rec.EnvdToken, &rec.Template, &rec.ExpiresAt, &rec.Epoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &rec, nil
}

// AcquireSandboxLease implements sandbox.SandboxLeaseStore.
//
// Concurrency safety (why the three statements are enough, without an
// advisory lock): the UPDATE only claims an already-expired row and stamps a
// future expiry, so a concurrent acquirer that starts later re-evaluates the
// WHERE clause against the updated row and sees no match (Postgres
// EvalPlanQual; sqlite serializes writes). The INSERT then only wins when no
// row exists, and the final SELECT returns whichever row won — the loser
// gets acquired=false and adopts the winner. Both supported dialects
// (sqlite/postgres) behave this way for these statements.
func (d *DBStore) AcquireSandboxLease(
	ctx context.Context,
	scopeKey, owner, sandboxID, envdToken, template string,
	ttl time.Duration,
) (*sandbox.SandboxLeaseRecord, bool, error) {
	if scopeKey == "" || owner == "" || sandboxID == "" {
		return nil, false, fmt.Errorf("store: sandbox lease requires scopeKey/owner/sandboxID")
	}
	now := time.Now().Unix()
	expires := now + int64(ttl/time.Second)
	if expires <= now {
		expires = now + 1
	}

	// 1. If the row exists but is expired, claim it first so a concurrent
	//    acquirer racing us sees an unexpired row and adopts instead.
	if _, err := d.handle().ExecContext(ctx,
		fmt.Sprintf(`UPDATE sandbox_leases
			SET owner = %s, sandbox_id = %s, envd_token = %s, template = %s,
			    expires_at = %s, epoch = 1, updated_at = %s
			WHERE scope_key = %s AND expires_at <= %s`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6), d.ph(7), d.ph(8)),
		owner, sandboxID, envdToken, template, expires, now, scopeKey, now); err != nil {
		return nil, false, err
	}
	// 2. Insert when absent; a concurrent winner's insert wins and ours no-ops.
	if _, err := d.handle().ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO sandbox_leases
			(scope_key, owner, sandbox_id, envd_token, template, expires_at, epoch, updated_at)
			VALUES (%s, %s, %s, %s, %s, %s, 1, %s)
			ON CONFLICT (scope_key) DO NOTHING`,
			d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6), d.ph(7)),
		scopeKey, owner, sandboxID, envdToken, template, expires, now); err != nil {
		return nil, false, err
	}
	// 3. Read back the authoritative row.
	rec, err := d.GetSandboxLease(ctx, scopeKey)
	if err != nil {
		return nil, false, err
	}
	if rec == nil {
		// Extremely unlikely (expired between steps); treat as lost so the
		// caller retries from scratch.
		return nil, false, nil
	}
	acquired := rec.SandboxID == sandboxID && rec.EnvdToken == envdToken
	return rec, acquired, nil
}

// RenewSandboxLease implements sandbox.SandboxLeaseStore. Ownership moves to
// the renewing pod only when the row still points at sandboxID and has not
// expired (CAS). Returns the new fencing epoch, or 0 when the CAS missed.
func (d *DBStore) RenewSandboxLease(
	ctx context.Context,
	scopeKey, owner, sandboxID string,
	ttl time.Duration,
) (int64, error) {
	if scopeKey == "" || owner == "" || sandboxID == "" {
		return 0, nil
	}
	now := time.Now().Unix()
	expires := now + int64(ttl/time.Second)
	if expires <= now {
		expires = now + 1
	}
	var epoch int64
	err := d.handle().QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE sandbox_leases
			SET owner = %s, expires_at = %s, epoch = epoch + 1, updated_at = %s
			WHERE scope_key = %s AND sandbox_id = %s AND expires_at > %s
			RETURNING epoch`, d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6)),
		owner, expires, now, scopeKey, sandboxID, now).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return epoch, err
}

// ReleaseSandboxLease implements sandbox.SandboxLeaseStore.
func (d *DBStore) ReleaseSandboxLease(ctx context.Context, scopeKey, owner string, epoch int64) (bool, error) {
	if scopeKey == "" || owner == "" {
		return false, nil
	}
	res, err := d.handle().ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM sandbox_leases WHERE scope_key = %s AND owner = %s AND epoch = %s`,
			d.ph(1), d.ph(2), d.ph(3)),
		scopeKey, owner, epoch)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// EncryptedSandboxLeaseStore wraps a lease store so envd_token is encrypted
// before it crosses to the database and decrypted on read. Renew and Release
// pass through untouched (they never touch the token). It is assembled at
// the gateway composition root, never inside DBStore, so the SQL adapter
// stays key-agnostic and unit tests can exercise plaintext behavior. The
// cipher contract lives in cryptoutil so OAuth and sandbox adapters share
// one definition.
type EncryptedSandboxLeaseStore struct {
	Inner sandbox.SandboxLeaseStore
	Crypt cryptoutil.Cipher
}

func (e *EncryptedSandboxLeaseStore) GetSandboxLease(ctx context.Context, scopeKey string) (*sandbox.SandboxLeaseRecord, error) {
	rec, err := e.Inner.GetSandboxLease(ctx, scopeKey)
	if err != nil || rec == nil {
		return rec, err
	}
	plain, err := e.decryptToken(ctx, rec.EnvdToken)
	if err != nil {
		return nil, fmt.Errorf("store: decrypt sandbox lease token: %w", err)
	}
	rec.EnvdToken = plain
	return rec, nil
}

func (e *EncryptedSandboxLeaseStore) AcquireSandboxLease(
	ctx context.Context,
	scopeKey, owner, sandboxID, envdToken, template string,
	ttl time.Duration,
) (*sandbox.SandboxLeaseRecord, bool, error) {
	enc, err := e.encryptToken(ctx, envdToken)
	if err != nil {
		return nil, false, fmt.Errorf("store: encrypt sandbox lease token: %w", err)
	}
	rec, acquired, err := e.Inner.AcquireSandboxLease(
		ctx, scopeKey, owner, sandboxID, enc, template, ttl)
	if err != nil || rec == nil {
		return rec, acquired, err
	}
	plain, derr := e.decryptToken(ctx, rec.EnvdToken)
	if derr != nil {
		return nil, acquired, fmt.Errorf("store: decrypt sandbox lease token after acquire: %w", derr)
	}
	rec.EnvdToken = plain
	return rec, acquired, nil
}

func (e *EncryptedSandboxLeaseStore) RenewSandboxLease(
	ctx context.Context,
	scopeKey, owner, sandboxID string,
	ttl time.Duration,
) (int64, error) {
	return e.Inner.RenewSandboxLease(ctx, scopeKey, owner, sandboxID, ttl)
}

func (e *EncryptedSandboxLeaseStore) ReleaseSandboxLease(ctx context.Context, scopeKey, owner string, epoch int64) (bool, error) {
	return e.Inner.ReleaseSandboxLease(ctx, scopeKey, owner, epoch)
}

// encryptToken returns the AES-GCM ciphertext as base64 so the stored value
// stays a plain TEXT column that is identical on every dialect (sqlite and
// Postgres). No BLOB/BYTEA, no dialect branching.
func (e *EncryptedSandboxLeaseStore) encryptToken(ctx context.Context, plain string) (string, error) {
	enc, err := e.Crypt.Encrypt(ctx, []byte(plain))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

func (e *EncryptedSandboxLeaseStore) decryptToken(ctx context.Context, stored string) (string, error) {
	enc, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return "", err
	}
	plain, err := e.Crypt.Decrypt(ctx, enc)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
