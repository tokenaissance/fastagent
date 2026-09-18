package sandbox

// H (docs 10 §4 G17): a project's file tree is ONE tree, its containers are per
// chat, and the preview's dev server may be running in any of them. Docker
// closes that gap with a bind mount; without one the store is the channel, so a
// write — and a delete — has to reach every live container of the project.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// recordingExecutor is a remote-workspace executor whose two side effects can be
// asserted: what it was asked to write and what it was asked to run.
type recordingExecutor struct {
	writes   map[string]string
	execs    []string
	writeErr error
	remoteWS bool
}

func newRecordingExecutor() *recordingExecutor {
	return &recordingExecutor{writes: map[string]string{}, remoteWS: true}
}

func (r *recordingExecutor) Exec(_ context.Context, cmd string, _ time.Duration) (string, error) {
	r.execs = append(r.execs, cmd)
	return "", nil
}
func (r *recordingExecutor) ReadFile(_ context.Context, path string) (string, error) {
	return r.writes[path], nil
}
func (r *recordingExecutor) WriteFile(_ context.Context, path, content string) (string, error) {
	if r.writeErr != nil {
		return "", r.writeErr
	}
	r.writes[path] = content
	return "", nil
}
func (r *recordingExecutor) ListDir(context.Context, string) (string, error) { return "", nil }
func (r *recordingExecutor) Backend() string                                 { return "recording" }
func (r *recordingExecutor) Close() error                                    { return nil }
func (r *recordingExecutor) IsRemoteWorkspace()                              {}

// fakeInnerPool is the inner pool shape the lifecycle layer expects: Get hands
// back whatever is registered for the scope, and the two live-set accessors
// answer from the same map.
type fakeInnerPool struct {
	byScope map[string]*recordingExecutor
}

func (p *fakeInnerPool) Get(_ context.Context, agentID, projectID, sessionID string) (Executor, error) {
	if ex, ok := p.byScope[poolKey(agentID, projectID, sessionID)]; ok {
		return ex, nil
	}
	return nil, errors.New("no such scope")
}
func (p *fakeInnerPool) Release(string, string, string) error { return nil }
func (p *fakeInnerPool) CloseAll()                            {}
func (p *fakeInnerPool) Backend() string                      { return "fake" }
func (p *fakeInnerPool) SetWorkspace(workspace.Store)         {}
func (p *fakeInnerPool) LiveExecutor(agentID, projectID, sessionID string) (Executor, bool) {
	ex, ok := p.byScope[poolKey(agentID, projectID, sessionID)]
	if !ok {
		return nil, false
	}
	return ex, true
}
func (p *fakeInnerPool) LiveProjectExecutors(agentID, projectID string) []Executor {
	prefix := agentID + ":p:" + projectID
	out := []Executor{}
	for key, ex := range p.byScope {
		if key == prefix || strings.HasPrefix(key, prefix+":") {
			out = append(out, ex)
		}
	}
	return out
}

func broadcastFixture(t *testing.T) (*LifecyclePool, *recordingExecutor, *recordingExecutor, *recordingExecutor, *recordingExecutor) {
	t.Helper()
	const (
		agent    = "agt_b"
		project  = "proj_b"
		chatA    = "chat_a"
		chatB    = "chat_b"
		otherPid = "proj_other"
	)
	inner := &fakeInnerPool{byScope: map[string]*recordingExecutor{}}
	primary := newRecordingExecutor()     // the container this turn's tools write into
	peer := newRecordingExecutor()        // a sibling chat's container
	projectSlot := newRecordingExecutor() // the container a console-started preview uses
	foreign := newRecordingExecutor()     // another project — must never be touched
	inner.byScope[poolKey(agent, project, chatA)] = primary
	inner.byScope[poolKey(agent, project, chatB)] = peer
	inner.byScope[poolKey(agent, project, "")] = projectSlot
	inner.byScope[poolKey(agent, otherPid, chatA)] = foreign

	lp := NewLifecyclePool(inner, 0, 0)
	store := workspace.NewLocalFS(t.TempDir())
	// Seeded at the PROJECT ROOT: that is where the file tools write in a coding
	// session (`scopeSessionID()` collapses to ""), and it is deliberately NOT the
	// scope the sandbox is keyed by — the whole point of G22 is that the caller
	// states the store scope instead of the pool inferring it from the container.
	if err := store.Put(context.Background(), agent, project, "", "app/x.tsx",
		strings.NewReader("old"), -1, ""); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	lp.SetWorkspace(store)
	return lp, primary, peer, projectSlot, foreign
}

