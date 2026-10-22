package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ChatSubmissionStore is the atomic acceptance boundary for a run submission:
// the idempotency claim and the queue row are one transaction. A duplicate
// returns the first turn and creates nothing; a conflict changes nothing.
type ChatSubmissionStore interface {
	AcceptChatSubmission(
		ctx context.Context,
		scopeKey, digest, turnID string,
		entry *SessionQueueEntry,
		ttl time.Duration,
	) (verdict, firstTurnID string, seq int64, err error)
}

// AcceptChatSubmission is ClaimChatIdempotency + EnqueueSessionQueue in one
// transaction. Without it a crash or an insert failure between the two writes
// leaves a claim that names a turn no queue row can reach.
func (d *DBStore) AcceptChatSubmission(
	ctx context.Context,
	scopeKey, digest, turnID string,
	entry *SessionQueueEntry,
	ttl time.Duration,
) (string, string, int64, error) {
	if entry == nil || entry.ID == "" {
		return "", "", 0, fmt.Errorf("store: submission needs a queue entry")
	}
	entry.State = SessionQueueQueued
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	for attempt := 0; attempt < 8; attempt++ {
		var verdict, first string
		var seq int64
		err := d.WithTx(ctx, func(tx Store) error {
			db := tx.(*DBStore)
			verdict, first = IdempotencyMiss, turnID
			if scopeKey != "" {
				v, f, err := db.claimChatIdempotency(ctx, scopeKey, digest, turnID, ttl)
				if err != nil {
					return err
				}
				verdict, first = v, f
				if v != IdempotencyMiss {
					return nil
				}
			}
			var maxSeq sql.NullInt64
			if err := db.handle().QueryRowContext(ctx,
				"SELECT MAX(seq) FROM session_queue WHERE "+db.sessionQueueWhere(),
				entry.UserID, entry.AgentID, entry.SessionID,
			).Scan(&maxSeq); err != nil {
				return err
			}
			seq = maxSeq.Int64 + 1
			_, err := db.handle().ExecContext(ctx,
				"INSERT INTO session_queue (id, user_id, agent_id, session_key, seq, text, state, created_at, expires_at) VALUES ("+
					db.ph(1)+","+db.ph(2)+","+db.ph(3)+","+db.ph(4)+","+db.ph(5)+","+db.ph(6)+","+db.ph(7)+","+db.ph(8)+","+db.ph(9)+")",
				entry.ID, entry.UserID, entry.AgentID, entry.SessionID, seq, entry.Text, entry.State, entry.CreatedAt, entry.ExpiresAt,
			)
			return err
		})
		if err == nil {
			entry.Seq = seq
			if verdict == IdempotencyMiss {
				_, _ = d.CleanupSessionQueue(ctx, time.Hour)
			}
			return verdict, first, seq, nil
		}
		if !strings.Contains(err.Error(), "UNIQUE constraint failed") &&
			!strings.Contains(err.Error(), "duplicate key value") {
			return "", "", 0, err
		}
	}
	return "", "", 0, fmt.Errorf("store: submission acceptance kept conflicting")
}
