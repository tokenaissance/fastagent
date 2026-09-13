package sandbox

// Orphan reaping: what the pool is allowed to destroy, and what must survive.
//
// The rule has three conjuncts and every one of them is a test here, because
// the failure mode of getting it wrong is destroying a sandbox that holds
// someone's work. The provider calls are stubbed at the seam (policy) and at
// HTTP (wire), matching how the rest of this package is tested.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// reapProvider scripts the two provider calls the reaper makes and records
// what it was asked to destroy.
type reapProvider struct {
	mu sync.Mutex
	// listed is what GET /v2/sandboxes answers.
	listed  []e2bSandboxInfo
	listErr error
	// askedStates records the state filter the pool sent.
	askedStates []string
	listCalls   int
	destroyed   []string
	// destroyErr fails the delete for one sandbox id.
	destroyErr map[string]error
}

func (p *reapProvider) list(_ context.Context, _, state string) ([]e2bSandboxInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listCalls++
	p.askedStates = append(p.askedStates, state)
	if p.listErr != nil {
		return nil, p.listErr
	}
	out := make([]e2bSandboxInfo, len(p.listed))
	copy(out, p.listed)
	return out, nil
}

func (p *reapProvider) destroy(_ context.Context, _, sandboxID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.destroyed = append(p.destroyed, sandboxID)
	return p.destroyErr[sandboxID]
}

func (p *reapProvider) destroyedIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.destroyed))
	copy(out, p.destroyed)
	return out
}

// newReapPool builds a tagged pool with a one-minute lease TTL, so the reap
// grace is two minutes and a test can place a row on either side of it.
func newReapPool(t *testing.T, store SandboxLeaseStore, tag string) *E2BExecutorPool {
	t.Helper()
	return NewE2BExecutorPool("api-key", "tpl", t.TempDir(), time.Minute,
		WithSandboxLeases(E2BLeaseOptions{
			Store: store, Owner: "pod-a", LeaseTTL: time.Minute, PoolTag: tag,
		}))
}

func tagged(id, state string) e2bSandboxInfo {
	return e2bSandboxInfo{SandboxID: id, State: state,
		Metadata: map[string]string{e2bPoolTagKey: "prod"}}
}

// The whole point: an instance this deployment created, paused, and no row
// claims any more, goes away.
func TestReapOrphansDestroysPausedUnclaimedInstances(t *testing.T) {
	now := time.Now().Unix()
	store := &fakeLeaseStore{refs: []SandboxLeaseRef{
		{SandboxID: "sb-live", ExpiresAt: now + 600},
		{SandboxID: "sb-orphan", ExpiresAt: now - 3600},
	}}
	pool := newReapPool(t, store, "prod")
	provider := &reapProvider{listed: []e2bSandboxInfo{
		tagged("sb-live", "paused"),
		tagged("sb-orphan", "paused"),
	}}
	pool.listSandboxes = provider.list
	pool.destroySandbox = provider.destroy

	reaped, err := pool.ReapOrphans(context.Background())
	if err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1", reaped)
	}
	if got := provider.destroyedIDs(); len(got) != 1 || got[0] != "sb-orphan" {
		t.Fatalf("destroyed = %v, want [sb-orphan]", got)
	}
	if len(provider.askedStates) != 1 || provider.askedStates[0] != e2bStatePaused {
		t.Fatalf("list states = %v, want [paused] — reaping must not walk running sandboxes",
			provider.askedStates)
	}
}

