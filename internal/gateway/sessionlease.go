package gateway

import (
	"context"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// storeSessionLease adapts store.Store to agent.SessionLease: the port's
// vocabulary (internal/agent, the consumer and owner of the rule) is projected
// onto the store's SQL (internal/store). It lives here, next to storeLeaser,
// because the composition root is the only place that may know both sides.
//
// The adapter mints the holder: uniqueness per acquisition is the fence's
// premise (obligation L4(c)), so a caller must not be able to pass one in.
type storeSessionLease struct{ st store.Store }

// NewStoreSessionLease is the one way to build the adapter from outside this
// package. The composition root uses it (gateway/userspace.go); so does the
// two-replica test harness in internal/setup, which has to build the production
// shape — two Servers that share one store — or it would only be testing two
// unrelated servers (docs/fs-formal-proof/11-change-register.md row 32, the one
// item that note held open).
//
// The struct stays unexported on purpose: the holder is minted inside Acquire,
// and a caller that could name the type could name a holder too.
func NewStoreSessionLease(st store.Store) agent.SessionLease { return storeSessionLease{st: st} }

func (s storeSessionLease) Acquire(ctx context.Context, key agent.SessionKey, ttl time.Duration) (*agent.Turn, error) {
	holder := agent.NewTurnHolder()
	epoch, ok, err := s.st.AcquireSessionLease(ctx, key.UserID, key.AgentID, key.SessionKey, holder, ttl)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, s.busy(ctx, key)
	}
	return &agent.Turn{Holder: holder, Epoch: epoch, ExpiresAt: time.Now().Add(ttl)}, nil
}

func (s storeSessionLease) Renew(ctx context.Context, key agent.SessionKey, held *agent.Turn, ttl time.Duration) (*agent.Turn, error) {
	epoch, ok, err := s.st.RenewSessionLease(ctx, key.UserID, key.AgentID, key.SessionKey, held.Holder, held.Epoch, ttl)
	if err != nil {
		return nil, err
	}
	if !ok {
		// Lost: a live holder we are not. Name it when the row can tell us,
		// so the turn's σ can say who took over (docs A4.1).
		return nil, s.busy(ctx, key)
	}
	return &agent.Turn{Holder: held.Holder, Epoch: epoch, ExpiresAt: time.Now().Add(ttl)}, nil
}

func (s storeSessionLease) Release(ctx context.Context, key agent.SessionKey, held *agent.Turn) error {
	return s.st.ReleaseSessionLease(ctx, key.UserID, key.AgentID, key.SessionKey, held.Holder, held.Epoch)
}

func (s storeSessionLease) CancelRequested(ctx context.Context, key agent.SessionKey, held *agent.Turn) (bool, error) {
	rec, err := s.st.GetSessionLease(ctx, key.UserID, key.AgentID, key.SessionKey)
	if err != nil || rec == nil {
		return false, err
	}
	// Only the current possession acts on the request: a stamp from an earlier
	// possession must not stop this one (the row's epoch is the boundary).
	if held != nil && rec.Epoch != held.Epoch {
		return false, nil
	}
	return rec.CancelRequested != 0, nil
}

func (s storeSessionLease) Live(ctx context.Context, key agent.SessionKey) (*agent.Turn, error) {
	rec, err := s.st.GetSessionLease(ctx, key.UserID, key.AgentID, key.SessionKey)
	if err != nil || rec == nil {
		return nil, err
	}
	return &agent.Turn{Holder: rec.HolderID, Epoch: rec.Epoch, ExpiresAt: rec.ExpiresAt}, nil
}

// busy reads the live row only to fill the error's facts; the verdict itself
// came from the CAS. A read failure still reports "busy" without the facts —
// losing the ETA must not turn a refusal into a start.
func (s storeSessionLease) busy(ctx context.Context, key agent.SessionKey) error {
	rec, err := s.st.GetSessionLease(ctx, key.UserID, key.AgentID, key.SessionKey)
	if err != nil || rec == nil {
		return &agent.SessionTurnBusy{}
	}
	return &agent.SessionTurnBusy{Holder: rec.HolderID, ExpiresAt: rec.ExpiresAt}
}
