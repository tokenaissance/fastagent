package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SessionQueueState is the durable state of one waiting submission.
const (
	SessionQueueQueued   = "queued"
	SessionQueueStarted  = "started"
	SessionQueueCanceled = "canceled"
)

// SessionQueueEntry is one turn waiting for a session's writer slot.
//
// It is the entity the dashboard's queued block reads: id is the turn id the
// cancellation endpoint names, seq is the global per-session order assigned at
// acceptance, and expiresAt is when this claim stops being true. The wait used
// to be a goroutine's context plus a per-pod map; that lost the entry on a
// reload, a pod restart, or a cancel routed elsewhere.
type SessionQueueEntry struct {
	ID        string
	UserID    string
	AgentID   string
	SessionID string
	Seq       int64
	Text      string
	State     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// SessionQueueStore is the narrow capability the agent and the chat handler
// need. *DBStore implements it; a nil store means the legacy in-process wait.
type SessionQueueStore interface {
	EnqueueSessionQueue(ctx context.Context, entry *SessionQueueEntry) (int64, error)
	GetSessionQueue(ctx context.Context, id string) (*SessionQueueEntry, error)
	ListSessionQueue(ctx context.Context, userID, agentID, sessionID string) ([]SessionQueueEntry, error)
	CancelSessionQueue(ctx context.Context, id string) (bool, error)
	MarkSessionQueueStarted(ctx context.Context, id string) error
}

func (d *DBStore) sessionQueueWhere() string {
	return fmt.Sprintf(
		"user_id = %s AND agent_id = %s AND session_key = %s",
		d.ph(1), d.ph(2), d.ph(3),
	)
}

// EnqueueSessionQueue assigns the next per-session sequence and stores the
// entry. Two concurrent acceptances can read the same MAX; the unique key
// (user, agent, session, seq) turns that race into one retry rather than a
// duplicate order.
func (d *DBStore) EnqueueSessionQueue(ctx context.Context, entry *SessionQueueEntry) (int64, error) {
	if entry == nil || entry.ID == "" {
		return 0, errors.New("store: queue entry needs an id")
	}
	if entry.State == "" {
		entry.State = SessionQueueQueued
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	for attempt := 0; attempt < 8; attempt++ {
		var seq int64
		err := d.WithTx(ctx, func(tx Store) error {
			db := tx.(*DBStore)
			var maxSeq sql.NullInt64
			if err := db.handle().QueryRowContext(ctx,
				"SELECT MAX(seq) FROM session_queue WHERE "+db.sessionQueueWhere(),
				entry.UserID, entry.AgentID, entry.SessionID,
			).Scan(&maxSeq); err != nil {
				return err
			}
			seq = maxSeq.Int64 + 1
			_, err := db.handle().ExecContext(ctx,
				fmt.Sprintf(`INSERT INTO session_queue
					(id, user_id, agent_id, session_key, seq, text, state, created_at, expires_at)
					VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s)`,
					db.ph(1), db.ph(2), db.ph(3), db.ph(4), db.ph(5), db.ph(6), db.ph(7), db.ph(8), db.ph(9)),
				entry.ID, entry.UserID, entry.AgentID, entry.SessionID, seq,
				entry.Text, entry.State, entry.CreatedAt, entry.ExpiresAt,
			)
			return err
		})
		if err == nil {
			entry.Seq = seq
			return seq, nil
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
	}
	return 0, errors.New("store: queue seq allocation kept conflicting")
}

func (d *DBStore) GetSessionQueue(ctx context.Context, id string) (*SessionQueueEntry, error) {
	e := &SessionQueueEntry{}
	err := d.handle().QueryRowContext(ctx,
		`SELECT id, user_id, agent_id, session_key, seq, text, state, created_at, expires_at
		 FROM session_queue WHERE id = `+d.ph(1), id,
	).Scan(&e.ID, &e.UserID, &e.AgentID, &e.SessionID, &e.Seq, &e.Text, &e.State, &e.CreatedAt, &e.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

func (d *DBStore) ListSessionQueue(ctx context.Context, userID, agentID, sessionID string) ([]SessionQueueEntry, error) {
	rows, err := d.handle().QueryContext(ctx,
		`SELECT id, user_id, agent_id, session_key, seq, text, state, created_at, expires_at
		 FROM session_queue WHERE `+d.sessionQueueWhere()+` ORDER BY seq ASC`,
		userID, agentID, sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionQueueEntry
	for rows.Next() {
		var e SessionQueueEntry
		if err := rows.Scan(&e.ID, &e.UserID, &e.AgentID, &e.SessionID, &e.Seq, &e.Text, &e.State, &e.CreatedAt, &e.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CancelSessionQueue marks a queued entry canceled. It is deliberately a
// compare-and-set on state=queued: a started turn cannot be withdrawn here.
func (d *DBStore) CancelSessionQueue(ctx context.Context, id string) (bool, error) {
	res, err := d.handle().ExecContext(ctx,
		"UPDATE session_queue SET state = "+d.ph(1)+" WHERE id = "+d.ph(2)+" AND state = "+d.ph(3),
		SessionQueueCanceled, id, SessionQueueQueued,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkSessionQueueStarted advances a still-queued entry. A canceled entry is
// left alone: withdrawal wins over a late start.
func (d *DBStore) MarkSessionQueueStarted(ctx context.Context, id string) error {
	_, err := d.handle().ExecContext(ctx,
		"UPDATE session_queue SET state = "+d.ph(1)+" WHERE id = "+d.ph(2)+" AND state = "+d.ph(3),
		SessionQueueStarted, id, SessionQueueQueued,
	)
	return err
}

// CleanupSessionQueue drops finished entries older than olderThan. The cutoff
// is evaluated by the database, not by a Go timestamp: the column is TIMESTAMP
// WITHOUT TIME ZONE, and a local +08:00 cutoff compared against UTC rows is
// eight hours ahead and deletes just-finished entries (measured in the
// two-pod dev run, 2026-10-22).
func (d *DBStore) CleanupSessionQueue(ctx context.Context, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		olderThan = time.Hour
	}
	secs := int64(olderThan.Seconds())
	var expr string
	var args []any
	if d.dialect == "postgres" {
		expr = "created_at < now() - (" + d.ph(2) + " * interval '1 second')"
		args = []any{SessionQueueQueued, secs}
	} else {
		// SQLite has no portable timestamp arithmetic on the text form the
		// driver writes (it includes the +08:00 offset), so compare against a
		// Go cutoff in the same local zone.
		expr = "created_at < " + d.ph(2)
		args = []any{SessionQueueQueued, time.Now().Add(-olderThan)}
	}
	res, err := d.handle().ExecContext(ctx,
		"DELETE FROM session_queue WHERE state <> "+d.ph(1)+" AND "+expr,
		args...,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
