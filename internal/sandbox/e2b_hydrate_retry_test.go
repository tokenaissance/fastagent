package sandbox

// Two gaps behind one production failure:
//
//	hydrate after recreate (sandboxID=…): hydrate sandbox dirs: e2b exec did not
//	exit cleanly (frames=1, bodyBytes=168): (no output — response stream
//	truncated before exit-status trailer)
//
// 1. The message could not be acted on. Trailers — the one place the Connect
//    protocol carries a server-side error — were parsed and thrown away, and
//    the raw body was never shown, so "truncated" was all anyone could say.
// 2. A sandbox created moments ago can cut one stream while it finishes
//    booting. Hydrate had no retry, so that single cut failed the rebuild.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExecReportsServerTrailerError(t *testing.T) {
	envd := &fakeEnvdTransport{trailerError: "sandbox is shutting down"}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: envd}

	_, err := ex.execOnce(context.Background(), "true", time.Second)
	if err == nil {
		t.Fatal("an error trailer must fail the exec")
	}
	// The point is that the trailer was PARSED, not that the words appear
	// somewhere in a body dump.
	if !strings.Contains(err.Error(), "server error: internal: sandbox is shutting down") {
		t.Fatalf("error must carry the parsed trailer, got %q", err)
	}
	var truncated *execStreamTruncatedError
	if !errors.As(err, &truncated) {
		t.Fatalf("error = %T, want *execStreamTruncatedError", err)
	}
}

func TestExecTruncatedStreamCarriesRawBody(t *testing.T) {
	envd := &fakeEnvdTransport{truncateFirstN: 1}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: envd}

	_, err := ex.execOnce(context.Background(), "true", time.Second)
	if err == nil {
		t.Fatal("a stream without its exit trailer must fail the exec")
	}
	// The frames it did receive are the only clue when the stream simply died,
	// so they have to survive into the message — parsed, not as framed bytes.
	if !strings.Contains(err.Error(), "frames=") || !strings.Contains(err.Error(), "start") {
		t.Fatalf("error must include the received frames, got %q", err)
	}
	if !sandboxUnusable(err) {
		t.Fatal("a truncated stream is the class a fresh sandbox produces once")
	}
}

func TestHydrateRetriesATruncatedStream(t *testing.T) {
	ctx := context.Background()
	envd := &fakeEnvdTransport{truncateFirstN: 1}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: envd}
	// An empty bundle keeps the test on the exec step (no tar upload).
	ex.SetHydrationSources(nil, nil, "agt", "", "sess")

	if err := ex.Hydrate(ctx); err != nil {
		t.Fatalf("hydrate should absorb one truncated stream: %v", err)
	}
	if got := envd.attempts(); got != 2 {
		t.Fatalf("exec attempts = %d, want 2 (one cut, one success)", got)
	}
}

func TestHydrateDoesNotRetryAVerdict(t *testing.T) {
	ctx := context.Background()
	// A 401 is the provider telling us the request is wrong; retrying it just
	// delays the report and burns another round trip.
	envd := &fakeEnvdTransport{
		brokenSandboxIDs: []string{"sb-1"},
		brokenStatus:     http.StatusUnauthorized,
		brokenBody:       `{"code":401,"message":"access token is invalid"}`,
	}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: envd}
	ex.SetHydrationSources(nil, nil, "agt", "", "sess")

	if err := ex.Hydrate(ctx); err == nil {
		t.Fatal("hydrate must fail on a 401")
	}
	if got := envd.attempts(); got != 1 {
		t.Fatalf("exec attempts = %d, want 1: a verdict is not retried", got)
	}
}

// sandboxUnusable is now asked by two callers — Hydrate (retry it) and the
// lifecycle layer (replace the instance over it, §7.4) — so its boundary is the
// contract between them: widen it and a failing command starts costing a
// sandbox and re-running side effects, narrow it and a genuinely broken
// instance is handed back for the next call.
func TestSandboxUnusableIsAboutTheInstanceNotTheCommand(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"no error at all", nil, false},
		{"a cut stream (the 2026-09-16 shape)", &execStreamTruncatedError{detail: "did not exit cleanly"}, true},
		{"wrapped cut stream", fmt.Errorf("exec: %w", &execStreamTruncatedError{detail: "cut"}), true},
		{"the instance is gone (502)", &sandboxHTTPError{op: "e2b exec", status: http.StatusBadGateway}, true},
		{"the instance is gone (404)", &sandboxHTTPError{op: "e2b exec", status: http.StatusNotFound}, true},
		{"the command said no", errors.New("exit code 1"), false},
		{"a verdict from the provider", &sandboxHTTPError{op: "e2b exec", status: http.StatusUnauthorized}, false},
		{"a plain transport error", errors.New("dial tcp: i/o timeout"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sandboxUnusable(tc.err); got != tc.want {
				t.Fatalf("sandboxUnusable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
