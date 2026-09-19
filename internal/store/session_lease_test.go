package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The session turn lease is the cross-replica "one writer per session" gate
// (docs/session-turn-integrity.md A1). Its contract, in the terms of
// docs/文件系统形式化证明/12-lease-formal-design.md:
//
//	L1 exclusion — at most one live row per key; acquiring is a CAS
//	L3 freshness — expiry is the only way a live holder is displaced
//	L4(c) fencing — (holder, epoch) is unique per acquisition, and the epoch
//	     never resets while the row lives
//	L5 guarded release — a superseded holder cannot renew or release
func TestSessionLeaseAcquireRenewReleaseAndTakeover(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	const user, agent, session = "u1", "agt1", "sess1"

	// Fresh row: epoch 1.
	first, ok, err := db.AcquireSessionLease(ctx, user, agent, session, "pod-a/1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("fresh acquire: ok=%v err=%v", ok, err)
	}
	if first != 1 {
		t.Fatalf("fresh epoch = %d, want 1", first)
	}

	// A second holder while the lease is live loses without an error: the
	// caller's contract is "queue", not "retry blindly".
	if epoch, ok, err := db.AcquireSessionLease(ctx, user, agent, session, "pod-b/2", time.Minute); err != nil || ok {
		t.Fatalf("second holder while live: epoch=%d ok=%v err=%v; want ok=false, no error", epoch, ok, err)
	}

	// Renew bumps the epoch and is the only way to keep it.
	renewed, ok, err := db.RenewSessionLease(ctx, user, agent, session, "pod-a/1", first, time.Minute)
	if err != nil || !ok || renewed <= first {
		t.Fatalf("renew: epoch=%d ok=%v err=%v (want > %d)", renewed, ok, err, first)
	}

	// The stale token cannot renew again (L4c/L5): ownership moved on inside
	// the row's own history.
	if _, ok, _ := db.RenewSessionLease(ctx, user, agent, session, "pod-a/1", first, time.Minute); ok {
		t.Fatal("a stale epoch renewed the lease")
	}
	// …and it cannot release the live row either.
	if err := db.ReleaseSessionLease(ctx, user, agent, session, "pod-a/1", first); err != nil {
		t.Fatalf("stale release: %v", err)
	}
	stillOurs, ok, err := db.RenewSessionLease(ctx, user, agent, session, "pod-a/1", renewed, time.Minute)
	if err != nil || !ok {
		t.Fatal("the live holder's lease was freed by a stale release")
	}
	renewed = stillOurs // each renew bumps the token; the holder tracks it

	// Expiry is the displacement path (L3), and the takeover keeps the token
	// monotonic across possessions.
	if _, ok, err := db.AcquireSessionLease(ctx, user, "agt2", "sess2", "pod-a/1", time.Second); err != nil || !ok {
		t.Fatalf("short acquire: ok=%v err=%v", ok, err)
	}
	time.Sleep(1100 * time.Millisecond)
	taken, ok, err := db.AcquireSessionLease(ctx, user, "agt2", "sess2", "pod-b/2", time.Minute)
	if err != nil || !ok {
		t.Fatalf("takeover after expiry: ok=%v err=%v", ok, err)
	}
	if taken <= 1 {
		t.Fatalf("takeover epoch = %d, want > 1 (the token must not reset)", taken)
	}

	// A voluntary release frees the row for the peer immediately.
	if err := db.ReleaseSessionLease(ctx, user, agent, session, "pod-a/1", renewed); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, ok, err := db.AcquireSessionLease(ctx, user, agent, session, "pod-b/2", time.Minute); err != nil || !ok {
		t.Fatalf("acquire after release: ok=%v err=%v", ok, err)
	}
}

