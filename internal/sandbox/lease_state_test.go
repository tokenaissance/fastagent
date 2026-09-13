package sandbox

// The running/paused marker's two consumers: the idle sweep writes it, and
// adoption reads it to wake a sandbox instead of rebuilding one.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSleepScopeRecordsThePausedState(t *testing.T) {
	store := &fakeLeaseStore{}
	pool := newLeasePool(t, store, "pod-a")
	envd := &fakeEnvdTransport{}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: envd}
	pool.executors[rebuildScopeKey] = ex
	pool.leaseEpochs[rebuildScopeKey] = 3

	paused, err := pool.SleepScope(context.Background(), "agt_1", "", "chat_1")
	if err != nil || !paused {
		t.Fatalf("SleepScope = (%v, %v), want (true, nil)", paused, err)
	}
	if calls := envd.controlCalls(); len(calls) != 1 || !strings.Contains(calls[0], "/pause") {
		t.Fatalf("control calls = %v, want one pause", calls)
	}
	if states := store.recordedStates(); len(states) != 1 || states[0] != "paused" {
		t.Fatalf("recorded states = %v, want [paused]", states)
	}
}

// A pause can coincide with a pending rebuild, and the lease renew that follows
// it publishes the replacement — which stamps the row 'running', because a
// rebuilt instance IS running. So the 'paused' write has to land last; the other
// order leaves the row describing a sleeping sandbox as awake.
func TestSleepRecordsPausedAfterPublishingARebuild(t *testing.T) {
	ctx := context.Background()
	store := &fakeLeaseStore{}
	pool := newLeasePool(t, store, "pod-a")
	envd := &fakeEnvdTransport{}
	ex := testExecutor(&leaseCloseRecorder{}, "sb-old", "tok-old")
	ex.client = &http.Client{Transport: envd}
	ex.createFn = func(context.Context, string, string, time.Duration) (*E2BExecutor, error) {
		return newAdoptedE2BExecutor("api-key", "sb-new", "tok-new", "tpl", time.Minute), nil
	}
	pool.executors[rebuildScopeKey] = ex
	pool.leaseEpochs[rebuildScopeKey] = 3

	// Rebuild, leaving the row (as production would) still naming the old id.
	if err := ex.recreateIfCurrent(ctx, ex.identSnapshot()); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	store.getRec = &SandboxLeaseRecord{SandboxID: "sb-old", EnvdToken: "tok-old", Template: "tpl", Epoch: 3}

	paused, err := pool.SleepScope(ctx, "agt_1", "", "chat_1")
	if err != nil || !paused {
		t.Fatalf("SleepScope = (%v, %v), want (true, nil)", paused, err)
	}

	ops := store.opLog()
	if len(ops) < 2 || ops[len(ops)-1] != "state=paused" {
		t.Fatalf("ops = %v, want the paused write last", ops)
	}
	var replaced bool
	for _, op := range ops[:len(ops)-1] {
		if op == "replace" {
			replaced = true
		}
	}
	if !replaced {
		t.Fatalf("ops = %v, want the pending rebuild published before the state write", ops)
	}
}

// Ownership is not transferred by a pause: the row's token, id and template all
// stay as they were, which is what lets the next holder resume this instance.
func TestAdoptingAPausedSandboxResumesIt(t *testing.T) {
	store := &fakeLeaseStore{getRec: &SandboxLeaseRecord{
		SandboxID: "sb-1", EnvdToken: "tok-old", Template: "tpl", State: "paused", Epoch: 1,
	}}
	pool := newLeasePool(t, store, "pod-b")
	envd := &fakeEnvdTransport{connectToken: "tok-new"}
	pool.newAdoptedExecutor = func(_, sandboxID, accessToken, template string, timeout time.Duration) *E2BExecutor {
		ex := testExecutor(&leaseCloseRecorder{}, sandboxID, accessToken)
		ex.client = &http.Client{Transport: envd}
		return ex
	}

	got, err := pool.Get(context.Background(), "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	ex, ok := got.(*E2BExecutor)
	if !ok {
		t.Fatalf("Get returned %T", got)
	}
	if id := ex.identSnapshot().id; id != "sb-1" {
		t.Fatalf("adopted sandbox = %q, want the paused one", id)
	}
	// The resume returns the current token, which supersedes whatever the row
	// held — this is the secure-mode half of the same step.
	if tok := ex.identSnapshot().token; tok != "tok-new" {
		t.Fatalf("token = %q, want the token from the resume", tok)
	}
	if calls := envd.controlCalls(); len(calls) != 1 || !strings.Contains(calls[0], "/connect") {
		t.Fatalf("control calls = %v, want one connect", calls)
	}
	if states := store.recordedStates(); len(states) != 1 || states[0] != "running" {
		t.Fatalf("recorded states = %v, want [running]", states)
	}
}

// A running sandbox must not pay a control-plane round trip on every adoption.
func TestAdoptingARunningSandboxDoesNotConnect(t *testing.T) {
	store := &fakeLeaseStore{getRec: &SandboxLeaseRecord{
		SandboxID: "sb-1", EnvdToken: "tok-1", Template: "tpl", State: "running", Epoch: 1,
	}}
	pool := newLeasePool(t, store, "pod-b")
	envd := &fakeEnvdTransport{connectToken: "tok-new"}
	pool.newAdoptedExecutor = func(_, sandboxID, accessToken, template string, timeout time.Duration) *E2BExecutor {
		ex := testExecutor(&leaseCloseRecorder{}, sandboxID, accessToken)
		ex.client = &http.Client{Transport: envd}
		return ex
	}

	if _, err := pool.Get(context.Background(), "agt_1", "", "chat_1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if calls := envd.controlCalls(); len(calls) != 0 {
		t.Fatalf("control calls = %v, want none for a running sandbox", calls)
	}
	if states := store.recordedStates(); len(states) != 0 {
		t.Fatalf("recorded states = %v, want none", states)
	}
}
