package sandbox

// Witness for 《认知哲学的数学原理》§19.5.8.9（P-WAD 登记表第 1 行，路 1 受控实验）,
// and for the σ that landed on top of it the same day (docs 10 §4 G32, path ①).
//
// The prediction, written down before this file first ran (2026-09-21 13:46):
//
//	P-WAD-1. When the pool's fencing-epoch mirror (p.leaseEpochs) holds no
//	epoch for a scope — the shape produced by a registry error during
//	acquire/adopt, where registerExecutor runs and recordEpoch does not — a
//	Release of that scope is SILENT: it returns nil, it does not destroy the
//	sandbox, and it emits no σ. Stronger form: the caller cannot tell
//	"another holder fenced me out" (correct) from "I released nothing"
//	(a leak) — two different worlds, one observable.
//
// It held: measured at 13:47 (nil / zero destroys / zero log lines, item-by-item
// identical to the correctly-fenced release, and the surviving row still named
// the releasing pod). The other half held too: the three events P-WAD lists
// (restart / eviction / takeover) do NOT produce this gap on this carrier —
// restart takes the executor away with the map, and a sibling takeover
// re-records an epoch before it can release. The fourth subtest below is that
// discriminating negative; if it ever fails, the trigger condition was written
// wrong, not the σ.
//
// This file pins the SILENT behavior: the observation, not a wish. The strong
// form of the prediction held — with no σ anywhere on the path, a release that
// acted on no lease row and a release that a sibling correctly fenced out
// produce the same observable, item by item, and the row the pod failed to
// delete still names the pod itself.
//
// The falsifier is one line: put a σ (a probe) on the `epoch == 0` branch of
// releaseExecutor and this file goes red at the last assertion — "the two
// worlds are distinguishable". Until that line exists nothing here is fixed;
// this file is the measurement, and it is what any fix has to satisfy.
//
// What it does NOT say: that the gap only fires on a registry error (that would
// need fault injection), and that any production behavior changed — Release
// still returns nil in the `epoch == 0` branch.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// leaseRowStore is a lease store whose ReleaseSandboxLease is row-accurate:
// it reports deleted=true only when the caller presents the exact
// (scope, owner, epoch) triple the row currently holds — the same predicate as
// the SQL in internal/store/sandbox_leases.go:237. The existing fakeLeaseStore
// returns a scripted bool, which is exactly the detail under test here.
type leaseRowStore struct {
	mu       sync.Mutex
	scopeKey string
	owner    string
	row      *SandboxLeaseRecord
	// acquireErr models a registry failure that may have landed the write
	// anyway: the real AcquireSandboxLease runs UPDATE, INSERT, then a read-back
	// SELECT, so a failure after the write leaves a row nobody on the caller
	// side knows the epoch of. When set, the row is written *and* the error is
	// returned — the caller cannot confirm its own write.
	acquireErr error
	deletes    int     // rows actually deleted
	presented  []int64 // the epoch argument of every Release call
}

func (s *leaseRowStore) GetSandboxLease(_ context.Context, scopeKey string) (*SandboxLeaseRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil || scopeKey != s.scopeKey {
		return nil, nil
	}
	rec := *s.row
	return &rec, nil
}

func (s *leaseRowStore) AcquireSandboxLease(
	_ context.Context,
	scopeKey, owner, sandboxID, envdToken, template string,
	ttl time.Duration,
) (*SandboxLeaseRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil {
		s.scopeKey = scopeKey
		s.owner = owner
		s.row = &SandboxLeaseRecord{
			SandboxID: sandboxID, EnvdToken: envdToken, Template: template,
			State: "running", ExpiresAt: time.Now().Add(ttl).Unix(), Epoch: 1,
		}
		rec := *s.row
		if s.acquireErr != nil {
			return nil, false, s.acquireErr
		}
		return &rec, true, nil
	}
	if s.acquireErr != nil {
		return nil, false, s.acquireErr
	}
	rec := *s.row
	return &rec, rec.SandboxID == sandboxID, nil
}

func (s *leaseRowStore) RenewSandboxLease(
	_ context.Context,
	scopeKey, owner, sandboxID string,
	ttl time.Duration,
) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil || scopeKey != s.scopeKey || s.row.SandboxID != sandboxID {
		return 0, nil // CAS miss: the scope moved or is gone
	}
	s.owner = owner
	s.row.Epoch++
	s.row.ExpiresAt = time.Now().Add(ttl).Unix()
	return s.row.Epoch, nil
}

func (s *leaseRowStore) ReplaceSandboxLease(
	_ context.Context,
	scopeKey, owner, sandboxID, envdToken, template string,
	ttl time.Duration,
) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil || scopeKey != s.scopeKey || s.owner != owner {
		return 0, nil
	}
	s.row.SandboxID, s.row.EnvdToken, s.row.Template = sandboxID, envdToken, template
	s.row.Epoch++
	return s.row.Epoch, nil
}

func (s *leaseRowStore) SetSandboxLeaseState(_ context.Context, _, _, _ string) error {
	return nil
}

