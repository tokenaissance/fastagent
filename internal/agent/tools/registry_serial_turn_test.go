package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// A queued serial call belongs to the TURN it was emitted in, and that turn's
// deadline is the only clock that should release it. Its context has no deadline
// of its own — the loop hands tools a grace window that outlives the turn on
// purpose — so the wait reads the deadline the loop stamped. Without that, a
// call waiting behind another session's sub-agent sat until the grace expired,
// 60 seconds after nobody was waiting for it (delegate_task, 2026-09-14).
func TestRegisterSerialWaitEndsWithTheTurnDeadline(t *testing.T) {
	r := NewRegistry("", "")
	inside := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	r.RegisterSerial("serial_tool", "test", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
		if string(args) == `"first"` {
			close(inside)
			<-release
		}
		return "ok", nil
	})
	fn := r.GetFunc("serial_tool")

	go func() { _, _ = fn(context.Background(), json.RawMessage(`"first"`)) }()
	<-inside

	// The waiting call's context: no deadline of its own (the grace window), and
	// a turn deadline 150ms out.
	waitCtx := WithTurnDeadline(context.Background(), time.Now().Add(150*time.Millisecond))
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := fn(waitCtx, json.RawMessage(`"second"`))
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want DeadlineExceeded", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("the wait ended after %s — it must end with the turn, not with the grace window", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the queued call never returned: it is waiting on the grace window, not on the turn")
	}
}

// The stamp is a fact about the turn, not a deadline on the tool context: a tool
// that is already running keeps its grace window.
func TestTurnDeadlineDoesNotShortenTheToolContext(t *testing.T) {
	turn := time.Now().Add(time.Minute)
	ctx := WithTurnDeadline(context.Background(), turn)
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("stamping must not give the tool context a deadline — the grace window depends on it having none")
	}
	left, ok := TurnRemaining(ctx)
	if !ok || left <= 0 || left > time.Minute {
		t.Fatalf("TurnRemaining = %s (ok=%v)", left, ok)
	}
	if _, ok := TurnRemaining(context.Background()); ok {
		t.Fatal("no turn clock must report nothing, not zero")
	}
}
