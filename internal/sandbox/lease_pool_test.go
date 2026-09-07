package sandbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type leaseCloseRecorder struct {
	mu     sync.Mutex
	closed []string
}

func (r *leaseCloseRecorder) add(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = append(r.closed, id)
}

func (r *leaseCloseRecorder) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.closed))
	copy(out, r.closed)
	return out
}

func testExecutor(rec *leaseCloseRecorder, sandboxID, token string) *E2BExecutor {
	ex := newAdoptedE2BExecutor(sandboxID, token, "tpl", time.Minute)
	ex.closeFn = func() error {
		rec.add(sandboxID)
		return nil
	}
	return ex
}

// fakeLeaseStore scripts the four lease operations so pool unit tests can
// drive each reconcile branch without a database.
type fakeLeaseStore struct {
	mu sync.Mutex

	getRec     *SandboxLeaseRecord
	getErr     error
	acquireRec *SandboxLeaseRecord
	acquired   bool
	acquireErr error

	renewCalls   int
	renewEpoch   int64
	renewErr     error
	renewMiss    bool
	releaseOK    bool
	releaseErr   error
	releaseCalls int
}

func (f *fakeLeaseStore) GetSandboxLease(_ context.Context, _ string) (*SandboxLeaseRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getRec, f.getErr
}

func (f *fakeLeaseStore) AcquireSandboxLease(
	_ context.Context,
	_, _, _, _, _ string,
	_ time.Duration,
) (*SandboxLeaseRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireErr != nil {
		return nil, false, f.acquireErr
	}
	return f.acquireRec, f.acquired, nil
}

func (f *fakeLeaseStore) RenewSandboxLease(
	_ context.Context,
	_, _, _ string,
	_ time.Duration,
) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls++
	if f.renewErr != nil {
		return 0, f.renewErr
	}
	if f.renewMiss {
		return 0, nil
	}
	if f.renewEpoch == 0 {
		f.renewEpoch = 2
	}
	return f.renewEpoch, nil
}

func (f *fakeLeaseStore) ReleaseSandboxLease(_ context.Context, _, _ string, _ int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	return f.releaseOK, f.releaseErr
}

func (f *fakeLeaseStore) renewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renewCalls
}

func (f *fakeLeaseStore) releaseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.releaseCalls
}

func newLeasePool(t *testing.T, store SandboxLeaseStore, owner string) *E2BExecutorPool {
	t.Helper()
	return NewE2BExecutorPool("api-key", "tpl", "", 5*time.Minute,
		WithSandboxLeases(E2BLeaseOptions{Store: store, Owner: owner, LeaseTTL: time.Minute}))
}

func TestE2BPoolReconcile_SameSandboxRenews(t *testing.T) {
	ctx := context.Background()
	store := &fakeLeaseStore{getRec: &SandboxLeaseRecord{SandboxID: "sb-1", EnvdToken: "tok-1", Template: "tpl", Epoch: 5}}
	pool := newLeasePool(t, store, "pod-a")
	rec := &leaseCloseRecorder{}
	ex := testExecutor(rec, "sb-1", "tok-1")
	const key = "agt_1:s:chat_1"
	pool.executors[key] = ex

	got, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != ex {
		t.Fatal("same-sandbox reconcile replaced the local executor")
	}
	if store.renewCount() != 1 {
		t.Fatalf("renew calls = %d, want 1", store.renewCount())
	}
	if got := pool.leaseEpochs["agt_1:s:chat_1"]; got != store.renewEpoch {
		t.Fatalf("lease epoch cached = %d, want %d", got, store.renewEpoch)
	}
	if len(rec.ids()) != 0 {
		t.Fatalf("same-sandbox reconcile closed executor: %v", rec.ids())
	}
}