func (s *leaseRowStore) SetSandboxLeaseUnhydrated(_ context.Context, _, _, _ string, _ bool) error {
	return nil
}

// ReleaseSandboxLease mirrors the SQL: only the current (scope, owner, epoch)
// triple deletes the row.
func (s *leaseRowStore) ReleaseSandboxLease(_ context.Context, scopeKey, owner string, epoch int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presented = append(s.presented, epoch)
	if s.row == nil || scopeKey != s.scopeKey || s.owner != owner || s.row.Epoch != epoch {
		return false, nil
	}
	s.row = nil
	s.deletes++
	return true, nil
}

func (s *leaseRowStore) state() (owner string, epoch int64, live bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil {
		return "", 0, false
	}
	return s.owner, s.row.Epoch, true
}

// leaseObservable is what a caller can see after Release: everything the
// release path exposes. If two different worlds produce the same triple, the
// caller cannot tell them apart.
type leaseObservable struct {
	err    error
	closes int
	logs   []string
}

func (o leaseObservable) equal(other leaseObservable) bool {
	return o.err == other.err && o.closes == other.closes && len(o.logs) == len(other.logs)
}

func (o leaseObservable) String() string {
	return "err=" + errString(o.err) + " closes=" + itoa(o.closes) + " logs=" + strings.Join(o.logs, " | ")
}