// A lease that lapsed moments ago still protects its instance: the serve path
// may yet resume it, and the provider filter cannot tell the two apart.
func TestReapOrphansKeepsInstancesASlippedLeaseStillClaims(t *testing.T) {
	now := time.Now().Unix()
	// Expired 30s ago; the grace is 2 × the one-minute lease TTL.
	store := &fakeLeaseStore{refs: []SandboxLeaseRef{{SandboxID: "sb-slipped", ExpiresAt: now - 30}}}
	pool := newReapPool(t, store, "prod")
	provider := &reapProvider{listed: []e2bSandboxInfo{tagged("sb-slipped", "paused")}}
	pool.listSandboxes = provider.list
	pool.destroySandbox = provider.destroy

	reaped, err := pool.ReapOrphans(context.Background())
	if err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if reaped != 0 || len(provider.destroyedIDs()) != 0 {
		t.Fatalf("reaped %d and destroyed %v, want nothing: the row still claims it",
			reaped, provider.destroyedIDs())
	}
}

// Past the grace window the same row stops protecting its instance — that is
// the abandoned-session case reaping exists for.
func TestReapOrphansReapsAfterTheGraceWindow(t *testing.T) {
	store := &fakeLeaseStore{refs: []SandboxLeaseRef{
		{SandboxID: "sb-abandoned", ExpiresAt: time.Now().Add(-3 * time.Minute).Unix()},
	}}
	pool := newReapPool(t, store, "prod")
	provider := &reapProvider{listed: []e2bSandboxInfo{tagged("sb-abandoned", "paused")}}
	pool.listSandboxes = provider.list
	pool.destroySandbox = provider.destroy

	reaped, err := pool.ReapOrphans(context.Background())
	if err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1 (lease lapsed past the grace window)", reaped)
	}
}

// Ownership is what keeps one deployment out of another's sandboxes: an e2b
// account can be shared, and "paused and unnamed" describes their live
// sandboxes exactly as well as our orphans.
func TestReapOrphansLeavesSandboxesItCannotProveItCreated(t *testing.T) {
	store := &fakeLeaseStore{} // no row claims anything
	pool := newReapPool(t, store, "prod")
	provider := &reapProvider{listed: []e2bSandboxInfo{
		{ // another deployment's sandbox in the same account
			SandboxID: "sb-other-deploy", State: "paused",
			Metadata: map[string]string{e2bPoolTagKey: "staging"},
		},
		{ // created before this deployment was tagged
			SandboxID: "sb-untagged", State: "paused",
		},
		{ // no metadata at all
			SandboxID: "sb-no-meta", State: "paused", Metadata: nil,
		},
	}}
	pool.listSandboxes = provider.list
	pool.destroySandbox = provider.destroy

	reaped, err := pool.ReapOrphans(context.Background())
	if err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if reaped != 0 || len(provider.destroyedIDs()) != 0 {
		t.Fatalf("reaped %d and destroyed %v, want nothing without our tag",
			reaped, provider.destroyedIDs())
	}
}

// The state filter is an optimization, not the rule. A provider that answers
// with something still running must not have it destroyed.
func TestReapOrphansNeverDestroysARunningSandbox(t *testing.T) {
	store := &fakeLeaseStore{}
	pool := newReapPool(t, store, "prod")
	provider := &reapProvider{listed: []e2bSandboxInfo{tagged("sb-running", "running")}}
	pool.listSandboxes = provider.list
	pool.destroySandbox = provider.destroy

	if _, err := pool.ReapOrphans(context.Background()); err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if got := provider.destroyedIDs(); len(got) != 0 {
		t.Fatalf("destroyed = %v, want nothing: a running sandbox may hold work", got)
	}
}

// Without a tag there is no set the pool can prove it owns, so it must not
// even ask the provider — an untagged fleet reaps nothing rather than guessing.
func TestReapOrphansDisabledWithoutAPoolTag(t *testing.T) {
	store := &fakeLeaseStore{}
	pool := newReapPool(t, store, "")
	provider := &reapProvider{listed: []e2bSandboxInfo{tagged("sb-orphan", "paused")}}
	pool.listSandboxes = provider.list
	pool.destroySandbox = provider.destroy

	reaped, err := pool.ReapOrphans(context.Background())
	if err != nil {
		t.Fatalf("ReapOrphans: %v", err)
	}
	if reaped != 0 || provider.listCalls != 0 || len(provider.destroyedIDs()) != 0 {
		t.Fatalf("reaped=%d listCalls=%d destroyed=%v, want a no-op",
			reaped, provider.listCalls, provider.destroyedIDs())
	}
}

