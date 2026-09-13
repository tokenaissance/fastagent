package sandbox

// The post-create routing gap.
//
// POST /sandboxes returns an id before e2b's edge can route it. The first call
// after create is hydrate's /files upload, and it used to receive the edge's
//
//	{"sandboxId":...,"message":"The sandbox was not found","code":502}
//
// — byte-identical to the answer for a sandbox that died long ago. A transient
// gap therefore looked like a dead instance, and the caller's recovery
// (rebuild) could never converge: every rebuild created another sandbox and hit
// the same window. waitUntilRoutable absorbs the gap inside creation, where it
// is a retry.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestE2BWaitUntilRoutable(t *testing.T) {
	ctx := context.Background()

	t.Run("ready on the first try", func(t *testing.T) {
		envd := &fakeEnvdTransport{}
		ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
		ex.client = &http.Client{Transport: envd}

		if err := ex.waitUntilRoutable(ctx); err != nil {
			t.Fatalf("waitUntilRoutable: %v", err)
		}
		if got := envd.attempts(); got != 1 {
			t.Fatalf("attempts = %d, want 1", got)
		}
	})

	t.Run("retries until the edge knows the id", func(t *testing.T) {
		envd := &fakeEnvdTransport{routableAfter: 2}
		ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
		ex.client = &http.Client{Transport: envd}
		ex.readyTimeout = 5 * time.Second
		ex.readyInterval = time.Millisecond

		if err := ex.waitUntilRoutable(ctx); err != nil {
			t.Fatalf("waitUntilRoutable: %v", err)
		}
		if got := envd.attempts(); got != 3 {
			t.Fatalf("attempts = %d, want 3 (two 'not found' answers, then success)", got)
		}
	})

	t.Run("gives up naming the sandbox and the bound", func(t *testing.T) {
		envd := &fakeEnvdTransport{deadSandboxIDs: []string{"sb-1"}}
		ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
		ex.client = &http.Client{Transport: envd}
		ex.readyTimeout = 40 * time.Millisecond
		ex.readyInterval = 5 * time.Millisecond

		err := ex.waitUntilRoutable(ctx)
		if err == nil {
			t.Fatal("a sandbox that never becomes routable must fail creation")
		}
		if !strings.Contains(err.Error(), "never became routable") || !strings.Contains(err.Error(), "sb-1") {
			t.Fatalf("error should name the sandbox and the wait, got %q", err)
		}
		if got := envd.attempts(); got < 2 {
			t.Fatalf("attempts = %d, want retries before giving up", got)
		}
	})

	t.Run("a non-gone provider verdict fails fast", func(t *testing.T) {
		envd := &fakeEnvdTransport{
			brokenSandboxIDs: []string{"sb-1"},
			brokenStatus:     http.StatusUnauthorized,
			brokenBody:       `{"code":401,"message":"access token is invalid"}`,
		}
		ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
		ex.client = &http.Client{Transport: envd}
		ex.readyTimeout = 5 * time.Second
		ex.readyInterval = time.Millisecond

		err := ex.waitUntilRoutable(ctx)
		if err == nil {
			t.Fatal("a 401 must fail creation, not be retried")
		}
		if !strings.Contains(err.Error(), "not usable after create") {
			t.Fatalf("error should say the sandbox is unusable, got %q", err)
		}
		if got := envd.attempts(); got != 1 {
			t.Fatalf("attempts = %d, want 1: a rebuild cannot fix a 401", got)
		}
	})
}
