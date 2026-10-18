package usage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// OutboxEntry is one persisted usage report that still needs delivery.
type OutboxEntry struct {
	ID       string
	Payload  []byte
	Attempts int
}

// Outbox stores usage reports until the webhook accepts them.
//
// The outbox is the durability boundary for billing. A report is written
// before the HTTP call, so a process restart cannot lose it. Delivery is
// at-least-once; the receiver deduplicates by the report ID carried in the
// payload.
type Outbox interface {
	Enqueue(ctx context.Context, id string, payload []byte) error
	Pending(ctx context.Context, now time.Time, limit int) ([]OutboxEntry, error)
	MarkSent(ctx context.Context, id string) error
	MarkRetry(ctx context.Context, id string, attempts int, next time.Time, lastErr string) error
	MarkDead(ctx context.Context, id string, attempts int, lastErr string) error
	Close() error
}

type memOutboxEntry struct {
	payload  []byte
	attempts int
	next     time.Time
	status   string
}

// MemOutbox is the in-process Outbox. It keeps reports across meter
// restarts inside one process, but not across process restarts. Production
// wiring uses SQLOutbox on the same database as the local meter.
type MemOutbox struct {
	mu      sync.Mutex
	entries map[string]*memOutboxEntry
}

func NewMemOutbox() *MemOutbox {
	return &MemOutbox{entries: make(map[string]*memOutboxEntry)}
}

func (m *MemOutbox) Enqueue(_ context.Context, id string, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; ok {
		return nil
	}
	m.entries[id] = &memOutboxEntry{
		payload: append([]byte(nil), payload...),
		next:    time.Now(),
		status:  "pending",
	}
	return nil
}

func (m *MemOutbox) Pending(_ context.Context, now time.Time, limit int) ([]OutboxEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]OutboxEntry, 0, limit)
	for id, e := range m.entries {
		if e.status != "pending" || e.next.After(now) {
			continue
		}
		out = append(out, OutboxEntry{
			ID:       id,
			Payload:  append([]byte(nil), e.payload...),
			Attempts: e.attempts,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemOutbox) MarkSent(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[id]; ok && e.status == "pending" {
		e.status = "sent"
	}
	return nil
}

func (m *MemOutbox) MarkRetry(_ context.Context, id string, attempts int, next time.Time, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[id]; ok && e.status == "pending" {
		e.attempts = attempts
		e.next = next
		e.status = "pending"
	}
	return nil
}

func (m *MemOutbox) MarkDead(_ context.Context, id string, attempts int, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[id]; ok && e.status == "pending" {
		e.attempts = attempts
		e.status = "dead"
	}
	return nil
}

func (m *MemOutbox) Close() error { return nil }

// SQLOutbox persists usage reports in usage_outbox. It shares the store's
// database and dialect, so it works on both SQLite and Postgres.
type SQLOutbox struct {
	db      *sql.DB
	dialect string
}

func NewSQLOutbox(db *sql.DB, dialect string) *SQLOutbox {
	return &SQLOutbox{db: db, dialect: dialect}
}

func (s *SQLOutbox) Enqueue(ctx context.Context, id string, payload []byte) error {
	q := rebindQuery(`
		INSERT INTO usage_outbox (id, payload, status, attempts, next_attempt_at)
		VALUES (?, ?, 'pending', 0, ?)
		ON CONFLICT (id) DO NOTHING`, s.dialect)
	_, err := s.db.ExecContext(ctx, q, id, string(payload), timeParam(s.dialect, time.Now()))
	if err != nil {
		return fmt.Errorf("insert usage outbox row: %w", err)
	}
	return nil
}

func (s *SQLOutbox) Pending(ctx context.Context, now time.Time, limit int) ([]OutboxEntry, error) {
	q := rebindQuery(`
		SELECT id, payload, attempts
		FROM usage_outbox
		WHERE status = 'pending' AND next_attempt_at <= ?
		ORDER BY next_attempt_at, created_at
		LIMIT ?`, s.dialect)
	rows, err := s.db.QueryContext(ctx, q, timeParam(s.dialect, now), limit)
	if err != nil {
		return nil, fmt.Errorf("read pending usage outbox rows: %w", err)
	}
	defer rows.Close()

	var out []OutboxEntry
	for rows.Next() {
		var e OutboxEntry
		var payload string
		if err := rows.Scan(&e.ID, &payload, &e.Attempts); err != nil {
			return nil, fmt.Errorf("scan usage outbox row: %w", err)
		}
		e.Payload = []byte(payload)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage outbox rows: %w", err)
	}
	return out, nil
}

func (s *SQLOutbox) MarkSent(ctx context.Context, id string) error {
	q := rebindQuery(`
		UPDATE usage_outbox
		SET status = 'sent', sent_at = CURRENT_TIMESTAMP, last_error = ''
		WHERE id = ? AND status = 'pending'`, s.dialect)
	_, err := s.db.ExecContext(ctx, q, id)
	return err
}

func (s *SQLOutbox) MarkRetry(ctx context.Context, id string, attempts int, next time.Time, lastErr string) error {
	q := rebindQuery(`
		UPDATE usage_outbox
		SET attempts = ?, next_attempt_at = ?, last_error = ?
		WHERE id = ? AND status = 'pending'`, s.dialect)
	_, err := s.db.ExecContext(ctx, q, attempts, timeParam(s.dialect, next), lastErr, id)
	return err
}

func (s *SQLOutbox) MarkDead(ctx context.Context, id string, attempts int, lastErr string) error {
	q := rebindQuery(`
		UPDATE usage_outbox
		SET status = 'dead', attempts = ?, last_error = ?
		WHERE id = ? AND status = 'pending'`, s.dialect)
	_, err := s.db.ExecContext(ctx, q, attempts, lastErr, id)
	return err
}

func (s *SQLOutbox) Close() error { return nil }

func rebindQuery(q string, dialect string) string {
	if dialect != "postgres" {
		return q
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

func timeParam(dialect string, t time.Time) any {
	if dialect == "sqlite" {
		return t.UTC().Format("2006-01-02 15:04:05")
	}
	return t.UTC()
}
