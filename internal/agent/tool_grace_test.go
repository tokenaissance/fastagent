package agent

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The cancellation a grace window performs is invisible downstream: it reaches
// a long tool as a bare "context canceled" (an exec stream cut mid-read shows
// up as `e2b exec body read: context canceled`), which reads exactly like a
// sandbox or provider fault. These two lines are what tell them apart in prod,
// so they are pinned here rather than left to inspection.
func TestToolGraceContextLogsWhyItCancelled(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	parent, cancelParent := context.WithCancel(context.Background())
	toolCtx, stop := toolGraceContext(parent, 50*time.Millisecond)
	defer stop()
	cancelParent()

	select {
	case <-toolCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("grace never expired; nothing to assert")
	}

	logs := buf.String()
	if !strings.Contains(logs, "letting it finish within the grace window") {
		t.Fatalf("no line naming the cause; logs=%q", logs)
	}
	// slog's text handler quotes values containing spaces — assert the rendered
	// field, i.e. what an operator will actually grep for in the log.
	if !strings.Contains(logs, `cause="context canceled"`) {
		t.Fatalf("the log does not name which context ended; logs=%q", logs)
	}
	if !strings.Contains(logs, "still running after its grace window") {
		t.Fatalf("no line for the tool that outlived the grace; logs=%q", logs)
	}
}

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
