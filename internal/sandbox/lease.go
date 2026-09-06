package sandbox

import (
	"context"
	"time"
)

// SandboxLeaseRecord describes one live cross-pod sandbox lease.
// EnvdToken is short-lived (bounded by the sandbox's e2b timeout) and is
// stored only so sibling gateway replicas can adopt the same instance
// instead of creating a duplicate for the same (agent, project, session).
type SandboxLeaseRecord struct {
	SandboxID string
	EnvdToken string
	Template  string
	// ExpiresAt is a unix timestamp (seconds). An expired lease is dead:
	// the next acquirer may replace it.
	ExpiresAt int64
	// Epoch is the fencing version. It is bumped on every successful
	// renew/adopt; destroy must present the exact epoch the caller last
	// received, so a stale/delayed destroy can never win against a newer
	// ownership.
	Epoch int64
}

// SandboxLeaseStore is the persistence port the E2B pool uses to make
// sandbox instances process-wide instead of per-pod. Implemented by the
// relational store (Postgres in production, sqlite in tests).
type SandboxLeaseStore interface {
	// GetSandboxLease returns the current valid lease for scopeKey, or nil
	// when none exists / the existing lease has expired.
	GetSandboxLease(ctx context.Context, scopeKey string) (*SandboxLeaseRecord, error)
	// AcquireSandboxLease claims scopeKey for (owner, sandboxID) when it is
	// free or expired. Returns the final record and whether this caller's
	// sandbox won the claim; on a lost race the returned record belongs to
	// the winning pod and the caller should adopt it and close its own.
	AcquireSandboxLease(
		ctx context.Context,
		scopeKey, owner, sandboxID, envdToken, template string,
		ttl time.Duration,
	) (record *SandboxLeaseRecord, acquired bool, err error)
	// RenewSandboxLease extends the lease and moves ownership to this pod,
	// but only when the row still points at sandboxID and has not expired
	// (CAS). Returns the new epoch, or 0 when the CAS missed (lease gone or
	// replaced) — the caller must not treat 0 as ownership.
	RenewSandboxLease(
		ctx context.Context,
		scopeKey, owner, sandboxID string,
		ttl time.Duration,
	) (epoch int64, err error)
	// ReleaseSandboxLease deletes the lease only when this pod is still the
	// owner AND the stored epoch matches the one the caller last received.
	// Returns true when the row was deleted (i.e. this pod may destroy the
	// sandbox); false when ownership moved or the epoch is stale.
	ReleaseSandboxLease(ctx context.Context, scopeKey, owner string, epoch int64) (bool, error)
}

// Lease TTL used by the pool when no explicit value is configured. Must be
// comfortably longer than the longest single tool call so an active sandbox
// is never considered dead mid-use; a crashed pod's lease expires within
// this window and another pod takes over.
const DefaultSandboxLeaseTTL = 15 * time.Minute
