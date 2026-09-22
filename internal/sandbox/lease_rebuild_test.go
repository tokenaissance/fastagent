package sandbox

// Rebuild ↔ lease coverage. recreate() changes the identity the shared lease
// is supposed to name, so the pool must move the row onto the replacement.
// These tests drive the whole rebuild path offline by stubbing envd (the
// outermost layer) and the create call, leaving hydrate → verify → publish —
// the part that can actually be wrong — running for real.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeEnvdTransport answers the only two envd endpoints recreate() touches:
// POST /files (hydrate bundle upload) and POST /process.Process/Start (the
// mkdir/chown script plus the /workspace writability probe). Stubbing at HTTP
// is deliberate — it is the outermost layer, so every line above it stays
// under test.
//
// The per-sandbox lists let a test stage the situation the invariant cares
// about: requests aimed at a dead instance ("sb-old") answer 502, so a rebuild
// is triggered, while requests aimed at a replacement behave normally.
type fakeEnvdTransport struct {
	// deadSandboxIDs answer every request with e2b's 502 for a vanished
	// instance — what a provider-side timeout looks like to a caller.
	deadSandboxIDs []string
	// brokenSandboxIDs accept the request but fail inside the sandbox (500),
	// which is how a replacement that cannot be hydrated shows up.
	brokenSandboxIDs []string
	// brokenBody is the body those failures return. Tests use it to prove the
	// rebuild decision reads the status code, not the message text.
	brokenBody string
	// brokenStatus is the status those failures answer with (0 → 500).
	brokenStatus int
	// deleteStatus answers Close()'s DELETE. Zero means 200.
	deleteStatus int
	// routableAfter makes the first N exec attempts answer 502 "The sandbox was
	// not found" before succeeding — the gap between create returning an id and
	// the edge being able to route it.
	routableAfter int
	// truncateFirstN makes the first N exec attempts answer 200 with a single
	// start frame and no exit-status trailer — a stream the sandbox cut while it
	// was still coming up.
	truncateFirstN int
	// trailerError, when set, is returned as a Connect end-stream trailer
	// holding {"error":{...}} — where the protocol carries a server-side error.
	trailerError string
	// control records control-plane calls (pause / timeout / connect) as
	// "METHOD path body".
	control []string
	// connectToken is what POST /sandboxes/{id}/connect answers with — the fresh
	// envd token a secure sandbox gets when it is resumed.
	connectToken string
	// unauthorizedFirstN makes the first N exec attempts answer 401: the shape
	// of a token the provider has superseded.
	unauthorizedFirstN int
	// missingPaths are paths envd answers 404 for — its verdict about a file,
	// not about the instance:
	//	{"code":404,"message":"path '/home/user/CURRENT.md' does not exist"}
	// Without this the fake answers 200 to every /files call, so a rebuild that
	// "fixed" a missing file would look like a successful empty read.
	missingPaths []string

	mu           sync.Mutex
	commands     []string
	execAttempts int
}

