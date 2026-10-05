package agentconfig

/**
 * [INPUT]: config (the resolved-agent entity) and two ports it owns: Store
 *          (version + resolve) and Memo (the cache). No database, no Redis, no
 *          HTTP, no gateway type.
 * [OUTPUT]: Resolve.For(ctx, scope) — the resolved agent for one scope, never
 *           older than the counter Store reports, plus the counters that make
 *           cache behaviour observable.
 * [POS]: The use case that owns the invariant in
 *        docs/fastagent/design/15-agent-config-consistency.md §4: a reader must
 *        never serve an entry older than the newest write it could have seen.
 *        Adapters (Postgres, in-process memo) hang off the ports, so the
 *        dependency points inward only. Callers in internal/agent and
 *        internal/gateway depend on this package, never on the cache.
 * [PROTOCOL]: On change, update this header, then check
 *        docs/fastagent/design/15-agent-config-consistency.md (I1–I5 and C1–C6)
 *        and the change-register row that introduced the epoch counter.
 */

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// Version is the global config counter. One counter, strictly increasing: a
// per-scope version compared as max(scope versions) can alias a change, because
// a scope bump is invisible while another scope reads higher.
type Version = int64

// Scope names the agent whose configuration a caller needs. The user ID is part
// of the scope because the resolution rules merge system ← user ← agent.
type Scope struct {
	UserID  string
	AgentID string
}

// Store is the port the use case owns. Adapters implement it: the database
// adapter answers both methods. A test adapter answers from memory.
type Store interface {
	// CurrentVersion returns the counter. A store with no writes reads as 0.
	CurrentVersion(ctx context.Context) (Version, error)
	// Resolve returns the resolved agent as the source of truth holds it now.
	// The returned value carries no version: the caller reads the counter
	// before and after, which is what keeps a torn pair out of the cache.
	Resolve(ctx context.Context, scope Scope) (config.ResolvedAgent, error)
}

// Memo is the cache port. The use case decides what to keep. The adapter
// decides where. The in-process adapter is the only one today (P1's shared
// Redis level was deleted from the plan: with a version check on every read it
// added work and changed no observable behaviour).
type Memo interface {
	Get(scope Scope) (config.ResolvedAgent, Version, bool)
	// Put stores an entry unless a newer version is already there: a slow
	// rebuild must never overwrite a fresher entry.
	Put(scope Scope, cfg config.ResolvedAgent, version Version)
	Drop(scope Scope)
}

// ErrConfigChurn means the counter moved on every read-repair attempt. The
// caller sees an error instead of a value of unknown age (I3: no undeclared
// stale serve).
var ErrConfigChurn = errors.New("agentconfig: the config changed while it was read")

// readRepairAttempts bounds the read-repair loop. Three attempts cover a writer
// that lands between the two counter reads. A fourth writer in that window is
// not a retry problem, it is a signal that the caller should surface.
const readRepairAttempts = 3

// Resolve is the use case.
type Resolve struct {
	store Store
	memo  Memo

	// flights serializes a rebuild per scope. Two callers that miss together
	// pay for one resolve, and the second reads the memo the first filled.
	flights sync.Map // Scope -> *sync.Mutex

	checks   atomic.Int64
	hits     atomic.Int64
	rebuilds atomic.Int64
	churn    atomic.Int64
}

// Stats reports cache behaviour for metrics. The counters are the witnesses the
// plan asks for: a steady state shows many checks, some hits, and rebuilds only
// after a write.
type Stats struct {
	Checks   int64
	Hits     int64
	Rebuilds int64
	Churn    int64
}

func (r *Resolve) Stats() Stats {
	return Stats{Checks: r.checks.Load(), Hits: r.hits.Load(), Rebuilds: r.rebuilds.Load(), Churn: r.churn.Load()}
}

// New builds the use case. Both ports are required: a nil memo would rebuild on
// every read, and a nil store cannot answer the version question at all.
func New(store Store, memo Memo) (*Resolve, error) {
	if store == nil {
		return nil, errors.New("agentconfig: store is required")
	}
	if memo == nil {
		return nil, errors.New("agentconfig: memo is required")
	}
	return &Resolve{store: store, memo: memo}, nil
}

// For returns the resolved agent for one scope. Every read checks the counter:
// the check is what makes a write visible to the writer's own next read, in the
// same turn (the incident this use case exists for), and to any other replica.
func (r *Resolve) For(ctx context.Context, scope Scope) (config.ResolvedAgent, error) {
	if scope.AgentID == "" {
		return config.ResolvedAgent{}, errors.New("agentconfig: agent ID is required")
	}
	lock := r.scopeLock(scope)
	lock.Lock()
	defer lock.Unlock()

	for attempt := 0; attempt < readRepairAttempts; attempt++ {
		r.checks.Add(1)
		before, err := r.store.CurrentVersion(ctx)
		if err != nil {
			return config.ResolvedAgent{}, fmt.Errorf("agentconfig: read version: %w", err)
		}
		if cfg, version, ok := r.memo.Get(scope); ok && version == before {
			r.hits.Add(1)
			return cfg, nil
		}
		cfg, err := r.store.Resolve(ctx, scope)
		if err != nil {
			return config.ResolvedAgent{}, fmt.Errorf("agentconfig: resolve: %w", err)
		}
		after, err := r.store.CurrentVersion(ctx)
		if err != nil {
			return config.ResolvedAgent{}, fmt.Errorf("agentconfig: read version after resolve: %w", err)
		}
		if after != before {
			// The content may mix two versions. Drop anything this scope had
			// and read again instead of caching a pair that never existed.
			r.memo.Drop(scope)
			continue
		}
		r.rebuilds.Add(1)
		r.memo.Put(scope, cfg, after)
		return cfg, nil
	}
	r.churn.Add(1)
	return config.ResolvedAgent{}, ErrConfigChurn
}

func (r *Resolve) scopeLock(scope Scope) *sync.Mutex {
	if v, ok := r.flights.Load(scope); ok {
		return v.(*sync.Mutex)
	}
	created := &sync.Mutex{}
	actual, _ := r.flights.LoadOrStore(scope, created)
	return actual.(*sync.Mutex)
}