func errString(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// captureLogs runs fn with the default logger redirected and returns every
// record it emitted. A σ, if one existed, would have to appear here.
func captureLogs(fn func()) []string {
	var buf bytes.Buffer
	prev := slog.Default()
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(prev)
	fn()
	out := []string{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func observedRelease(t *testing.T, pool *E2BExecutorPool, rec *leaseCloseRecorder, agentID, projectID, sessionID string) leaseObservable {
	t.Helper()
	var err error
	logs := captureLogs(func() {
		err = pool.Release(agentID, projectID, sessionID)
	})
	return leaseObservable{err: err, closes: len(rec.ids()), logs: logs}
}

// newEpochGapPool wires a pool against the row-accurate store with every
// network seam stubbed.
func newEpochGapPool(t *testing.T, store *leaseRowStore, owner string, rec *leaseCloseRecorder) *E2BExecutorPool {
	t.Helper()
	pool := newLeasePool(t, store, owner)
	pool.newSandboxExecutor = func(_ context.Context, _, _ string, _ time.Duration) (*E2BExecutor, error) {
		return testExecutor(rec, "sb-1", "tok-1"), nil
	}
	pool.newAdoptedExecutor = func(_, sandboxID, token, _ string, _ time.Duration) *E2BExecutor {
		return testExecutor(rec, sandboxID, token)
	}
	pool.hydrateSandbox = func(context.Context, *E2BExecutor) error { return nil }
	pool.verifySandbox = func(context.Context, *E2BExecutor) error { return nil }
	pool.warmupSandbox = func(context.Context, *E2BExecutor) {}
	return pool
}

func TestPWAD1_ReleaseWithNoRecordedEpochIsSilent(t *testing.T) {
	ctx := context.Background()
	const key = "agt_1:s:chat_1"

	var (
		normal     leaseObservable
		fenced     leaseObservable
		epochless  leaseObservable
		takenOver  leaseObservable
		leakedRow  string
		leakedLive bool
	)

	// ① Control: epoch recorded, row held by us — release must destroy.
	t.Run("control: a recorded epoch releases the row and destroys the sandbox", func(t *testing.T) {
		store := &leaseRowStore{}
		rec := &leaseCloseRecorder{}
		pool := newEpochGapPool(t, store, "pod-a", rec)
		if _, err := pool.Get(ctx, "agt_1", "", "chat_1"); err != nil {
			t.Fatalf("Get: %v", err)
		}
		if epoch, ok := pool.leaseEpochs[key]; !ok || epoch != 1 {
			t.Fatalf("recorded epoch = %d ok=%v, want 1", epoch, ok)
		}
		normal = observedRelease(t, pool, rec, "agt_1", "", "chat_1")
		if _, _, live := store.state(); live {
			t.Fatal("control: the lease row survived a release that had a recorded epoch")
		}
		if normal.closes != 1 {
			t.Fatalf("control: destroyed %d sandboxes, want 1 (%s)", normal.closes, normal)
		}
		if normal.err != nil {
			t.Fatalf("control: release returned %v", normal.err)
		}
	})

	// ② Correct fencing: another owner holds the row. Deleted=false is the
	// designed answer, and the sandbox must not be destroyed.
	t.Run("takeover: a fenced-out release does nothing and says nothing", func(t *testing.T) {
		store := &leaseRowStore{}
		rec := &leaseCloseRecorder{}
		pool := newEpochGapPool(t, store, "pod-a", rec)
		if _, err := pool.Get(ctx, "agt_1", "", "chat_1"); err != nil {
			t.Fatalf("Get: %v", err)
		}
		store.mu.Lock() // the sibling replica adopted the scope
		store.owner = "pod-b"
		store.row.Epoch = 2
		store.mu.Unlock()
		fenced = observedRelease(t, pool, rec, "agt_1", "", "chat_1")
		if fenced.closes != 0 {
			t.Fatalf("a fenced-out release destroyed a sibling's sandbox (%d closes)", fenced.closes)
		}
	})

	// ③ The gap itself: the executor is registered, the epoch never was.
	// The registry error lands AFTER the row is written — so the pod is left
	// holding a sandbox on a row whose epoch it never learned.
	//
	// What is asserted here is the OBSERVATION, not a fix: the release returns
	// nil, destroys nothing, logs nothing — and the row it did not delete still
	// names this very pod.
	t.Run("the gap is silent: a nil release, no destroy, no log", func(t *testing.T) {
		store := &leaseRowStore{acquireErr: errors.New("lease db down")}
		rec := &leaseCloseRecorder{}
		pool := newEpochGapPool(t, store, "pod-a", rec)
		if _, err := pool.Get(ctx, "agt_1", "", "chat_1"); err != nil {
			t.Fatalf("Get: %v", err)
		}
		if _, ok := pool.leaseEpochs[key]; ok {
			t.Fatal("precondition failed: an epoch was recorded, so this is not the gap")
		}
		epochless = observedRelease(t, pool, rec, "agt_1", "", "chat_1")
		leakedRow, _, leakedLive = store.state()
		if !leakedLive {
			t.Fatal("the row was deleted: fail-open was supposed to leave it alone")
		}
		if epochless.closes != 0 {
			t.Fatalf("the sandbox was destroyed (%d closes); a pod that cannot prove ownership must not destroy", epochless.closes)
		}
		if epochless.err != nil {
			t.Fatalf("the gap said something (%v); the observation is that it says nothing", epochless.err)
		}
		if len(epochless.logs) != 0 {
			t.Fatalf("the gap logged something: %v", epochless.logs)
		}
		if store.presented[0] != 0 {
			t.Fatalf("epoch presented to the store = %d, want 0", store.presented[0])
		}
		if leakedRow != "pod-a" {
			t.Fatalf("the surviving row is owned by %q, want pod-a (the releasing pod itself)", leakedRow)
		}
	})

	// ④ The discriminating negative: a sibling that TAKES OVER records an
	// epoch, so its release is not silent. If this ever fails, the half of the
	// prediction that says "the three events do not fire on this carrier" is
	// what breaks — not the other half.
	t.Run("takeover by a sibling records an epoch and releases normally", func(t *testing.T) {
		store := &leaseRowStore{}
		recA := &leaseCloseRecorder{}
		poolA := newEpochGapPool(t, store, "pod-a", recA)
		if _, err := poolA.Get(ctx, "agt_1", "", "chat_1"); err != nil {
			t.Fatalf("pod-a Get: %v", err)
		}
		// pod-b has never seen this scope: a cold pool over the same store.
		recB := &leaseCloseRecorder{}
		poolB := newEpochGapPool(t, store, "pod-b", recB)
		if _, err := poolB.Get(ctx, "agt_1", "", "chat_1"); err != nil {
			t.Fatalf("pod-b Get: %v", err)
		}
		if epoch, ok := poolB.leaseEpochs[key]; !ok || epoch == 0 {
			t.Fatalf("the adopting pod recorded no epoch (epoch=%d ok=%v): the takeover DID produce the gap", epoch, ok)
		}
		takenOver = observedRelease(t, poolB, recB, "agt_1", "", "chat_1")
		if takenOver.closes != 1 {
			t.Fatalf("the adopting pod destroyed %d sandboxes, want 1 (%s)", takenOver.closes, takenOver)
		}
		if _, _, live := store.state(); live {
			t.Fatal("the adopting pod's release left its own row behind")
		}
	})

	// The strong form of the prediction, asserted as an equality: two different
	// worlds — "a sibling fenced me out" (correct, silent, nothing to do) and
	// "I never knew the epoch, so I deleted nothing while my own row survived"
	// (a leak) — produce ONE observable. That is the whole gap: the difference is
	// not something the caller can see. Putting a σ back must break this line.
	if !fenced.equal(epochless) {
		t.Fatalf("the two worlds are distinguishable, refuting the strong form:\n  fenced:    %s\n  epochless: %s",
			fenced, epochless)
	}
	if fenced.err != nil || fenced.closes != 0 || len(fenced.logs) != 0 {
		t.Fatalf("fencing is a correct outcome and must stay silent: %s", fenced)
	}
	if epochless.err != nil || epochless.closes != 0 || len(epochless.logs) != 0 {
		t.Fatalf("the gap is supposed to be silent; a σ appeared: %s", epochless)
	}
	t.Logf("observable (fenced)    = %s", fenced)
	t.Logf("observable (epochless) = %s", epochless)
	t.Logf("observable (control)   = %s", normal)
	t.Logf("observable (takeover)  = %s", takenOver)
	t.Logf("after the silent release the row is still owned by %q and the sandbox is still alive: %v",
		leakedRow, leakedLive)
}