func (f *fakeEnvdTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil { // DELETE carries no body
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
	}
	// DELETE /sandboxes/<id> is the control-plane call Close() makes; it does
	// not go through envd, but it does come through this client.
	if req.Method == http.MethodDelete {
		status := f.deleteStatus
		if status == 0 {
			status = http.StatusOK
		}
		return envdResponse(req, status, []byte(`{"code":500,"message":"close refused"}`)), nil
	}
	// Control-plane calls share this client but not the envd framing.
	if strings.Contains(req.URL.Path, "/sandboxes/") {
		f.mu.Lock()
		f.control = append(f.control, fmt.Sprintf("%s %s %s", req.Method, req.URL.Path, strings.TrimSpace(string(body))))
		f.mu.Unlock()
		switch {
		case strings.HasSuffix(req.URL.Path, "/connect"):
			out, _ := json.Marshal(map[string]any{"sandboxID": "sb-1", "envdAccessToken": f.connectToken})
			return envdResponse(req, http.StatusOK, out), nil
		default: // /timeout, /pause
			return envdResponse(req, http.StatusNoContent, nil), nil
		}
	}
	if strings.Contains(req.URL.Path, "Process/Start") {
		f.mu.Lock()
		f.execAttempts++
		attempt := f.execAttempts
		f.mu.Unlock()
		if f.unauthorizedFirstN > 0 && attempt <= f.unauthorizedFirstN {
			return envdResponse(req, http.StatusUnauthorized,
				[]byte(`{"code":401,"message":"access token is invalid"}`)), nil
		}
		if f.trailerError != "" {
			// A real end-stream trailer: flags 0x02. connectEnvelope hardcodes
			// data frames, so a plain envelope here would exercise the raw-body
			// fallback instead of the trailer parser.
			body, _ := json.Marshal(map[string]any{"error": map[string]any{
				"code": "internal", "message": f.trailerError}})
			return envdResponse(req, http.StatusOK, connectTrailer(body)), nil
		}
		if f.routableAfter > 0 && attempt <= f.routableAfter {
			return envdResponse(req, http.StatusBadGateway,
				[]byte(`{"sandboxId":"sb-x","message":"The sandbox was not found","code":502}`)), nil
		}
		if f.truncateFirstN > 0 && attempt <= f.truncateFirstN {
			start, _ := json.Marshal(map[string]any{"event": map[string]any{
				"start": map[string]any{"pid": 42}}})
			return envdResponse(req, http.StatusOK, connectEnvelope(start)), nil
		}
	}
	for _, id := range f.deadSandboxIDs {
		if strings.Contains(req.URL.Host, id) {
			return envdResponse(req, http.StatusBadGateway, []byte(`{"code":502,"message":"sandbox not found"}`)), nil
		}
	}
	for _, id := range f.brokenSandboxIDs {
		if strings.Contains(req.URL.Host, id) {
			body := f.brokenBody
			if body == "" {
				body = `{"code":500,"message":"boom"}`
			}
			status := f.brokenStatus
			if status == 0 {
				status = http.StatusInternalServerError
			}
			return envdResponse(req, status, []byte(body)), nil
		}
	}
	if strings.Contains(req.URL.Path, "/files") {
		if p := req.URL.Query().Get("path"); p != "" {
			for _, missing := range f.missingPaths {
				if p == missing {
					body := fmt.Sprintf(`{"code":404,"message":"path '%s' does not exist"}`, p)
					return envdResponse(req, http.StatusNotFound, []byte(body)), nil
				}
			}
		}
		return envdResponse(req, http.StatusOK, nil), nil
	}
	if cmd := execCommandFrom(body); cmd != "" {
		f.mu.Lock()
		f.commands = append(f.commands, cmd)
		f.mu.Unlock()
	}
	// One stdout frame plus a clean exit trailer: exactly the shape execOnce
	// requires before it will call the process successful.
	stdout, _ := json.Marshal(map[string]any{"event": map[string]any{
		"data": map[string]any{"stdout": base64.StdEncoding.EncodeToString([]byte("ok"))}}})
	end, _ := json.Marshal(map[string]any{"event": map[string]any{
		"end": map[string]any{"exited": true, "status": "exit status 0"}}})
	return envdResponse(req, http.StatusOK, append(connectEnvelope(stdout), connectEnvelope(end)...)), nil
}

func (f *fakeEnvdTransport) ranCommand(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Contains(strings.Join(f.commands, "\n"), substr)
}

func (f *fakeEnvdTransport) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.execAttempts
}

func (f *fakeEnvdTransport) controlCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.control))
	copy(out, f.control)
	return out
}

func envdResponse(req *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/connect+json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}
}

// connectTrailer frames a payload as an end-stream (trailer) message: flags
// 0x02 rather than connectEnvelope's 0x00. That is where Connect carries a
// server-side error, and where the production code used to look away.
func connectTrailer(payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	buf[0] = 0x02
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	return buf
}

// execCommandFrom unwraps the Connect envelope of a Start request and returns
// the script that would have run.
func execCommandFrom(body []byte) string {
	if len(body) < 5 {
		return ""
	}
	var msg struct {
		Process struct {
			Args []string `json:"args"`
		} `json:"process"`
	}
	if err := json.Unmarshal(body[5:], &msg); err != nil || len(msg.Process.Args) < 2 {
		return ""
	}
	return msg.Process.Args[1]
}

