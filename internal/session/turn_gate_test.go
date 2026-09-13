package session

import (
	"context"
	"testing"
	"time"
)

// The session owns exactly one turn slot. Two callers must never hold it at
// once: that is what let a cron-fired turn and a dashboard turn interleave
// their appends and poison the history (see docs/session-turn-integrity.md).
func TestAcquireTurnSerializesCallers(t *testing.T) {
	s := &Session{}

	if !s.AcquireTurn(context.Background()) {
		t.Fatal("first acquire should succeed on an idle session")
	}

	acquired := make(chan bool, 1)
	go func() { acquired <- s.AcquireTurn(context.Background()) }()

	select {
	case <-acquired:
		t.Fatal("second acquire succeeded while the first turn still held the slot")
	case <-time.After(150 * time.Millisecond):
	}

	s.ReleaseTurn()
	select {
	case ok := <-acquired:
		if !ok {
			t.Fatal("queued acquire returned false after the slot was released")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued acquire never woke up after release")
	}
	s.ReleaseTurn()
}

// Waiters are served in arrival order so a queued conversation keeps the
// order the user typed it in.
func TestAcquireTurnHandsOffFIFO(t *testing.T) {
	s := &Session{}
	if !s.AcquireTurn(context.Background()) {
		t.Fatal("first acquire failed")
	}

	order := make(chan string, 2)
	// Queue the waiters one at a time so the arrival order (and therefore
	// the expected handoff order) is deterministic.
	for _, name := range []string{"b", "c"} {
		name := name
		go func() {
			if s.AcquireTurn(context.Background()) {
				order <- name
			}
		}()
		want := 1
		if name == "c" {
			want = 2
		}
		deadline := time.Now().Add(2 * time.Second)
		for s.TurnWaiters() < want {
			if time.Now().After(deadline) {
				t.Fatalf("waiter %s never queued (waiters=%d)", name, s.TurnWaiters())
			}
			time.Sleep(time.Millisecond)
		}
	}

	s.ReleaseTurn()
	first := <-order
	s.ReleaseTurn()
	second := <-order

	if first != "b" || second != "c" {
		t.Fatalf("handoff order = %s, %s; want b, c", first, second)
	}
}

// A caller whose context dies while queued must give up without stranding
// the slot: the next caller has to be able to take it.
func TestAcquireTurnContextCancelDoesNotLeakSlot(t *testing.T) {
	s := &Session{}
	if !s.AcquireTurn(context.Background()) {
		t.Fatal("holder acquire failed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	waiterResult := make(chan bool, 1)
	go func() { waiterResult <- s.AcquireTurn(ctx) }()
	time.Sleep(50 * time.Millisecond)

	cancel()
	select {
	case ok := <-waiterResult:
		if ok {
			t.Fatal("canceled waiter reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled waiter never returned")
	}

	// The canceled waiter must not have consumed the slot.
	s.ReleaseTurn()
	if !s.AcquireTurn(context.Background()) {
		t.Fatal("slot leaked after a canceled waiter")
	}
	s.ReleaseTurn()
}

// A canceled wait that races the handoff must pass the slot on instead of
// keeping it (the waiter is gone; nobody would ever release it).
func TestAcquireTurnCancelAtHandoffDoesNotStrandSlot(t *testing.T) {
	for i := 0; i < 200; i++ {
		s := &Session{}
		if !s.AcquireTurn(context.Background()) {
			t.Fatal("holder acquire failed")
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan bool, 1)
		go func() { result <- s.AcquireTurn(ctx) }()
		time.Sleep(time.Millisecond)
		// Cancel and release at the same time: whichever order the runtime
		// picks, the slot must still end up free.
		cancel()
		s.ReleaseTurn()
		<-result
		if !s.AcquireTurn(context.Background()) {
			t.Fatalf("iteration %d: slot stranded after a cancel/handoff race", i)
		}
		s.ReleaseTurn()
	}
}
