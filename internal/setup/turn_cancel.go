package setup

import (
	"context"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// cancelTurn is the use case behind POST /api/chat/cancel, extracted from the
// HTTP handler so it can be exercised without standing up an agent manager
// (clean-architecture's Humble Object: keep the framework-coupled part thin,
// put the logic where it can be tested).
//
// Two shapes, one rule — "the user does not want this turn":
//
//   - still queued (found && !started): withdraw it by canceling the request
//     context; the turn never starts and nothing reaches the session.
//   - otherwise: stamp a cancel request on the session's LIVE lease row. The
//     holder may be another replica; it reads the request at its next iteration
//     boundary (next to the fence check) and stops with a σ. `false` from the
//     store means there was no live possession — an honest no-op, not an error.
//
// Idempotent: withdrawing twice is one withdrawal, stamping twice is one
// request against the same possession.
func cancelTurn(ctx context.Context, st store.Store, pending *pendingWebTurn, found bool, uid, agentID, sessionID string) (canceled, wasRunning bool, err error) {
	if found && pending != nil && !pending.started {
		if pending.cancel != nil {
			pending.cancel()
		}
		return true, false, nil
	}
	if st == nil {
		return false, false, nil
	}
	stamped, err := st.RequestSessionCancel(ctx, uid, agentID, sessionID)
	if err != nil {
		return false, false, err
	}
	return stamped, stamped, nil
}
