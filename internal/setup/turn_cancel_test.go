package setup

import (
	"context"
	"testing"
	"time"
)

// The cancel use case, exercised without an HTTP harness or an agent manager
// (X6b): a queued turn is withdrawn, a running turn is stamped onto the live
// lease row, and "nothing to cancel" is an honest false rather than an error.
//
// Falsification: make cancelTurn return true unconditionally and the no-lease
// case below goes red — the handler would then tell the user "cancelled" when
// nothing happened, which is the class of lie this whole line of work removes.
func TestCancelTurnUseCase(t *testing.T) {
	ctx := context.Background()
	s, uid, aid := setupFileUploadTest(t)
	const session = "sess-cancel-usecase"

	// 1. Queued: the request context is cancelled and nothing is stamped.
	withdrawn := false
	pending := &pendingWebTurn{started: false, cancel: func() { withdrawn = true }}
	canceled, wasRunning, err := cancelTurn(ctx, s.dataStore, pending, true, uid, aid, session)
	if err != nil || !canceled || wasRunning || !withdrawn {
		t.Fatalf("queued cancel: canceled=%v wasRunning=%v withdrawn=%v err=%v", canceled, wasRunning, withdrawn, err)
	}

	// 2. Nothing anywhere: honest false, no error.
	if canceled, wasRunning, err := cancelTurn(ctx, s.dataStore, nil, false, uid, aid, session); err != nil || canceled || wasRunning {
		t.Fatalf("no-op cancel: canceled=%v wasRunning=%v err=%v; want false/false/nil", canceled, wasRunning, err)
	}

	// 3. Started and a live possession exists: the request rides the row.
	if _, ok, err := s.dataStore.AcquireSessionLease(ctx, uid, aid, session, "pod-a/1", time.Minute); err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	canceled, wasRunning, err = cancelTurn(ctx, s.dataStore, &pendingWebTurn{started: true}, true, uid, aid, session)
	if err != nil || !canceled || !wasRunning {
		t.Fatalf("running cancel: canceled=%v wasRunning=%v err=%v; want true/true/nil", canceled, wasRunning, err)
	}
	// Idempotent, and the row carries the request.
	if canceled, _, _ := cancelTurn(ctx, s.dataStore, &pendingWebTurn{started: true}, true, uid, aid, session); !canceled {
		t.Fatal("a second cancel on the same possession was not idempotent")
	}
	lease, err := s.dataStore.GetSessionLease(ctx, uid, aid, session)
	if err != nil || lease == nil || lease.CancelRequested == 0 {
		t.Fatalf("the request did not ride the row: lease=%+v err=%v", lease, err)
	}

	// 4. Started but the possession already lapsed: honest false.
	if err := s.dataStore.ReleaseSessionLease(ctx, uid, aid, session, "pod-a/1", lease.Epoch); err != nil {
		t.Fatalf("release: %v", err)
	}
	if canceled, wasRunning, err := cancelTurn(ctx, s.dataStore, &pendingWebTurn{started: true}, true, uid, aid, session); err != nil || canceled || wasRunning {
		t.Fatalf("cancel after release: canceled=%v wasRunning=%v err=%v; want false/false/nil", canceled, wasRunning, err)
	}
}