// rebuildableExecutor is an executor whose sandbox can be replaced without
// network access: envd is faked and the replacement is minted by the closure.
func rebuildableExecutor(t *testing.T, rec *leaseCloseRecorder, sandboxID, token, replacementID, replacementToken string) (*E2BExecutor, *fakeEnvdTransport) {
	t.Helper()
	envd := &fakeEnvdTransport{}
	ex := testExecutor(rec, sandboxID, token)
	ex.client = &http.Client{Transport: envd}
	ex.createFn = func(context.Context, string, string, time.Duration) (*E2BExecutor, error) {
		return newAdoptedE2BExecutor("api-key", replacementID, replacementToken, "tpl", time.Minute), nil
	}
	return ex, envd
}

const rebuildScopeKey = "agt_1:s:chat_1"

// The invariant: an executor that replaced its own sandbox must move the lease
// onto the replacement. Before this, the row kept the dead id, and the next
// reconcile read that as "another pod took over" — closing the healthy
// replacement and adopting the corpse back on every single call.
func TestE2BPoolReconcileRepublishesRebuiltSandbox(t *testing.T) {
	ctx := context.Background()
	store := &fakeLeaseStore{}
	pool := newLeasePool(t, store, "pod-a")
	rec := &leaseCloseRecorder{}
	ex, envd := rebuildableExecutor(t, rec, "sb-old", "tok-old", "sb-new", "tok-new")
	pool.executors[rebuildScopeKey] = ex
	pool.leaseEpochs[rebuildScopeKey] = 3

	if err := ex.recreateIfCurrent(ctx, ex.identSnapshot(), nil); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if _, pending := ex.pendingPublish(); !pending {
		t.Fatal("recreate() must mark the executor as rebuilt")
	}
	if !envd.ranCommand("mkdir -p /skills /workspace") {
		t.Fatal("rebuild skipped hydration of the replacement")
	}
	if !envd.ranCommand(".fc-health") {
		t.Fatal("rebuild skipped the /workspace writability probe")
	}

	// The following call sees a row that still names the dead sandbox.
	store.getRec = &SandboxLeaseRecord{SandboxID: "sb-old", EnvdToken: "tok-old", Template: "tpl", Epoch: 3}
	got, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get after rebuild: %v", err)
	}
	if got != ex {
		t.Fatal("reconcile replaced the rebuilt executor instead of publishing it")
	}
	if _, pending := ex.pendingPublish(); pending {
		t.Fatal("rebuilt flag still set after the lease was updated")
	}
	if id := store.replaceID(0); id != "sb-new" {
		t.Fatalf("lease published sandbox %q, want sb-new", id)
	}
	if want := store.replaceEpoch; pool.leaseEpochs[rebuildScopeKey] != want {
		t.Fatalf("recorded epoch = %d, want %d", pool.leaseEpochs[rebuildScopeKey], want)
	}
	if ids := rec.ids(); len(ids) != 0 {
		t.Fatalf("healthy replacement was closed: %v", ids)
	}
}

// Registry failure must not cost availability: the rebuilt sandbox keeps
// serving, the flag stays set, and the next reconcile retries the publish.
func TestE2BPoolRebuildPublishFailureKeepsLocalAndRetries(t *testing.T) {
	ctx := context.Background()
	store := &fakeLeaseStore{replaceErr: errors.New("lease db down")}
	pool := newLeasePool(t, store, "pod-a")
	rec := &leaseCloseRecorder{}
	ex, _ := rebuildableExecutor(t, rec, "sb-old", "tok-old", "sb-new", "tok-new")
	pool.executors[rebuildScopeKey] = ex
	pool.leaseEpochs[rebuildScopeKey] = 3

	if err := ex.recreateIfCurrent(ctx, ex.identSnapshot(), nil); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	store.getRec = &SandboxLeaseRecord{SandboxID: "sb-old", EnvdToken: "tok-old", Template: "tpl", Epoch: 3}
	got, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil || got != ex {
		t.Fatalf("registry failure must keep the local executor: got=%v err=%v", got, err)
	}
	if _, pending := ex.pendingPublish(); !pending {
		t.Fatal("failed publish must leave the flag set so the next reconcile retries")
	}
	if ids := rec.ids(); len(ids) != 0 {
		t.Fatalf("registry failure closed the sandbox: %v", ids)
	}

	// Registry recovers → the retry publishes and clears the flag.
	store.replaceErr = nil
	if _, err := pool.Get(ctx, "agt_1", "", "chat_1"); err != nil {
		t.Fatalf("Get after recovery: %v", err)
	}
	if _, pending := ex.pendingPublish(); pending {
		t.Fatal("retry did not clear the rebuilt flag")
	}
	if store.replaceCount() != 2 {
		t.Fatalf("replace calls = %d, want 2 (failed + retry)", store.replaceCount())
	}
}