// Per-pod mode (no lease store) has no naming authority to consult, so there
// is nothing to reap against.
func TestReapOrphansWithoutALeaseStoreIsANoOp(t *testing.T) {
	pool := NewE2BExecutorPool("api-key", "tpl", t.TempDir(), time.Minute)
	provider := &reapProvider{listed: []e2bSandboxInfo{tagged("sb-orphan", "paused")}}
	pool.listSandboxes = provider.list
	pool.destroySandbox = provider.destroy

	reaped, err := pool.ReapOrphans(context.Background())
	if err != nil || reaped != 0 {
		t.Fatalf("ReapOrphans = (%d, %v), want (0, nil)", reaped, err)
	}
	if provider.listCalls != 0 {
		t.Fatalf("list calls = %d, want 0", provider.listCalls)
	}
}

// Fail closed: if the naming authority cannot answer, every paused instance
// looks like an orphan. Reaping then would destroy sandboxes a sibling pod is
// about to resume.
func TestReapOrphansFailsClosedWhenTheRegistryCannotAnswer(t *testing.T) {
	store := &fakeLeaseStore{refsErr: errors.New("db down")}
	pool := newReapPool(t, store, "prod")
	provider := &reapProvider{listed: []e2bSandboxInfo{tagged("sb-live", "paused")}}
	pool.listSandboxes = provider.list
	pool.destroySandbox = provider.destroy

	reaped, err := pool.ReapOrphans(context.Background())
	if err == nil {
		t.Fatal("ReapOrphans: want an error when the lease table is unreadable")
	}
	if reaped != 0 || len(provider.destroyedIDs()) != 0 {
		t.Fatalf("reaped=%d destroyed=%v, want nothing destroyed without the table",
			reaped, provider.destroyedIDs())
	}
}

// One failed delete must not abandon the rest of the pass, and the caller has
// to be able to see which instance failed.
func TestReapOrphansKeepsGoingAfterAFailedDestroy(t *testing.T) {
	now := time.Now().Unix()
	store := &fakeLeaseStore{refs: []SandboxLeaseRef{
		{SandboxID: "sb-a", ExpiresAt: now - 3600},
		{SandboxID: "sb-b", ExpiresAt: now - 3600},
	}}
	pool := newReapPool(t, store, "prod")
	provider := &reapProvider{
		listed:     []e2bSandboxInfo{tagged("sb-a", "paused"), tagged("sb-b", "paused")},
		destroyErr: map[string]error{"sb-a": errors.New("HTTP 500")},
	}
	pool.listSandboxes = provider.list
	pool.destroySandbox = provider.destroy

	reaped, err := pool.ReapOrphans(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sb-a") {
		t.Fatalf("err = %v, want one naming sb-a", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1", reaped)
	}
	if got := provider.destroyedIDs(); len(got) != 2 {
		t.Fatalf("destroyed = %v, want both attempted", got)
	}
}

// roundTripFunc adapts a closure to http.RoundTripper, so the listing can be
// driven page by page without a server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The wire contract: v2 endpoint, the paused filter, the page size, and the
// cursor header followed to the end.
func TestListE2BSandboxesPaginatesThePausedFilter(t *testing.T) {
	var urls []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		urls = append(urls, r.URL.String())
		if got := r.Header.Get("X-API-Key"); got != "api-key" {
			t.Errorf("X-API-Key = %q, want api-key", got)
		}
		var body string
		header := http.Header{"Content-Type": []string{"application/json"}}
		switch r.URL.Query().Get("nextToken") {
		case "":
			body = `[{"sandboxID":"sb-1","state":"paused","metadata":{"fastagent_pool":"prod"}}]`
			header.Set("X-Next-Token", "page2")
		case "page2":
			body = `[{"sandboxID":"sb-2","state":"paused","metadata":{"fastagent_pool":"prod"}}]`
		default:
			t.Errorf("unexpected nextToken %q", r.URL.Query().Get("nextToken"))
		}
		return &http.Response{
			StatusCode: 200,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})}

	got, err := listE2BSandboxes(context.Background(), client, "api-key", e2bStatePaused)
	if err != nil {
		t.Fatalf("listE2BSandboxes: %v", err)
	}
	if len(got) != 2 || got[0].SandboxID != "sb-1" || got[1].SandboxID != "sb-2" {
		t.Fatalf("listed = %+v, want sb-1 and sb-2", got)
	}
	if got[1].Metadata[e2bPoolTagKey] != "prod" {
		t.Fatalf("metadata = %v, want the pool tag carried through", got[1].Metadata)
	}
	if len(urls) != 2 {
		t.Fatalf("requests = %d (%v), want 2 pages", len(urls), urls)
	}
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if u.Path != "/v2/sandboxes" {
			t.Fatalf("path = %q, want /v2/sandboxes (v1 has no state filter)", u.Path)
		}
		if got := u.Query().Get("state"); got != e2bStatePaused {
			t.Fatalf("state = %q, want paused", got)
		}
		if got := u.Query().Get("limit"); got != "100" {
			t.Fatalf("limit = %q, want 100", got)
		}
	}
}

