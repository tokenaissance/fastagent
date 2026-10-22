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

// ChatIdempotencyStore is the cross-replica de-duplication window. The in-process
// map is only a fallback for stores that do not implement it.
type ChatIdempotencyStore interface {
	ClaimChatIdempotency(ctx context.Context, scopeKey, digest, turnID string, ttl time.Duration) (verdict, firstTurnID string, err error)
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
