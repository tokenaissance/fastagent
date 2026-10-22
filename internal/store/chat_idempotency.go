package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Chat idempotency verdicts. The strings are the wire contract between the
// store and the chat handler.
const (
	IdempotencyMiss      = "miss"
	IdempotencyDuplicate = "duplicate"
	IdempotencyConflict  = "conflict"
)

// ChatIdempotencyStore is the cross-replica de-duplication window. Every
// environment claims this store; there is no in-memory implementation.
type ChatIdempotencyStore interface {
	ClaimChatIdempotency(ctx context.Context, scopeKey, digest, turnID string, ttl time.Duration) (verdict, firstTurnID string, err error)
	DeleteChatIdempotency(ctx context.Context, scopeKey string) error
}

// ClaimChatIdempotency inserts the key or classifies the existing row.
//
// The INSERT is the compare-and-set: exactly one racer inserts and gets miss.
// Everyone else reads the winner and gets duplicate (same digest, the first
// turn stands) or conflict (same key, different content).
func (d *DBStore) ClaimChatIdempotency(ctx context.Context, scopeKey, digest, turnID string, ttl time.Duration) (string, string, error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	now := time.Now()
	cutoff := now.Add(-ttl)
	for attempt := 0; attempt < 4; attempt++ {
		res, err := d.handle().ExecContext(ctx,
			"INSERT INTO chat_idempotency (scope_key, digest, turn_id, created_at) VALUES ("+
				d.ph(1)+","+d.ph(2)+","+d.ph(3)+","+d.ph(4)+") ON CONFLICT (scope_key) DO NOTHING",
			scopeKey, digest, turnID, now,
		)
		if err != nil {
			return "", "", err
		}
		if n, err := res.RowsAffected(); err == nil && n == 1 {
			d.cleanupChatIdempotency(ctx, ttl)
			return IdempotencyMiss, turnID, nil
		}

		var gotDigest, gotTurn string
		var createdAt time.Time
		err = d.handle().QueryRowContext(ctx,
			"SELECT digest, turn_id, created_at FROM chat_idempotency WHERE scope_key = "+d.ph(1), scopeKey,
		).Scan(&gotDigest, &gotTurn, &createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if createdAt.Before(cutoff) {
			// Expired: claim it for this submission, but only if nobody else
			// replaced the row since the read.
			res, err := d.handle().ExecContext(ctx,
				"UPDATE chat_idempotency SET digest = "+d.ph(1)+", turn_id = "+d.ph(2)+", created_at = "+d.ph(3)+
					" WHERE scope_key = "+d.ph(4)+" AND created_at < "+d.ph(5),
				digest, turnID, now, scopeKey, cutoff,
			)
			if err != nil {
				return "", "", err
			}
			if n, err := res.RowsAffected(); err == nil && n == 1 {
				return IdempotencyMiss, turnID, nil
			}
			continue
		}
		if gotDigest == digest {
			return IdempotencyDuplicate, gotTurn, nil
		}
		return IdempotencyConflict, gotTurn, nil
	}
	return "", "", fmt.Errorf("store: idempotency claim kept racing for %q", scopeKey)
}

// DeleteChatIdempotency releases a claim that did not change anything, such as
// a steer that found no running turn. Without it a failed steer would answer
// duplicate to every retry for the rest of the window.
func (d *DBStore) DeleteChatIdempotency(ctx context.Context, scopeKey string) error {
	_, err := d.handle().ExecContext(ctx,
		"DELETE FROM chat_idempotency WHERE scope_key = "+d.ph(1), scopeKey,
	)
	return err
}

// cleanupChatIdempotency drops rows past the window so the table is bounded by
// live keys, not by every key the deployment has ever seen. It runs on the rare
// insert path, behind the created_at index.
func (d *DBStore) cleanupChatIdempotency(ctx context.Context, ttl time.Duration) {
	if d.dialect == "postgres" {
		_, _ = d.handle().ExecContext(ctx,
			"DELETE FROM chat_idempotency WHERE created_at < now() - ("+d.ph(1)+" * interval '1 second')",
			int64(ttl.Seconds()),
		)
		return
	}
	_, _ = d.handle().ExecContext(ctx,
		"DELETE FROM chat_idempotency WHERE created_at < "+d.ph(1),
		time.Now().Add(-ttl),
	)
}