// One winner under concurrency: the CAS is the whole exclusion argument (L1).
func TestSessionLeaseConcurrentAcquireHasOneWinner(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	const holders = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wins := make(chan string, holders)
	for i := 0; i < holders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			holder := "pod-" + string(rune('a'+i)) + "/1"
			<-start
			if _, ok, err := db.AcquireSessionLease(ctx, "u1", "agt1", "sess1", holder, time.Minute); err == nil && ok {
				wins <- holder
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(wins)

	var winners []string
	for h := range wins {
		winners = append(winners, h)
	}
	if len(winners) != 1 {
		t.Fatalf("winners = %v (%d), want exactly 1", winners, len(winners))
	}
}

// TestSessionFenceRefusesASupersededWriter is the witness for obligation L4(a):
// the token is checked by the resource, in the same statement as the write, so
// a turn that lost the lease updates/inserts ZERO rows and is told — it cannot
// overwrite, or archive into, the history another turn now owns.
//
// Falsification: drop the `EXISTS (SELECT 1 FROM session_turns …)` predicate
// from either statement and the matching assertion below stops failing (the
// stale writer lands its row again).
func TestSessionFenceRefusesASupersededWriter(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	const user, agent, session = "u1", "agt1", "sess1"
	epoch, ok, err := db.AcquireSessionLease(ctx, user, agent, session, "pod-a/1", time.Second)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	live := &SessionFence{HolderID: "pod-a/1", Epoch: epoch}

	// While the fence is live, both fenced writes land.
	if err := db.SaveSessionFenced(ctx, user, agent, session, &SessionRecord{Channel: "web"}, live); err != nil {
		t.Fatalf("fenced SaveSession with a live token: %v", err)
	}
	if err := db.AppendSessionMessageFenced(ctx, user, agent, session, SessionMessage{Role: "user", Content: "hi"}, live); err != nil {
		t.Fatalf("fenced AppendSessionMessage with a live token: %v", err)
	}

	// The lease lapses and a peer takes over.
	time.Sleep(1100 * time.Millisecond)
	next, ok, err := db.AcquireSessionLease(ctx, user, agent, session, "pod-b/2", time.Minute)
	if err != nil || !ok {
		t.Fatalf("takeover: ok=%v err=%v", ok, err)
	}
	if next <= epoch {
		t.Fatalf("takeover epoch = %d, want > %d", next, epoch)
	}

	// The superseded turn's writes are refused (0 rows), not silently accepted.
	if err := db.SaveSessionFenced(ctx, user, agent, session, &SessionRecord{Channel: "web"}, live); !errors.Is(err, ErrSessionFenceLost) {
		t.Fatalf("stale fenced SaveSession = %v, want ErrSessionFenceLost", err)
	}
	if err := db.AppendSessionMessageFenced(ctx, user, agent, session, SessionMessage{Role: "user", Content: "late"}, live); !errors.Is(err, ErrSessionFenceLost) {
		t.Fatalf("stale fenced AppendSessionMessage = %v, want ErrSessionFenceLost", err)
	}

	// The new holder's fence is accepted…
	if err := db.AppendSessionMessageFenced(ctx, user, agent, session, SessionMessage{Role: "user", Content: "mine"}, &SessionFence{HolderID: "pod-b/2", Epoch: next}); err != nil {
		t.Fatalf("the live holder's append was refused: %v", err)
	}
	// …and, for the installations that never wire a lease, the plain path is
	// exactly what it was (nil fence).
	if err := db.SaveSession(ctx, user, agent, session, &SessionRecord{Channel: "web"}); err != nil {
		t.Fatalf("unfenced SaveSession: %v", err)
	}
}

// A cancel request is a store write aimed at the LIVE possession (design X1–X6,
// docs/session-turn-integrity.md A4): the holder may run on another replica, so
// the request travels through the lease row and the holder reads it at its next
// iteration boundary. The predicate and the effect share one statement (L7), so
// a request that arrives after the possession lapsed writes nothing and cannot
// leak into the next turn.
//
// Falsification: drop `AND expires_at > now` from RequestSessionCancel and the
// "no live holder" case below starts reporting a cancel against a dead row
// (and the next acquisition would inherit it).
func TestSessionCancelTargetsOnlyTheLivePossession(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	const user, agent, session = "u1", "agt1", "sess-cancel"

	// Nobody holds the session yet: nothing to cancel.
	var live bool
	if ok, err := db.RequestSessionCancel(ctx, user, agent, session); err != nil || ok {
		t.Fatalf("cancel with no lease: ok=%v err=%v; want false/nil", ok, err)
	}

	epoch, ok, err := db.AcquireSessionLease(ctx, user, agent, session, "pod-a/1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	rec, err := db.GetSessionLease(ctx, user, agent, session)
	if err != nil || rec == nil {
		t.Fatalf("get: rec=%v err=%v", rec, err)
	}
	if rec.CancelRequested != 0 {
		t.Fatalf("a fresh possession starts with cancel_requested=%d, want 0", rec.CancelRequested)
	}

	// The request lands while the possession is live, and is idempotent.
	if ok, err := db.RequestSessionCancel(ctx, user, agent, session); err != nil || !ok {
		t.Fatalf("cancel on a live lease: ok=%v err=%v; want true/nil", ok, err)
	}
	if ok, err := db.RequestSessionCancel(ctx, user, agent, session); err != nil || !ok {
		t.Fatalf("second cancel: ok=%v err=%v; want true/nil (idempotent)", ok, err)
	}
	rec2, err := db.GetSessionLease(ctx, user, agent, session)
	if err != nil || rec2 == nil || rec2.CancelRequested == 0 {
		t.Fatalf("the request did not ride the row: rec=%+v err=%v", rec2, err)
	}
	if rec2.Epoch != epoch || rec2.HolderID != "pod-a/1" {
		t.Fatalf("the request changed ownership: %+v", rec2)
	}

	// After the possession lapses the request is refused, and the next
	// acquisition starts clean — no leak into the following turn.
	if err := db.ReleaseSessionLease(ctx, user, agent, session, "pod-a/1", epoch); err != nil {
		t.Fatalf("release: %v", err)
	}
	if ok, err := db.RequestSessionCancel(ctx, user, agent, session); err != nil || ok {
		t.Fatalf("cancel after release: ok=%v err=%v; want false/nil", ok, err)
	}
	if _, ok, err := db.AcquireSessionLease(ctx, user, agent, session, "pod-b/2", time.Minute); err != nil || !ok {
		t.Fatalf("re-acquire: ok=%v err=%v", ok, err)
	}
	live = true
	_ = live
	rec3, err := db.GetSessionLease(ctx, user, agent, session)
	if err != nil || rec3 == nil {
		t.Fatalf("get after re-acquire: rec=%v err=%v", rec3, err)
	}
	if rec3.CancelRequested != 0 {
		t.Fatalf("the next possession inherited a cancel request (%d) from the previous one", rec3.CancelRequested)
	}
}
