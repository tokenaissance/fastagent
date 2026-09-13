package sandbox

// Two consequences of the paused-sandbox design meeting long operations and
// secure sandboxes:
//
//  1. With autoPause on, an expiry that lands MID-operation pauses the sandbox
//     and cuts our stream. An operation long enough to straddle the expiry
//     therefore has to move it first (set-timeout).
//  2. A secure sandbox's envd token can be superseded across a pause, and a 401
//     from envd is that signal — not a dead instance. The repair is to reconnect
//     for the current token and publish it, not to rebuild.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestE2BExtendTimeoutMovesTheExpiry(t *testing.T) {
	envd := &fakeEnvdTransport{}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: envd}

	if err := ex.ExtendTimeout(context.Background(), "sb-1", 12*time.Minute); err != nil {
		t.Fatalf("ExtendTimeout: %v", err)
	}
	calls := envd.controlCalls()
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "POST /sandboxes/sb-1/timeout") {
		t.Fatalf("control calls = %v, want a POST to /sandboxes/sb-1/timeout", calls)
	}
	// The API rewrites the TTL from the request, so the whole budget is sent.
	if !strings.Contains(calls[0], `"timeout":720`) {
		t.Fatalf("timeout payload = %q, want 720 seconds", calls[0])
	}
}

// scopeExtendingPool records the extension requests the lifecycle layer makes.
type scopeExtendingPool struct {
	fakePool
	extended []time.Duration
}

func (p *scopeExtendingPool) ExtendScope(_ context.Context, _, _, _ string, d time.Duration) error {
	p.extended = append(p.extended, d)
	return nil
}

func TestLifecycleExtendsTheSandboxBeforeALongOperation(t *testing.T) {
	inner := &scopeExtendingPool{fakePool: *newFakePool()}
	lp := NewLifecyclePool(inner, time.Hour, time.Hour)
	defer lp.CloseAll()

	ex, err := lp.Get(context.Background(), "agt", "", "sess")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := ex.Exec(context.Background(), "long build", 10*time.Minute); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(inner.extended) != 1 {
		t.Fatalf("extensions = %v, want one for a 10-minute operation", inner.extended)
	}
	if want := 10*time.Minute + extendSlack; inner.extended[0] != want {
		t.Fatalf("extended by %s, want the operation budget plus slack (%s)", inner.extended[0], want)
	}

	// A short operation must not pay for the round trip.
	if _, err := ex.Exec(context.Background(), "quick check", 5*time.Second); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(inner.extended) != 1 {
		t.Fatalf("extensions = %v, want no extension for a 5-second operation", inner.extended)
	}
}

func TestStaleEnvdTokenClassification(t *testing.T) {
	if !staleEnvdToken(&sandboxHTTPError{op: "e2b exec", status: http.StatusUnauthorized}) {
		t.Fatal("a 401 from envd must be treated as a superseded token")
	}
	if staleEnvdToken(&sandboxHTTPError{op: "e2b exec", status: http.StatusInternalServerError}) {
		t.Fatal("a 500 is not a token problem")
	}
	if staleEnvdToken(&sandboxHTTPError{op: "e2b exec", status: http.StatusNotFound}) {
		t.Fatal("a 404 is a gone sandbox, handled by the rebuild path")
	}
}

// The whole point of secure mode: when the token is superseded, the executor
// reconnects for the current one, keeps the SAME sandbox, and marks the row for
// republication — no rebuild, no new instance.
func TestE2BRefreshesASupersededEnvdToken(t *testing.T) {
	envd := &fakeEnvdTransport{unauthorizedFirstN: 1, connectToken: "tok-new"}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-old")
	ex.client = &http.Client{Transport: envd}

	out, err := ex.Exec(context.Background(), "echo hi", 10*time.Second)
	if err != nil {
		t.Fatalf("Exec should recover from a superseded token: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("out = %q, want the retried command's output", out)
	}
	if got := ex.identSnapshot().token; got != "tok-new" {
		t.Fatalf("token = %q, want the reconnected one", got)
	}
	if _, pending := ex.pendingPublish(); !pending {
		t.Fatal("the refreshed token must be published to the lease row")
	}
	if calls := envd.controlCalls(); len(calls) != 1 || !strings.Contains(calls[0], "/connect") {
		t.Fatalf("control calls = %v, want one connect", calls)
	}
}
