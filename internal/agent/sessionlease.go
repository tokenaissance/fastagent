package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"
)

// The cross-replica turn lease: "at most one turn is writing this session".
//
// This file owns the *port*. The port's vocabulary — the session's identity,
// the possession a turn holds, the refusal that carries the other holder's
// facts — belongs to the layer that has the rule; the SQL lives in
// internal/store and the wiring in the gateway composition root, exactly like
// channels.Leaser (see docs/文件系统形式化证明/12-lease-formal-design.md §7 for the
// four-layer placement and §3 for the obligations L1–L6).
//
// It is deliberately NOT channels.Leaser: that key is (channel, account) and
// has no session dimension, so reusing it would serialise the wrong thing.

// SessionKey identifies the guarded resource by the same identity the sessions
// table uses. The chat triple (channel, account, chat) is NOT the key: several
// triples can resolve to one session, and serialising on the triple is exactly
// the hole that let a dashboard turn and a cron turn write one history
// (docs/session-turn-integrity.md, clause W).
type SessionKey struct {
	UserID     string
	AgentID    string
	SessionKey string
}

// Turn is the possession a successful Acquire grants. The holder is minted per
// acquisition (never the process identity): (holder, epoch) must be unique per
// acquisition over the whole life of the lease row, or a delayed write from an
// earlier possession could present a token a newer row also accepts — the
// failure G25 measured in the sandbox lease (obligation L4(c)).
type Turn struct {
	Holder    string
	Epoch     int64
	ExpiresAt time.Time
}

// SessionTurnBusy reports that a live turn already holds the session. It
// carries the holder's facts because the only consumers are signals: the
// queued event needs a holder and an ETA, and a caller deciding whether to
// retry needs to know when retrying could work (docs/session-turn-integrity.md
// A4.1). A bare bool would force every caller to invent those numbers.
type SessionTurnBusy struct {
	Holder    string
	ExpiresAt time.Time
}

// ErrSessionTurnBusy is the sentinel errors.Is matches; the typed error above
// carries the facts.
var ErrSessionTurnBusy = errors.New("session turn busy")

func (e *SessionTurnBusy) Error() string {
	if e.Holder == "" {
		return "session turn busy: another turn holds this session"
	}
	return fmt.Sprintf("session turn busy: held by %s until %s", e.Holder, e.ExpiresAt.UTC().Format(time.RFC3339))
}

func (e *SessionTurnBusy) Is(target error) bool { return target == ErrSessionTurnBusy }

// SessionLease is the admission port a turn holds for its whole life.
//
// Acquire generates the holder; Renew and Release present the possession they
// were given (never loose arguments, so a caller cannot update half of it).
// Every error other than *SessionTurnBusy is a refusal to start: the store
// being unreachable must fail closed, because refusing to start is
// recoverable and two writers are not.
type SessionLease interface {
	Acquire(ctx context.Context, key SessionKey, ttl time.Duration) (*Turn, error)
	Renew(ctx context.Context, key SessionKey, held *Turn, ttl time.Duration) (*Turn, error)
	Release(ctx context.Context, key SessionKey, held *Turn) error
	// CancelRequested reports whether the user asked THIS possession to stop.
	// The request rides the lease row, so it reaches the holder wherever it
	// runs; the holder reads it at its iteration boundary, next to the fence
	// check (docs/session-turn-integrity.md A4, cancel design X1–X6). A false
	// answer is not a claim that nobody asked — it is the fact this row
	// carries right now.
	CancelRequested(ctx context.Context, key SessionKey, held *Turn) (bool, error)
	// Live reports the current holder, or nil when the session is free. It is
	// a READ: the verdict that grants ownership is always Acquire's. Its one
	// caller is TurnStartIfIdle, which must refuse to start (never queue) when
	// somebody — possibly on another replica — already holds the session.
	Live(ctx context.Context, key SessionKey) (*Turn, error)
}

// NopSessionLease is the single-instance implementation: every Acquire wins and
// nothing is shared. It mirrors channels.NopLeaser and is what an install
// without a wired lease (file-backed dev, tests) gets, so those paths keep
// today's behaviour instead of failing closed on a component they do not have.
type NopSessionLease struct{}

func (NopSessionLease) Acquire(context.Context, SessionKey, time.Duration) (*Turn, error) {
	return &Turn{Holder: "nop", Epoch: 1, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (NopSessionLease) Renew(_ context.Context, _ SessionKey, held *Turn, _ time.Duration) (*Turn, error) {
	next := *held
	next.Epoch++
	next.ExpiresAt = time.Now().Add(time.Hour)
	return &next, nil
}

func (NopSessionLease) Release(context.Context, SessionKey, *Turn) error { return nil }

func (NopSessionLease) Live(context.Context, SessionKey) (*Turn, error) { return nil, nil }

func (NopSessionLease) CancelRequested(context.Context, SessionKey, *Turn) (bool, error) {
	return false, nil
}

// NewTurnHolder mints "<pod>/<random>". The pod name makes rows and logs
// attributable (the 09-18 investigation needed that and did not have it); the
// random half makes the pair unique per acquisition.
//
// It is exported for the composition root's adapter, which is the only place
// that knows a lease exists at all; the port's callers never see a holder.
func NewTurnHolder() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "pod"
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A clock-derived fallback keeps the pair unique enough to be safe:
		// the epoch still orders possessions, and a collision needs the same
		// nanosecond on the same pod.
		return fmt.Sprintf("%s/%x", host, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s/%s", host, hex.EncodeToString(b[:]))
}

// lease returns the wired lease, or the single-instance one. A nil field means
// "no lease configured": file-backed dev installs, tests and the doctor keep
// today's behaviour instead of failing closed on a component they never had.
func (a *Agent) lease() SessionLease {
	if a.sessionLease == nil {
		return NopSessionLease{}
	}
	return a.sessionLease
}