// If the scope moved to another pod while we were rebuilding, our replacement
// must not overwrite that row: the CAS misses and we adopt the winner.
func TestE2BPoolRebuildSupersededByAnotherPodAdoptsCurrent(t *testing.T) {
	ctx := context.Background()
	store := &fakeLeaseStore{replaceMiss: true}
	pool := newLeasePool(t, store, "pod-a")
	rec := &leaseCloseRecorder{}
	ex, _ := rebuildableExecutor(t, rec, "sb-old", "tok-old", "sb-new", "tok-new")
	pool.executors[rebuildScopeKey] = ex
	pool.leaseEpochs[rebuildScopeKey] = 3

	if err := ex.recreateIfCurrent(ctx, ex.identSnapshot(), nil); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	store.getRec = &SandboxLeaseRecord{SandboxID: "sb-other", EnvdToken: "tok-other", Template: "tpl", Epoch: 9}
	got, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	adopted, ok := got.(*E2BExecutor)
	if !ok {
		t.Fatalf("Get returned %T, want *E2BExecutor", got)
	}
	if adopted == ex {
		t.Fatal("lost CAS must fall through to adopting the current lease")
	}
	if adopted.identSnapshot().id != "sb-other" {
		t.Fatalf("adopted sandbox = %q, want sb-other", adopted.identSnapshot().id)
	}
	if ids := rec.ids(); len(ids) != 1 || ids[0] != "sb-new" {
		t.Fatalf("superseded replacement must be closed exactly once, closed=%v", ids)
	}
}

// Single-pod / docker / leases-disabled: there is no row to publish, so a
// rebuild must stay exactly what it was before.
func TestE2BPoolRebuildWithoutLeaseStoreIsInert(t *testing.T) {
	ctx := context.Background()
	pool := NewE2BExecutorPool("api-key", "tpl", "", 5*time.Minute)
	rec := &leaseCloseRecorder{}
	ex, _ := rebuildableExecutor(t, rec, "sb-old", "tok-old", "sb-new", "tok-new")
	pool.executors[rebuildScopeKey] = ex

	if err := ex.recreateIfCurrent(ctx, ex.identSnapshot(), nil); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	got, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != ex {
		t.Fatal("per-pod pool must keep handing back the same executor")
	}
	if ids := rec.ids(); len(ids) != 0 {
		t.Fatalf("per-pod rebuild closed the sandbox: %v", ids)
	}
}

// A lease that lapsed while the pod was idle is re-acquired with the
// executor's CURRENT sandbox — which publishes a rebuild just as surely as
// ReplaceSandboxLease does. The flag must not survive that route, or its
// contract ("set until the row names the replacement") would be false for it.
func TestE2BPoolRebuildPublishedByExpiryReacquire(t *testing.T) {
	ctx := context.Background()
	store := &fakeLeaseStore{
		getRec:     nil, // lapsed while idle
		acquired:   true,
		acquireRec: &SandboxLeaseRecord{SandboxID: "sb-new", EnvdToken: "tok-new", Template: "tpl", Epoch: 4},
	}
	pool := newLeasePool(t, store, "pod-a")
	rec := &leaseCloseRecorder{}
	ex, _ := rebuildableExecutor(t, rec, "sb-old", "tok-old", "sb-new", "tok-new")
	pool.executors[rebuildScopeKey] = ex

	if err := ex.recreateIfCurrent(ctx, ex.identSnapshot(), nil); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if _, pending := ex.pendingPublish(); !pending {
		t.Fatal("recreate() must mark the executor as rebuilt")
	}
	if _, err := pool.Get(ctx, "agt_1", "", "chat_1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, pending := ex.pendingPublish(); pending {
		t.Fatal("re-acquiring a lapsed lease stamps the current sandbox; the flag must clear")
	}
	if store.replaceCount() != 0 {
		t.Fatalf("replace calls = %d, want 0: the expiry path re-acquires, it does not replace", store.replaceCount())
	}
	if pool.leaseEpochs[rebuildScopeKey] != 4 {
		t.Fatalf("recorded epoch = %d, want 4", pool.leaseEpochs[rebuildScopeKey])
	}
}

