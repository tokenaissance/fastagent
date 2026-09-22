package sandbox

// Three failure classifications that used to be guesses.
//
// 1. "The sandbox is gone" was decided by matching message text, so any error
//    that merely mentioned the codes — a 500 whose body quotes "HTTP 404" —
//    cost a sandbox rebuild. Only a 502/404 status from envd means gone.
// 2. Destroying a sandbox assumed success and never read the answer, so a
//    rejected DELETE left a live instance that no lease row pointed at any
//    more and nothing would ever close.
// 3. The file endpoints spell a missing PATH with the same 404 the edge uses
//    for a missing instance, so every read of a file that was not there
//    rebuilt the sandbox it was read from.

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A failure that is not "gone" must surface instead of costing a rebuild.
func TestE2BExecDoesNotRebuildOnNonGoneFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"plain 500", `{"code":500,"message":"boom"}`},
		{"500 that quotes the codes", `{"code":500,"message":"upstream said HTTP 404 then HTTP 502"}`},
		{"401 from a stale token", `{"code":401,"message":"HTTP 404 is not the problem here"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			envd := &fakeEnvdTransport{brokenSandboxIDs: []string{"sb-live"}, brokenBody: tc.body}
			ex := testExecutor(&leaseCloseRecorder{}, "sb-live", "tok-live")
			ex.client = &http.Client{Transport: envd}
			var creates int32
			ex.createFn = func(context.Context, string, string, time.Duration) (*E2BExecutor, error) {
				atomic.AddInt32(&creates, 1)
				return newAdoptedE2BExecutor("api-key", "sb-new", "tok-new", "tpl", time.Minute), nil
			}

			if _, err := ex.Exec(ctx, "echo hi", 5*time.Second); err == nil {
				t.Fatal("a failing sandbox call must surface its error")
			}
			if got := atomic.LoadInt32(&creates); got != 0 {
				t.Fatalf("created %d sandboxes for a non-404/502 failure, want 0", got)
			}
			if got := ex.identSnapshot().id; got != "sb-live" {
				t.Fatalf("identity = %q, want the sandbox kept as-is", got)
			}
		})
	}
}

// envd answers a missing path with a 404 of its own. Reading it must not cost
// the sandbox: the instance is alive and the file is simply not there.
//
// Production, 09-22 07:29:40Z: two read_file calls for paths that did not exist
// rebuilt the sandbox (1.6 s, 79 skill files, 1.13 MB tar), the retry read the
// replacement whose workspace really did not have them, and the turn carried on
// against a fresh instance three times in 21 minutes.
//
// Falsification: classify the file endpoints with sandboxGone() and the create
// counter below is 1 instead of 0.
func TestE2BReadOfAMissingPathKeepsTheSandbox(t *testing.T) {
	envd := &fakeEnvdTransport{missingPaths: []string{"/home/user/CURRENT.md"}}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-live", "tok-live")
	ex.client = &http.Client{Transport: envd}
	var creates int32
	ex.createFn = func(context.Context, string, string, time.Duration) (*E2BExecutor, error) {
		atomic.AddInt32(&creates, 1)
		return newAdoptedE2BExecutor("api-key", "sb-new", "tok-new", "tpl", time.Minute), nil
	}

	_, err := ex.ReadFile(context.Background(), "/home/user/CURRENT.md")
	if err == nil {
		t.Fatal("a missing file is still an error — the caller has to hear about it")
	}
	// The verdict survives, path and all: the model is the one who has to stop
	// looking for that file.
	if !strings.Contains(err.Error(), "CURRENT.md") {
		t.Fatalf("the error must name the path it refused, got %q", err)
	}
	if got := atomic.LoadInt32(&creates); got != 0 {
		t.Fatalf("a missing path cost %d sandbox rebuild(s); want 0", got)
	}
	if got := ex.identSnapshot().id; got != "sb-live" {
		t.Fatalf("identity = %q, want the sandbox kept as-is", got)
	}
}

// The other half: a 404 that is NOT a verdict about a path is still the edge
// saying the instance is gone, and the repair path must stay open. Without
// this, the fix above would be indistinguishable from "never rebuild on 404".
func TestE2BReadOfAGoneInstanceStillRebuilds(t *testing.T) {
	envd := &fakeEnvdTransport{
		brokenSandboxIDs: []string{"sb-live"},
		brokenStatus:     http.StatusNotFound,
		brokenBody:       `{"code":404,"message":"sandbox not found"}`,
	}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-live", "tok-live")
	ex.client = &http.Client{Transport: envd}
	var creates int32
	ex.createFn = func(context.Context, string, string, time.Duration) (*E2BExecutor, error) {
		atomic.AddInt32(&creates, 1)
		replacement := newAdoptedE2BExecutor("api-key", "sb-new", "tok-new", "tpl", time.Minute)
		return replacement, nil
	}

	if _, err := ex.ReadFile(context.Background(), "/home/user/CURRENT.md"); err != nil {
		t.Fatalf("read against a replaced sandbox: %v", err)
	}
	if got := atomic.LoadInt32(&creates); got != 1 {
		t.Fatalf("created %d sandboxes for a gone instance; want exactly 1", got)
	}
	if got := ex.identSnapshot().id; got != "sb-new" {
		t.Fatalf("identity = %q, want the replacement", got)
	}
}

// The control-plane DELETE must be read, and 404 must count as success: the
// instance is not running, which is the entire point of the call.
func TestE2BCloseReadsTheAnswer(t *testing.T) {
	t.Run("2xx is success", func(t *testing.T) {
		envd := &fakeEnvdTransport{deleteStatus: http.StatusOK}
		ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
		// Replace the seam so the real HTTP path (and its status handling) runs.
		ex.closeSandboxFn = nil
		ex.client = &http.Client{Transport: envd}
		if err := ex.Close(); err != nil {
			t.Fatalf("Close on a 2xx answer: %v", err)
		}
	})

	t.Run("404 counts as already gone", func(t *testing.T) {
		envd := &fakeEnvdTransport{deleteStatus: http.StatusNotFound}
		ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
		ex.closeSandboxFn = nil
		ex.client = &http.Client{Transport: envd}
		if err := ex.Close(); err != nil {
			t.Fatalf("Close on 404 must succeed (instance is gone): %v", err)
		}
	})

	t.Run("rejected DELETE is an error", func(t *testing.T) {
		envd := &fakeEnvdTransport{deleteStatus: http.StatusUnauthorized}
		ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
		ex.closeSandboxFn = nil
		ex.client = &http.Client{Transport: envd}
		err := ex.Close()
		if err == nil {
			t.Fatal("a rejected DELETE must not be reported as a destroy")
		}
		if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "sb-1") {
			t.Fatalf("error should name the status and the sandbox, got %q", err)
		}
	})
}
