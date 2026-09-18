package sandbox_test

// The panel's delete, end to end on real E2B (docs 10 §4 G21 + G7b, decision d1).
//
// Two halves, and the first one is the reason the second one exists:
//
//	1. WITHOUT the sandbox half, deleting the store object is undone by the next
//	   sync: the sandbox still holds the file, the sync's walk sees a path the
//	   store lacks, treats it as sandbox-born and writes it straight back. The
//	   user deletes a file, runs one command, and the file is back. This half
//	   asserts that loop exists — if it ever stops reproducing, the sync changed
//	   and this test should be rewritten, not deleted.
//	2. WITH the sandbox half (LiveWorkspaceFileRemover, reached through the same
//	   lazy proxy the tools use), the sandbox copy is gone too, so the sync has
//	   nothing to resurrect: the delete sticks.
//
// Gated like the other live tests:
//
//	FASTAGENT_E2B_LIVE=1 E2B_API_KEY=e2b_... \
//	  go test ./internal/sandbox/ -run TestE2BLivePanelDeleteSticks -v -count=1

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// Local to this file: the sandbox package's own live helpers live in an internal
// test file, and this one has to sit in sandbox_test to import the store.
const panelScopeSession = "panel-del"

func panelAgentName() string { return fmt.Sprintf("live_panel_%d", time.Now().UnixNano()) }

func livePanelDeleteFixture(t *testing.T, agent string) (*workspace.LocalFS, *sandbox.LifecyclePool, sandbox.Executor, context.Context) {
	t.Helper()
	if os.Getenv("FASTAGENT_E2B_LIVE") != "1" {
		t.Skip("live E2B: set FASTAGENT_E2B_LIVE=1 with E2B_API_KEY to run")
	}
	apiKey := os.Getenv("E2B_API_KEY")
	if apiKey == "" {
		t.Fatal("E2B_API_KEY is required")
	}
	template := os.Getenv("FASTAGENT_E2B_TEMPLATE")
	if template == "" {
		template = "base"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	store := workspace.NewLocalFS(t.TempDir())
	// Seeded the way the panel's upload does it: a BARE name inside the chat's
	// scope. The file LIST then returns the agent-relative, scope-prefixed path
	// ("sessions/<sid>/notes.md") — and that string is what the panel sends back
	// on delete (the shape G21 was about).
	if err := store.Put(ctx, agent, "", panelScopeSession, "notes.md",
		strings.NewReader("PLACEHOLDER"), -1, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	inner := sandbox.NewE2BExecutorPool(apiKey, template, t.TempDir(), 10*time.Minute)
	inner.SetWorkspace(store)
	lp := sandbox.NewLifecyclePool(inner, time.Hour, time.Hour)
	lp.SetWorkspace(store)
	t.Cleanup(lp.CloseAll)

	ex, err := lp.Get(ctx, agent, "", panelScopeSession)
	if err != nil {
		t.Fatalf("sandbox up: %v", err)
	}
	return store, lp, ex, ctx
}

func liveStoreHas(t *testing.T, store workspace.Store, agent, path string) bool {
	t.Helper()
	// Agent-relative, empty scope: the same resolution the download endpoint uses.
	rc, err := store.Get(context.Background(), agent, "", "", path)
	if err != nil {
		return false
	}
	defer rc.Close()
	_, _ = io.ReadAll(rc)
	return true
}

// NOTE: the fixture's seed path is what the FILE LIST returns for a loose chat
// (agent-relative, scope-prefixed), which is exactly the string the panel sends
// back on delete — the shape G21 was about.
func TestE2BLivePanelDeleteSticks(t *testing.T) {
	agent := panelAgentName()

	t.Run("without the sandbox half the delete is undone by the next sync", func(t *testing.T) {
		store, _, ex, ctx := livePanelDeleteFixture(t, agent+"_loop")
		const key = "sessions/" + panelScopeSession + "/notes.md"

		if out, err := ex.Exec(ctx, "cat /workspace/notes.md", 60*time.Second); err != nil || !strings.Contains(out, "PLACEHOLDER") {
			t.Fatalf("hydrate did not deliver the file: %v (%s)", err, out)
		}
		// The panel's library delete.
		if err := store.Delete(ctx, agent+"_loop", "", "", key); err != nil {
			t.Fatalf("library delete: %v", err)
		}
		// One command later, the sync runs.
		if _, err := ex.Exec(ctx, "true", 60*time.Second); err != nil {
			t.Fatalf("exec: %v", err)
		}
		if !liveStoreHas(t, store, agent+"_loop", key) {
			t.Fatalf("the loop did NOT reproduce: the store stayed deleted. If the sync " +
				"stopped rewriting sandbox-born paths, revisit docs 10 §4 G7b/d1 — the " +
				"decision rests on this loop being real")
		}
	})

	t.Run("with the sandbox half the delete sticks", func(t *testing.T) {
		store, _, ex, ctx := livePanelDeleteFixture(t, agent+"_fix")
		const key = "sessions/" + panelScopeSession + "/notes.md"

		if out, err := ex.Exec(ctx, "cat /workspace/notes.md", 60*time.Second); err != nil || !strings.Contains(out, "PLACEHOLDER") {
			t.Fatalf("hydrate did not deliver the file: %v (%s)", err, out)
		}
		// What the handler does now: library delete, then the live-sandbox removal
		// through the same proxy the file tools are handed.
		if err := store.Delete(ctx, agent+"_fix", "", "", key); err != nil {
			t.Fatalf("library delete: %v", err)
		}
		remover, ok := ex.(sandbox.LiveWorkspaceFileRemover)
		if !ok {
			t.Fatalf("the lifecycle proxy %T does not expose the delete-side capability", ex)
		}
		if err := remover.RemoveLiveWorkspaceFile(ctx, key); err != nil {
			t.Fatalf("sandbox removal: %v", err)
		}
		if out, err := ex.Exec(ctx, "test -e /workspace/notes.md && echo yes || echo no", 60*time.Second); err != nil {
			t.Fatalf("probe: %v (%s)", err, out)
		} else if strings.Contains(out, "yes") {
			t.Fatalf("the sandbox still holds the file: %q", out)
		}
		// And the sync no longer has anything to resurrect.
		if _, err := ex.Exec(ctx, "true", 60*time.Second); err != nil {
			t.Fatalf("exec: %v", err)
		}
		if liveStoreHas(t, store, agent+"_fix", key) {
			t.Fatal("the deleted file came back after a sync — the sandbox half is not doing its job")
		}
	})

}
