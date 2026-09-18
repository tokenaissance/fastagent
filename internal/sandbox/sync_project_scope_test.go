package sandbox

// A (docs 10 §4 G17, residual half): a sync's write-back belongs to the PROJECT
// ROOT, not to the chat that happens to own the container.
//
// The shared test fixture cannot see the difference — `fakeWorkspace` keys by
// `scopeForKey`, which collapses (project, session) to "p:<pid>" on purpose, so
// (pid, chat) and (pid, "") are the same bucket there. That is exactly the
// confusion A is about, so this test uses a real LocalFS store and asserts the
// KEYS the sync produced.

import (
	"context"
	"sort"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

func keysUnder(t *testing.T, store *workspace.LocalFS, agentID, projectID, sessionID string) []string {
	t.Helper()
	objs, err := store.List(context.Background(), agentID, projectID, sessionID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Path)
	}
	sort.Strings(out)
	return out
}

func TestSyncWritesBackToTheProjectRootNotTheChatSubdir(t *testing.T) {
	ctx := context.Background()
	store := workspace.NewLocalFS(t.TempDir())

	// A project chat's container: its /workspace IS the project tree (hydrate
	// lists the project with session=""), and the sandbox has produced one file
	// the store does not have yet.
	pool := newSnappingPool(map[string][]byte{"artifact.txt": []byte("from the sandbox")})
	lp := NewLifecyclePool(pool, 0, 0)
	lp.SetWorkspace(store)

	ex, err := pool.Get(ctx, "erin", "proj-1", "chat-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	lp.syncSnapshot(ctx, sandboxScope{agentID: "erin", projectID: "proj-1", sessionID: "chat-1"}, ex, "test")

	// The artefact belongs to the project root — the key the file tools read and
	// hydrate fills from.
	if got := keysUnder(t, store, "erin", "proj-1", ""); len(got) != 1 || got[0] != "artifact.txt" {
		t.Fatalf("project root = %v; want [artifact.txt]", got)
	}
	// …and NOT to the chat's own subdir, which is what duplicated every project
	// file once per chat.
	if got := keysUnder(t, store, "erin", "proj-1", "chat-1"); len(got) != 0 {
		t.Fatalf("chat subdir = %v; want nothing (the sync must not fork the tree per chat)", got)
	}

	// A loose chat is unchanged: no project to share, so its own scope holds the
	// artefact.
	loosePool := newSnappingPool(map[string][]byte{"loose.txt": []byte("x")})
	loose := NewLifecyclePool(loosePool, 0, 0)
	loose.SetWorkspace(store)
	exLoose, err := loosePool.Get(ctx, "erin", "", "chat-9")
	if err != nil {
		t.Fatalf("get loose: %v", err)
	}
	loose.syncSnapshot(ctx, sandboxScope{agentID: "erin", sessionID: "chat-9"}, exLoose, "test")
	if got := keysUnder(t, store, "erin", "", "chat-9"); len(got) != 1 || got[0] != "loose.txt" {
		t.Fatalf("loose chat = %v; want [loose.txt]", got)
	}
}

// The decision, stated as a property: for a project, the scope the sync writes
// to is the scope hydrate reads from. That equality is what keeps one file tree
// one tree.
func TestSyncScopeEqualsHydrateScopeForProjects(t *testing.T) {
	project := syncStoreScope(sandboxScope{agentID: "a", projectID: "p1", sessionID: "c1"})
	if project.projectID != "p1" || project.sessionID != "" {
		t.Fatalf("project sync scope = (%q,%q); want (p1,\"\")", project.projectID, project.sessionID)
	}
	loose := syncStoreScope(sandboxScope{agentID: "a", sessionID: "c1"})
	if loose.projectID != "" || loose.sessionID != "c1" {
		t.Fatalf("loose sync scope = (%q,%q); want (\"\",c1)", loose.projectID, loose.sessionID)
	}
}