func TestWriteThroughReachesEveryContainerOfTheProject(t *testing.T) {
	ctx := context.Background()
	lp, primary, peer, projectSlot, foreign := broadcastFixture(t)
	sc := sandboxScope{agentID: "agt_b", projectID: "proj_b", sessionID: "chat_a"}

	outcome, err := lp.WriteThrough(ctx, sc, StoreScope{ProjectID: "proj_b", SessionID: ""}, "app/x.tsx", "/workspace/app/x.tsx", "new", "")
	if err != nil {
		t.Fatalf("write-through: %v", err)
	}
	if outcome.BroadcastFailures != 0 {
		t.Fatalf("broadcast failures = %d; want 0", outcome.BroadcastFailures)
	}
	for name, ex := range map[string]*recordingExecutor{
		"primary": primary, "sibling chat": peer, "project slot": projectSlot,
	} {
		if got := ex.writes["/workspace/app/x.tsx"]; got != "new" {
			t.Fatalf("%s container holds %q; want the new content (H: the store is the channel)", name, got)
		}
	}
	if got := foreign.writes["/workspace/app/x.tsx"]; got != "" {
		t.Fatalf("another project's container was written: %q", got)
	}
	// The stamp rides along, so a peer's later reconcile still takes the cheap
	// size+mtime path instead of comparing bytes.
	if len(peer.execs) == 0 || !strings.Contains(peer.execs[len(peer.execs)-1], "touch -d @") {
		t.Fatalf("peer execs = %v; want a touch stamp", peer.execs)
	}
}

func TestWriteThroughCountsAContainerItCouldNotReach(t *testing.T) {
	ctx := context.Background()
	lp, _, peer, _, _ := broadcastFixture(t)
	peer.writeErr = errors.New("peer is gone")

	outcome, err := lp.WriteThrough(ctx,
		sandboxScope{agentID: "agt_b", projectID: "proj_b", sessionID: "chat_a"},
		StoreScope{ProjectID: "proj_b", SessionID: ""},
		"app/x.tsx", "/workspace/app/x.tsx", "new", "")
	if err != nil {
		t.Fatalf("a peer failure must not fail the tool call: %v", err)
	}
	if outcome.BroadcastFailures != 1 {
		t.Fatalf("broadcast failures = %d; want 1 (the tool layer has to be able to say so)", outcome.BroadcastFailures)
	}
}

// G22: the stamp reads the store with the scope the CALLER stated, not with the
// scope the sandbox happens to be keyed by. In a coding project session those
// differ (tools write the project root; containers are per chat), and the pool
// inferring the wrong one is what made the stamp silently stop landing — every
// later sync then paid a whole-object read to re-prove "same version".
func TestWriteThroughStampsWithTheStoreScopeItWasGiven(t *testing.T) {
	ctx := context.Background()
	lp, primary, peer, projectSlot, _ := broadcastFixture(t)
	// The fixture's store object lives at (agt_b, proj_b, chat_a) — exactly the
	// scope we are about to state.
	sc := sandboxScope{agentID: "agt_b", projectID: "proj_b", sessionID: "chat_a"}

	if _, err := lp.WriteThrough(ctx, sc, StoreScope{ProjectID: "proj_b", SessionID: ""},
		"app/x.tsx", "/workspace/app/x.tsx", "new", ""); err != nil {
		t.Fatalf("write-through: %v", err)
	}
	stamped := func(ex *recordingExecutor) bool {
		for _, cmd := range ex.execs {
			if strings.Contains(cmd, "touch -d @") {
				return true
			}
		}
		return false
	}
	for name, ex := range map[string]*recordingExecutor{
		"primary": primary, "sibling chat": peer, "project slot": projectSlot,
	} {
		if !stamped(ex) {
			t.Fatalf("%s container was not stamped — the store scope the caller stated was ignored: %v", name, ex.execs)
		}
	}

	// A scope that does not name the object produces no stamp: the parameter is
	// load-bearing, which is the whole point of passing it instead of guessing.
	fresh := newRecordingExecutor()
	lp.inner.(*fakeInnerPool).byScope[poolKey("agt_b", "proj_b", "chat_a")] = fresh
	if _, err := lp.WriteThrough(ctx, sc, StoreScope{ProjectID: "proj_b", SessionID: "chat_a"},
		"app/x.tsx", "/workspace/app/x.tsx", "new", ""); err != nil {
		t.Fatalf("write-through: %v", err)
	}
	if stamped(fresh) {
		t.Fatalf("a stamp landed for a scope that has no such object: %v", fresh.execs)
	}
}

// A delete has to reach the peers too: a container that keeps its copy will
// write it straight back on its next sync — the resurrection loop, one container
// further out.
func TestRemoveLiveWorkspaceFileReachesEveryContainerOfTheProject(t *testing.T) {
	ctx := context.Background()
	lp, primary, peer, projectSlot, foreign := broadcastFixture(t)

	ex, err := lp.Get(ctx, "agt_b", "proj_b", "chat_a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	remover, ok := ex.(LiveWorkspaceFileRemover)
	if !ok {
		t.Fatalf("the lifecycle proxy %T does not expose the delete-side capability", ex)
	}
	if err := remover.RemoveLiveWorkspaceFile(ctx, "projects/proj_b/app/x.tsx"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for name, target := range map[string]*recordingExecutor{
		"primary": primary, "sibling chat": peer, "project slot": projectSlot,
	} {
		found := false
		for _, cmd := range target.execs {
			if strings.Contains(cmd, "rm -f --") && strings.Contains(cmd, "/workspace/app/x.tsx") {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s container was not asked to drop the file: %v", name, target.execs)
		}
	}
	for _, cmd := range foreign.execs {
		if strings.Contains(cmd, "rm -f") {
			t.Fatalf("another project's container was touched: %v", foreign.execs)
		}
	}
}
