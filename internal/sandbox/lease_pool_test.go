package sandbox

import (
	"context"
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

	renewCalls int
	renewEpoch int64
	releaseOK  bool
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
	return f.acquireRec, f.acquired, f.acquireErr
}

func (f *fakeLeaseStore) RenewSandboxLease(
	_ context.Context,
	_, _, _ string,
	_ time.Duration,
) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls++
	if f.renewEpoch == 0 {
		f.renewEpoch = 2
	}
	return f.renewEpoch, nil
}

func (f *fakeLeaseStore) ReleaseSandboxLease(_ context.Context, _, _ string, _ int64) (bool, error) {
	return f.releaseOK, nil
}

func (f *fakeLeaseStore) renewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renewCalls
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
