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

func (f *fakeLeaseStore) RenewSandboxLease(_ context.Context, _, _ string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls++
	return nil
}

func (f *fakeLeaseStore) ReleaseSandboxLease(_ context.Context, _, _ string) (bool, error) {
	return false, nil
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
	store := &fakeLeaseStore{getRec: &SandboxLeaseRecord{SandboxID: "sb-1", EnvdToken: "tok-1", Template: "tpl"}}
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
	if len(rec.ids()) != 0 {
		t.Fatalf("same-sandbox reconcile closed executor: %v", rec.ids())
	}
}

func TestE2BPoolReconcile_StaleLocalAdoptsCurrent(t *testing.T) {
	ctx := context.Background()
	// Lease now points at sb-2 (another pod took over while we were idle);
	// our local cache still holds sb-1.
	store := &fakeLeaseStore{getRec: &SandboxLeaseRecord{SandboxID: "sb-2", EnvdToken: "tok-2", Template: "tpl"}}
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
		acquireRec: &SandboxLeaseRecord{SandboxID: "sb-1", EnvdToken: "tok-1", Template: "tpl"},
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
		acquireRec: &SandboxLeaseRecord{SandboxID: "sb-2", EnvdToken: "tok-2", Template: "tpl"},
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
