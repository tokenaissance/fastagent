package sandbox

// The pool's locking discipline, pinned as behavior.
//
// Get used to hold one process-wide mutex across its network calls — lease
// reads, create, hydrate, verify and warmup (bounded at 120s) — so a cold start
// for a single scope delayed sandbox binding for every other agent in the
// process. The slow work now runs under a per-scope lock.
//
// The property is asserted without consulting the lock implementation: hold
// provisioning open and count how many *different* scopes are inside it at the
// same time. A process-wide lock admits exactly one; per-scope locks admit one
// per scope (a couple of stripes may collide, which only makes the count
// smaller — never 1 by construction, with 32 scopes over 64 stripes).

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestE2BPoolProvisionsScopesConcurrently(t *testing.T) {
	ctx := context.Background()
	const scopes = 32

	pool := NewE2BExecutorPool("api-key", "tpl", "", time.Minute)
	rec := &leaseCloseRecorder{}
	pool.newSandboxExecutor = func(_ context.Context, _, _ string, _ time.Duration, _ map[string]string) (*E2BExecutor, error) {
		ex := testExecutor(rec, "sb-x", "tok-x")
		ex.client = &http.Client{Transport: &fakeEnvdTransport{}}
		return ex, nil
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	var inFlight int32
	twoInside := make(chan struct{})
	var twoOnce sync.Once
	pool.hydrateSandbox = func(_ context.Context, _ *E2BExecutor) error {
		if atomic.AddInt32(&inFlight, 1) >= 2 {
			twoOnce.Do(func() { close(twoInside) })
		}
		<-release
		atomic.AddInt32(&inFlight, -1)
		return nil
	}
	pool.verifySandbox = func(context.Context, *E2BExecutor) error { return nil }
	pool.warmupSandbox = func(context.Context, *E2BExecutor) {}

	var wg sync.WaitGroup
	errs := make([]error, scopes)
	for i := 0; i < scopes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = pool.Get(ctx, fmt.Sprintf("agent-%d", i), "", "sess")
		}(i)
	}

	select {
	case <-twoInside:
		// Two scopes were provisioning at once — the property holds.
	case <-time.After(5 * time.Second):
		t.Fatalf("only %d scope(s) ever provisioned at once: sandbox binding is serialized process-wide",
			atomic.LoadInt32(&inFlight))
	}

	releaseAll()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("scope %d Get: %v", i, err)
		}
	}
	if ids := rec.ids(); len(ids) != 0 {
		t.Fatalf("a successful provisioning closed its sandbox: %v", ids)
	}
}
