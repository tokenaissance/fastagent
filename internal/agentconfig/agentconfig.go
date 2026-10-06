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
	"time"

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
	// staleHits counts the reads that found a memo entry behind the counter:
	// the version check firing, which is the write becoming visible.
	staleHits atomic.Int64
	// checkSeconds and rebuildSeconds keep the recent shape of each kind of
	// work. A check is one read through For; a rebuild is the Resolve inside it.
	checkSeconds   sampleRing
	rebuildSeconds sampleRing
	// storedAt records when this process cached a scope, so a stale hit can
	// report how old the entry was. It is an upper bound on the write→notice
	// delay: the entry was filled before the write, never after.
	storedAt sync.Map // Scope -> time.Time
	// versionLagNs is the largest observed age of a stale entry.
	versionLagNs atomic.Int64
}

// Stats reports cache behaviour for metrics. The counters are the witnesses the
// plan asks for: a steady state shows many checks, some hits, and rebuilds only
// after a write.
type Stats struct {
	Checks   int64 `json:"checks"`
	Hits     int64 `json:"hits"`
	Rebuilds int64 `json:"rebuilds"`
	Churn    int64 `json:"churn"`
	// StaleHits counts the reads that found a cached entry behind the counter.
	// In steady state it stays flat; after a config write it moves by one per
	// scope that reads again.
	StaleHits int64 `json:"staleHits"`
	// RebuildRate is Rebuilds / Checks. The acceptance line is <= 1%.
	RebuildRate float64 `json:"rebuildRate"`
	// CheckSeconds and RebuildSeconds describe the recent window.
	CheckSeconds   LatencyStats `json:"checkSeconds"`
	RebuildSeconds LatencyStats `json:"rebuildSeconds"`
	// VersionLagSeconds is the largest age of a cached entry at the moment a
	// read discovered it was behind the counter. Zero in steady state. A write
	// that no read notices does not appear here: the reader cannot see a write
	// the store has not stamped.
	VersionLagSeconds float64 `json:"versionLagSeconds"`
}

// LatencyStats is the recent shape of one kind of work, in seconds.
type LatencyStats struct {
	P50     float64 `json:"p50"`
	P99     float64 `json:"p99"`
	Samples int     `json:"samples"`
}

// Snapshot is what the ops surface reports: the cache's behaviour, plus the
// counter the store holds right now. The counter is read live rather than taken
// from the last check, because a writer's stamp is exactly the thing an
// operator wants to see confirmed: after a config write the number must have
// moved, whether or not any read has happened since.
type Snapshot struct {
	Counter int64 `json:"counter"`
	Stats   Stats `json:"stats"`
}

// Snapshot reads the counter and the cache counters. One extra point query on
// an ops call, and only on an ops call.
func (r *Resolve) Snapshot(ctx context.Context) (Snapshot, error) {
	counter, err := r.store.CurrentVersion(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("agentconfig: read version for the snapshot: %w", err)
	}
	return Snapshot{Counter: counter, Stats: r.Stats()}, nil
}

func (r *Resolve) Stats() Stats {
	checks := r.checks.Load()
	rebuilds := r.rebuilds.Load()
	checkP50, checkP99, checkSamples := r.checkSeconds.percentiles()
	rebuildP50, rebuildP99, rebuildSamples := r.rebuildSeconds.percentiles()
	rate := 0.0
	if checks > 0 {
		rate = float64(rebuilds) / float64(checks)
	}
	return Stats{
		Checks:            checks,
		Hits:              r.hits.Load(),
		Rebuilds:          rebuilds,
		Churn:             r.churn.Load(),
		StaleHits:         r.staleHits.Load(),
		RebuildRate:       rate,
		CheckSeconds:      LatencyStats{P50: checkP50, P99: checkP99, Samples: checkSamples},
		RebuildSeconds:    LatencyStats{P50: rebuildP50, P99: rebuildP99, Samples: rebuildSamples},
		VersionLagSeconds: time.Duration(r.versionLagNs.Load()).Seconds(),
	}
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
		// The check is the part every read pays: the counter read plus the memo
		// decision. The rebuild is measured on its own, because a hit costs only
		// this and a rebuild is the rare half the acceptance line does not cap.
		// Measuring the whole call here made checkSeconds follow the rebuild,
		// which a live dev run showed: one cold read reported a check p99 of
		// 0.196 s against a rebuild p99 of 0.188 s.
		checkStarted := time.Now()
		before, err := r.store.CurrentVersion(ctx)
		if err != nil {
			r.checkSeconds.observe(time.Since(checkStarted))
			return config.ResolvedAgent{}, fmt.Errorf("agentconfig: read version: %w", err)
		}
		cached, entryVersion, ok := r.memo.Get(scope)
		r.checkSeconds.observe(time.Since(checkStarted))
		if ok {
			if entryVersion == before {
				r.hits.Add(1)
				return cached, nil
			}
			// The entry is behind the counter: this is where a write becomes
			// visible to this process. Record how old the entry was.
			r.staleHits.Add(1)
			if cachedAt, ok := r.storedAt.Load(scope); ok {
				r.observeVersionLag(time.Since(cachedAt.(time.Time)))
			}
		}
		resolveStarted := time.Now()
		cfg, err := r.store.Resolve(ctx, scope)
		if err != nil {
			return config.ResolvedAgent{}, fmt.Errorf("agentconfig: resolve: %w", err)
		}
		r.rebuildSeconds.observe(time.Since(resolveStarted))
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
		r.storedAt.Store(scope, time.Now())
		return cfg, nil
	}
	r.churn.Add(1)
	return config.ResolvedAgent{}, ErrConfigChurn
}

// observeVersionLag keeps the largest lag this process has seen. A smaller
// reading never lowers it: the number answers "how far behind did we catch
// ourselves", and hiding the worst case would defeat the metric.
func (r *Resolve) observeVersionLag(d time.Duration) {
	if d <= 0 {
		return
	}
	for {
		current := r.versionLagNs.Load()
		if int64(d) <= current {
			return
		}
		if r.versionLagNs.CompareAndSwap(current, int64(d)) {
			return
		}
	}
}

func (r *Resolve) scopeLock(scope Scope) *sync.Mutex {
	if v, ok := r.flights.Load(scope); ok {
		return v.(*sync.Mutex)
	}
	created := &sync.Mutex{}
	actual, _ := r.flights.LoadOrStore(scope, created)
	return actual.(*sync.Mutex)
}