func TestE2BPoolReconcile_StaleLocalAdoptsCurrent(t *testing.T) {
	ctx := context.Background()
	// Lease now points at sb-2 (another pod took over while we were idle);
	// our local cache still holds sb-1.
	store := &fakeLeaseStore{getRec: &SandboxLeaseRecord{SandboxID: "sb-2", EnvdToken: "tok-2", Template: "tpl", Epoch: 7}}
	pool := newLeasePool(t, store, "pod-a")
	rec := &leaseCloseRecorder{}
	pool.executors["agt_1:s:chat_1"] = testExecutor(rec, "sb-1", "tok-1")

	got, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	gotEx, ok := got.(*E2BExecutor)
	if !ok {
		t.Fatalf("unexpected executor type %T", got)
	}
	if gotEx.sandboxID != "sb-2" {
		t.Fatalf("adopted sandbox = %q, want sb-2", gotEx.sandboxID)
	}
	if ids := rec.ids(); len(ids) != 1 || ids[0] != "sb-1" {
		t.Fatalf("stale sb-1 should be closed exactly once, closed=%v", ids)
	}
	if store.renewCount() != 1 {
		t.Fatalf("adopt should renew once, renews=%d", store.renewCount())
	}
}

func TestE2BPoolReconcile_ExpiredReclaimsOwnSandbox(t *testing.T) {
	ctx := context.Background()
	store := &fakeLeaseStore{
		getRec:     nil, // lease expired/gone
		acquireRec: &SandboxLeaseRecord{SandboxID: "sb-1", EnvdToken: "tok-1", Template: "tpl", Epoch: 3},
		acquired:   true,
	}
	pool := newLeasePool(t, store, "pod-a")
	rec := &leaseCloseRecorder{}
	ex := testExecutor(rec, "sb-1", "tok-1")
	pool.executors["agt_1:s:chat_1"] = ex

	got, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != ex {
		t.Fatal("successful reclaim replaced the local executor")
	}
	if len(rec.ids()) != 0 {
		t.Fatalf("reclaim should not close anything: %v", rec.ids())
	}
}

func TestE2BPoolReconcile_ExpiredLostRaceAdoptsWinner(t *testing.T) {
	ctx := context.Background()
	store := &fakeLeaseStore{
		getRec:     nil,
		acquireRec: &SandboxLeaseRecord{SandboxID: "sb-2", EnvdToken: "tok-2", Template: "tpl", Epoch: 4},
		acquired:   false,
	}
	pool := newLeasePool(t, store, "pod-a")
	rec := &leaseCloseRecorder{}
	pool.executors["agt_1:s:chat_1"] = testExecutor(rec, "sb-1", "tok-1")

	got, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	gotEx := got.(*E2BExecutor)
	if gotEx.sandboxID != "sb-2" {
		t.Fatalf("adopted sandbox = %q, want sb-2", gotEx.sandboxID)
	}
	if ids := rec.ids(); len(ids) != 1 || ids[0] != "sb-1" {
		t.Fatalf("lost race should close local sb-1 once, closed=%v", ids)
	}
}

func TestE2BPoolReleaseUsesFencingEpoch(t *testing.T) {
	const key = "agt_1:s:chat_1"

	// Stale epoch → lease not deleted → sandbox must NOT be closed.
	stale := &fakeLeaseStore{releaseOK: false}
	p1 := newLeasePool(t, stale, "pod-a")
	rec1 := &leaseCloseRecorder{}
	p1.executors[key] = testExecutor(rec1, "sb-1", "tok-1")
	p1.leaseEpochs[key] = 3
	if err := p1.Release("agt_1", "", "chat_1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if len(rec1.ids()) != 0 {
		t.Fatalf("stale epoch release must not close sandbox, closed=%v", rec1.ids())
	}

	// Current owner + epoch → lease deleted → sandbox closed.
	ok := &fakeLeaseStore{releaseOK: true}
	p2 := newLeasePool(t, ok, "pod-a")
	rec2 := &leaseCloseRecorder{}
	p2.executors[key] = testExecutor(rec2, "sb-1", "tok-1")
	p2.leaseEpochs[key] = 5
	if err := p2.Release("agt_1", "", "chat_1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if ids := rec2.ids(); len(ids) != 1 || ids[0] != "sb-1" {
		t.Fatalf("owner release should close sandbox once, closed=%v", ids)
	}
}