// Concurrency (clause U). Parallel tool calls share one executor — the agent
// loop fans them out and only `delegate_task` is registered serial — so several
// goroutines can watch the same sandbox die and all decide to replace it.
// Unsynchronised, each one minted its own instance and every instance but the
// last was stranded: no lease row named it and nothing would ever close it.
// The rebuild must fan in to exactly one replacement, and every caller must
// still get its work done on it.
func TestE2BExecutorConcurrentRebuildMintsOneSandbox(t *testing.T) {
	ctx := context.Background()
	rec := &leaseCloseRecorder{}
	envd := &fakeEnvdTransport{deadSandboxIDs: []string{"sb-old"}}
	ex := testExecutor(rec, "sb-old", "tok-old")
	ex.client = &http.Client{Transport: envd}

	var creates int32
	ex.createFn = func(context.Context, string, string, time.Duration) (*E2BExecutor, error) {
		atomic.AddInt32(&creates, 1)
		return newAdoptedE2BExecutor("api-key", "sb-new", "tok-new", "tpl", time.Minute), nil
	}

	const callers = 4
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = ex.Exec(ctx, "echo hi", 10*time.Second)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent exec %d failed: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&creates); got != 1 {
		t.Fatalf("concurrent rebuild created %d sandboxes, want exactly 1", got)
	}
	if got := ex.identSnapshot().id; got != "sb-new" {
		t.Fatalf("executor identity = %q, want sb-new", got)
	}
	if ids := rec.ids(); len(ids) != 0 {
		t.Fatalf("concurrent rebuild destroyed a sandbox: %v", ids)
	}
}

// Failure path (clauses A and I). A rebuild whose hydrate or verification
// fails used to leave the executor pointing at an unusable new sandbox while
// the lease row still named the dead one — the next reconcile read that
// mismatch as a takeover, adopted the corpse back and closed the replacement,
// so every call paid a create + close cycle. The failed rebuild must instead
// leave memory and row agreeing, and must not leak the replacement.
func TestE2BExecutorFailedRebuildRestoresIdentityAndDestroysReplacement(t *testing.T) {
	ctx := context.Background()
	rec := &leaseCloseRecorder{}
	envd := &fakeEnvdTransport{brokenSandboxIDs: []string{"sb-new"}}
	ex := testExecutor(rec, "sb-old", "tok-old")
	ex.client = &http.Client{Transport: envd}
	ex.createFn = func(context.Context, string, string, time.Duration) (*E2BExecutor, error) {
		return newAdoptedE2BExecutor("api-key", "sb-new", "tok-new", "tpl", time.Minute), nil
	}

	if err := ex.recreateIfCurrent(ctx, ex.identSnapshot(), nil); err == nil {
		t.Fatal("a rebuild whose hydrate fails must surface the error")
	}
	if got := ex.identSnapshot().id; got != "sb-old" {
		t.Fatalf("identity after a failed rebuild = %q, want the previous sandbox back", got)
	}
	if _, pending := ex.pendingPublish(); pending {
		t.Fatal("a sandbox that never became usable must not be published")
	}
	if ids := rec.ids(); len(ids) != 1 || ids[0] != "sb-new" {
		t.Fatalf("the unusable replacement must be destroyed exactly once, destroyed=%v", ids)
	}

	// And the pool must not read the failed rebuild as a takeover: memory and
	// row name the same sandbox again, so the next use renews instead of
	// adopting the dead instance back and closing anything else.
	store := &fakeLeaseStore{getRec: &SandboxLeaseRecord{SandboxID: "sb-old", EnvdToken: "tok-old", Template: "tpl", Epoch: 3}}
	pool := newLeasePool(t, store, "pod-a")
	pool.executors[rebuildScopeKey] = ex
	got, err := pool.Get(ctx, "agt_1", "", "chat_1")
	if err != nil {
		t.Fatalf("Get after a failed rebuild: %v", err)
	}
	if got != ex {
		t.Fatal("reconcile adopted the dead sandbox back after a failed rebuild")
	}
	if ids := rec.ids(); len(ids) != 1 {
		t.Fatalf("a failed rebuild must not cause further closes: %v", ids)
	}
	if store.replaceCount() != 0 {
		t.Fatalf("replace calls = %d, want 0: there was nothing usable to publish", store.replaceCount())
	}
}