// The tag has to survive a rebuild: recreate() mints a new instance, and one
// that lost its metadata would never be reaped.
func TestRebuildCarriesThePoolTagIntoTheCreateCall(t *testing.T) {
	pool := newReapPool(t, &fakeLeaseStore{}, "prod")
	ex := newAdoptedE2BExecutor("api-key", "sb-old", "tok-old", "tpl", time.Minute)
	ex.createMeta = pool.createMetadata()
	ex.client = &http.Client{Transport: &fakeEnvdTransport{deadSandboxIDs: []string{"sb-old"}}}
	var seen map[string]string
	ex.createFn = func(
		_ context.Context, _, _ string, _ time.Duration, meta map[string]string,
	) (*E2BExecutor, error) {
		seen = meta
		return newAdoptedE2BExecutor("api-key", "sb-new", "tok-new", "tpl", time.Minute), nil
	}

	if err := ex.recreateIfCurrent(context.Background(), ex.identSnapshot()); err != nil {
		t.Fatalf("recreateIfCurrent: %v", err)
	}
	if seen[e2bPoolTagKey] != "prod" {
		t.Fatalf("create metadata = %v, want the pool tag", seen)
	}
}

// createMetadata is what makes the two above true; nil (not an empty map) when
// no tag is configured, so an untagged deployment's create body is unchanged.
func TestCreateMetadataIsNilWithoutAPoolTag(t *testing.T) {
	if got := NewE2BExecutorPool("k", "tpl", "", time.Minute).createMetadata(); got != nil {
		t.Fatalf("untagged createMetadata = %v, want nil", got)
	}
	pool := newReapPool(t, &fakeLeaseStore{}, "prod")
	if got := pool.createMetadata(); got[e2bPoolTagKey] != "prod" {
		t.Fatalf("createMetadata = %v, want the configured tag", got)
	}
}

// The create body carries the tag as instance metadata — the only place that
// outlives the lease row.
func TestE2BCreateBodyCarriesPoolMetadata(t *testing.T) {
	var body map[string]any
	if err := json.Unmarshal(
		e2bCreateBody("tpl", time.Minute, map[string]string{e2bPoolTagKey: "prod"}), &body); err != nil {
		t.Fatalf("create body is not valid JSON: %v", err)
	}
	meta, ok := body["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %#v, want an object", body["metadata"])
	}
	if meta[e2bPoolTagKey] != "prod" {
		t.Fatalf("metadata = %v, want fastagent_pool=prod", meta)
	}
}