func TestE2BPoolCreateLostRaceAdoptsWinner(t *testing.T) {
	ctx := context.Background()
	store := &fakeLeaseStore{
		getRec:     nil, // no lease yet → this pod creates
		acquireRec: &SandboxLeaseRecord{SandboxID: "sb-2", EnvdToken: "tok-2", Template: "tpl", Epoch: 4},
		acquired:   false, // another replica won the claim while we created
	}
	pool := newLeasePool(t, store, "pod-a")
	created := &leaseCloseRecorder{}
	pool.newSandboxExecutor = func(_ context.Context, _, _ string, _ time.Duration) (*E2BExecutor, error) {
		return testExecutor(created, "sb-1", "tok-1"), nil
	}
	pool.hydrateSandbox = func(context.Context, *E2BExecutor) error { return nil }
	pool.verifySandbox = func(context.Context, *E2BExecutor) error { return nil }
	pool.warmupSandbox = func(context.Context, *E2BExecutor) {}

	got, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	gotEx := got.(*E2BExecutor)
	if gotEx.sandboxID != "sb-2" {
		t.Fatalf("expected adopted sb-2, got %s", gotEx.sandboxID)
	}
	if ids := created.ids(); len(ids) != 1 || ids[0] != "sb-1" {
		t.Fatalf("locally created sb-1 must be closed exactly once after losing the race, closed=%v", ids)
	}
	if store.renewCount() != 1 {
		t.Fatalf("adoption should CAS-renew once, renews=%d", store.renewCount())
	}
	if epoch := pool.leaseEpochs["agt_1:s:chat_1"]; epoch != store.renewEpoch {
		t.Fatalf("cached epoch = %d, want %d", epoch, store.renewEpoch)
	}
}

