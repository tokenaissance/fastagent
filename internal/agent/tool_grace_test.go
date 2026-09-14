package agent

import (
	"context"
	"testing"
	"time"
)

// A cancelled turn must not take its in-flight tool down with it: the tool
// gets the grace window to finish, so the history records a real result
// instead of a synthetic pad. The turn itself is not extended — its own ctx
// stays cancelled, which is what stops the loop from starting another model
// round.
func TestToolGraceContextSurvivesCancellationForGrace(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	// Wide margins: the assertions are about the *contract* (the tool outlives
	// cancellation, and the grace is bounded), not about timer precision — a
	// loaded machine must not turn this into a flake.
	toolCtx, stop := toolGraceContext(parent, 3*time.Second)
	defer stop()

	cancelParent()
	select {
	case <-toolCtx.Done():
		t.Fatal("tool context ended immediately; the in-flight tool got no grace")
	case <-time.After(300 * time.Millisecond):
	}
	select {
	case <-toolCtx.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("tool context never ended; grace must be bounded")
	}
}

// The grace window is a ceiling, not a promise: finishing early must end the
// tool context immediately so a turn does not sit idle for the full window.
func TestToolGraceContextStopEndsImmediately(t *testing.T) {
	toolCtx, stop := toolGraceContext(context.Background(), 5*time.Second)
	stop()
	select {
	case <-toolCtx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stop() did not end the tool context")
	}
}

// With the grace disabled the tool context IS the turn context — the old
// behaviour, kept for callers that pass no grace.
func TestToolGraceContextDisabled(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	toolCtx, stop := toolGraceContext(parent, 0)
	defer stop()
	cancel()
	select {
	case <-toolCtx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("zero grace should pass the parent context through")
	}
}