// Fail-open contract (design doc: "Registry errors fail open: the sandbox is
// left alive"): a lease-lookup error on a fresh scope must still create and
// register a local sandbox, and an acquire error must keep it usable but
// unregistered so a later release can never destroy it.
func TestE2BPoolFreshGetLeaseErrorsFailOpen(t *testing.T) {
	t.Run("lookup error falls back to local create and registers", func(t *testing.T) {
		ctx := context.Background()
		store := &fakeLeaseStore{
			getErr:     errors.New("lease db down"),
			acquireRec: &SandboxLeaseRecord{SandboxID: "sb-1", EnvdToken: "tok-1", Template: "tpl", Epoch: 1},
			acquired:   true,
		}
		pool := newLeasePool(t, store, "pod-a")
		rec := &leaseCloseRecorder{}
		pool.newSandboxExecutor = func(_ context.Context, _, _ string, _ time.Duration) (*E2BExecutor, error) {
			return testExecutor(rec, "sb-1", "tok-1"), nil
		}
		pool.hydrateSandbox = func(context.Context, *E2BExecutor) error { return nil }
		pool.verifySandbox = func(context.Context, *E2BExecutor) error { return nil }
		pool.warmupSandbox = func(context.Context, *E2BExecutor) {}

		ex, err := pool.Get(ctx, "agt_1", "", "chat_1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got, ok := ex.(*E2BExecutor); !ok || got.sandboxID != "sb-1" {
			t.Fatalf("executor = %v, want local sb-1", ex)
		}
		if epoch, ok := pool.leaseEpochs["agt_1:s:chat_1"]; !ok || epoch != 1 {
			t.Fatalf("cached epoch = %d ok=%v, want 1", epoch, ok)
		}
		if len(rec.ids()) != 0 {
			t.Fatalf("local sandbox closed unexpectedly: %v", rec.ids())
		}
	})

	t.Run("acquire error keeps local sandbox unregistered", func(t *testing.T) {
		ctx := context.Background()
		store := &fakeLeaseStore{acquireErr: errors.New("lease write failed")}
		pool := newLeasePool(t, store, "pod-a")
		rec := &leaseCloseRecorder{}
		pool.newSandboxExecutor = func(_ context.Context, _, _ string, _ time.Duration) (*E2BExecutor, error) {
			return testExecutor(rec, "sb-1", "tok-1"), nil
		}
		pool.hydrateSandbox = func(context.Context, *E2BExecutor) error { return nil }
		pool.verifySandbox = func(context.Context, *E2BExecutor) error { return nil }
		pool.warmupSandbox = func(context.Context, *E2BExecutor) {}

		ex, err := pool.Get(ctx, "agt_1", "", "chat_1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got, ok := ex.(*E2BExecutor); !ok || got.sandboxID != "sb-1" {
			t.Fatalf("executor = %v, want local sb-1", ex)
		}
		if _, ok := pool.leaseEpochs["agt_1:s:chat_1"]; ok {
			t.Fatal("unregistered sandbox must not carry a lease epoch")
		}
		// Release with no epoch must not destroy the sandbox (fail open).
		if err := pool.Release("agt_1", "", "chat_1"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if len(rec.ids()) != 0 {
			t.Fatalf("unregistered sandbox was destroyed: %v", rec.ids())
		}
	})
}

// Adopt renew error must still hand back the adopted executor, but with no
// recorded epoch, so a later release cannot destroy a sandbox the pod never
// owned in the registry.
func TestE2BPoolAdoptRenewErrorKeepsExecutorWithoutEpoch(t *testing.T) {
	ctx := context.Background()
	store := &fakeLeaseStore{
		getRec:   &SandboxLeaseRecord{SandboxID: "sb-2", EnvdToken: "tok-2", Template: "tpl", Epoch: 7},
		renewErr: errors.New("renew failed"),
	}
	pool := newLeasePool(t, store, "pod-a")
	rec := &leaseCloseRecorder{}

	ex, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	gotEx, ok := ex.(*E2BExecutor)
	if !ok || gotEx.sandboxID != "sb-2" {
		t.Fatalf("executor = %v, want adopted sb-2", ex)
	}
	if _, ok := pool.leaseEpochs["agt_1:s:chat_1"]; ok {
		t.Fatal("adopt with renew error must not record an epoch")
	}
	if store.renewCount() != 1 {
		t.Fatalf("renew calls = %d, want 1", store.renewCount())
	}
	if len(rec.ids()) != 0 {
		t.Fatalf("nothing was closed: %v", rec.ids())
	}
}

// Reconcile failure modes on a cached executor all keep the local sandbox
// alive (never close it, never adopt blindly).
func TestE2BPoolReconcileRegistryErrorsKeepLocal(t *testing.T) {
	t.Run("lookup error", func(t *testing.T) {
		ctx := context.Background()
		store := &fakeLeaseStore{getErr: errors.New("lease db down")}
		pool := newLeasePool(t, store, "pod-a")
		rec := &leaseCloseRecorder{}
		ex := testExecutor(rec, "sb-1", "tok-1")
		pool.executors["agt_1:s:chat_1"] = ex

		got, err := pool.Get(ctx, "agt_1", "", "chat_1")
		if err != nil || got != ex {
			t.Fatalf("Get = %v err=%v, want cached executor", got, err)
		}
		if store.renewCount() != 0 || len(rec.ids()) != 0 {
			t.Fatalf("lookup error must not renew/close: renews=%d closed=%v", store.renewCount(), rec.ids())
		}
	})

	t.Run("reclaim acquire error", func(t *testing.T) {
		ctx := context.Background()
		store := &fakeLeaseStore{acquireErr: errors.New("lease write failed")}
		pool := newLeasePool(t, store, "pod-a")
		rec := &leaseCloseRecorder{}
		ex := testExecutor(rec, "sb-1", "tok-1")
		pool.executors["agt_1:s:chat_1"] = ex

		got, err := pool.Get(ctx, "agt_1", "", "chat_1")
		if err != nil || got != ex {
			t.Fatalf("Get = %v err=%v, want cached executor", got, err)
		}
		if _, ok := pool.leaseEpochs["agt_1:s:chat_1"]; ok {
			t.Fatal("failed reclaim must not record an epoch")
		}
		if len(rec.ids()) != 0 {
			t.Fatalf("reclaim error must not close local: %v", rec.ids())
		}
	})
}

// Double race (design doc "Known tradeoffs"): creator loses Acquire and the
// follow-up CAS adoption also misses — the pool keeps its own sandbox until
// the next reconcile, unregistered, and must not destroy anything.
func TestE2BPoolCreateLostRaceAdoptMissKeepsLocalUnregistered(t *testing.T) {
	ctx := context.Background()
	store := &fakeLeaseStore{
		getRec:     nil,
		acquireRec: &SandboxLeaseRecord{SandboxID: "sb-2", EnvdToken: "tok-2", Template: "tpl", Epoch: 4},
		acquired:   false,
		renewMiss:  true,
	}
	pool := newLeasePool(t, store, "pod-a")
	rec := &leaseCloseRecorder{}
	pool.newSandboxExecutor = func(_ context.Context, _, _ string, _ time.Duration) (*E2BExecutor, error) {
		return testExecutor(rec, "sb-1", "tok-1"), nil
	}
	pool.hydrateSandbox = func(context.Context, *E2BExecutor) error { return nil }
	pool.verifySandbox = func(context.Context, *E2BExecutor) error { return nil }
	pool.warmupSandbox = func(context.Context, *E2BExecutor) {}

	ex, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	gotEx := ex.(*E2BExecutor)
	if gotEx.sandboxID != "sb-1" {
		t.Fatalf("expected to keep local sb-1 after double race, got %s", gotEx.sandboxID)
	}
	if _, ok := pool.leaseEpochs["agt_1:s:chat_1"]; ok {
		t.Fatal("double-race local sandbox must not carry an epoch")
	}
	if len(rec.ids()) != 0 {
		t.Fatalf("local sandbox closed after double race: %v", rec.ids())
	}
}

// Release/CloseAll registry failures must leave the sandbox alive (fail
// open), even though the pool drops its local reference.
func TestE2BPoolReleaseRegistryErrorLeavesSandboxAlive(t *testing.T) {
	const key = "agt_1:s:chat_1"
	store := &fakeLeaseStore{releaseErr: errors.New("lease db down")}
	pool := newLeasePool(t, store, "pod-a")
	rec := &leaseCloseRecorder{}
	pool.executors[key] = testExecutor(rec, "sb-1", "tok-1")
	pool.leaseEpochs[key] = 5

	if err := pool.Release("agt_1", "", "chat_1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(rec.ids()) != 0 {
		t.Fatalf("registry error must not destroy sandbox, closed=%v", rec.ids())
	}
	if store.releaseCount() != 1 {
		t.Fatalf("release calls = %d, want 1", store.releaseCount())
	}
}

func TestE2BPoolCloseAllHonorsLeaseStore(t *testing.T) {
	const key1 = "agt_1:s:chat_1"
	const key2 = "agt_2:s:chat_2"

	t.Run("no lease store closes all", func(t *testing.T) {
		pool := NewE2BExecutorPool("api-key", "tpl", "", 5*time.Minute)
		rec := &leaseCloseRecorder{}
		pool.executors[key1] = testExecutor(rec, "sb-1", "tok-1")
		pool.executors[key2] = testExecutor(rec, "sb-2", "tok-2")
		pool.CloseAll()
		if ids := rec.ids(); len(ids) != 2 {
			t.Fatalf("CloseAll closed %v, want both sandboxes", ids)
		}
		if len(pool.executors) != 0 || len(pool.leaseEpochs) != 0 {
			t.Fatal("CloseAll left stale maps")
		}
	})

	t.Run("registry declines deletion, sandboxes survive", func(t *testing.T) {
		store := &fakeLeaseStore{releaseOK: false}
		pool := newLeasePool(t, store, "pod-a")
		rec := &leaseCloseRecorder{}
		pool.executors[key1] = testExecutor(rec, "sb-1", "tok-1")
		pool.leaseEpochs[key1] = 3
		pool.executors[key2] = testExecutor(rec, "sb-2", "tok-2")
		pool.leaseEpochs[key2] = 4
		pool.CloseAll()
		if len(rec.ids()) != 0 {
			t.Fatalf("CloseAll destroyed shared sandboxes: %v", rec.ids())
		}
		if store.releaseCount() != 2 {
			t.Fatalf("release calls = %d, want 2", store.releaseCount())
		}
		if len(pool.executors) != 0 || len(pool.leaseEpochs) != 0 {
			t.Fatal("CloseAll left stale maps")
		}
	})
}
