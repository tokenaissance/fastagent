package sandbox_test

// H on real infrastructure: one project, TWO containers (two chats), and the
// rule that makes the preview work when the dev server is running in only one of
// them — a write reaches every live container of the project, and a delete does
// too.
//
// Docker gets this for free (all containers bind-mount one host directory). A
// backend without a mount has to do it explicitly, which is what this test
// proves: chat A's write shows up inside chat B's sandbox, and deleting it in A
// also removes B's copy — otherwise B's next sync would write the file back
// (docs 10 §4 G17, options G + H).
//
// Gated like the other live tests:
//
//	FASTAGENT_E2B_LIVE=1 E2B_API_KEY=e2b_... \
//	  go test ./internal/sandbox/ -run TestE2BLiveProjectWriteReachesSiblingContainer -v -count=1

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

func TestE2BLiveProjectWriteReachesSiblingContainer(t *testing.T) {
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
	defer cancel()

	agent := fmt.Sprintf("live_bcast_%d", time.Now().UnixNano())
	const (
		projectID = "proj-bcast"
		chatA     = "chat-a"
		chatB     = "chat-b"
		relPath   = "app/notes.md"
	)
	store := workspace.NewLocalFS(t.TempDir())
	inner := sandbox.NewE2BExecutorPool(apiKey, template, t.TempDir(), 10*time.Minute)
	inner.SetWorkspace(store)
	lp := sandbox.NewLifecyclePool(inner, time.Hour, time.Hour)
	lp.SetWorkspace(store)
	defer lp.CloseAll()

	// Two chats of the SAME project — the shape a preview has to survive.
	exA, err := lp.Get(ctx, agent, projectID, chatA)
	if err != nil {
		t.Fatalf("chat A sandbox: %v", err)
	}
	exB, err := lp.Get(ctx, agent, projectID, chatB)
	if err != nil {
		t.Fatalf("chat B sandbox: %v", err)
	}
	if out, err := exA.Exec(ctx, "echo A", 60*time.Second); err != nil || !strings.Contains(out, "A") {
		t.Fatalf("chat A warm-up: %v (%s)", err, out)
	}
	if out, err := exB.Exec(ctx, "echo B", 60*time.Second); err != nil || !strings.Contains(out, "B") {
		t.Fatalf("chat B warm-up: %v (%s)", err, out)
	}

	// Chat A's tool write, mirrored the way the file tools mirror it.
	if err := store.Put(ctx, agent, projectID, chatA, relPath, strings.NewReader("BROADCAST"), -1, ""); err != nil {
		t.Fatalf("store put: %v", err)
	}
	// Through the same capability the file tools use, so the scope this write
	// belongs to is the executor's own (chat A).
	wt, ok := exA.(sandbox.WriteThroughExecutor)
	if !ok {
		t.Fatalf("executor %T does not expose the write-through capability", exA)
	}
	outcome, err := wt.WriteThroughScope(sandbox.StoreScope{ProjectID: projectID, SessionID: chatA}, relPath, "/workspace/"+relPath, "BROADCAST", "")
	if err != nil {
		t.Fatalf("write-through: %v", err)
	}
	if outcome.BroadcastFailures != 0 {
		t.Fatalf("broadcast failures = %d; want 0", outcome.BroadcastFailures)
	}

	// THE CLAIM: the sibling container — where a dev server for this project may
	// well be running — sees the file too.
	if out, err := exB.Exec(ctx, "cat /workspace/"+relPath, 60*time.Second); err != nil || !strings.Contains(out, "BROADCAST") {
		t.Fatalf("the sibling container did not get the write: %v (%s)", err, out)
	}

	// And a delete reaches it as well: a copy left behind is a file that comes
	// back on that container's next sync.
	remover, ok := exA.(sandbox.LiveWorkspaceFileRemover)
	if !ok {
		t.Fatalf("executor %T does not expose the delete-side capability", exA)
	}
	storeRel := "projects/" + projectID + "/" + relPath
	if err := store.Delete(ctx, agent, "", "", storeRel); err != nil {
		t.Fatalf("library delete: %v", err)
	}
	if err := remover.RemoveLiveWorkspaceFile(ctx, storeRel); err != nil {
		t.Fatalf("sandbox removal: %v", err)
	}
	if out, err := exB.Exec(ctx, "test -e /workspace/"+relPath+" && echo yes || echo no", 60*time.Second); err != nil {
		t.Fatalf("probe: %v (%s)", err, out)
	} else if strings.Contains(out, "yes") {
		t.Fatalf("the sibling container still holds the deleted file: %q", out)
	}
	// A sync there must not resurrect it in the store either.
	if _, err := exB.Exec(ctx, "true", 60*time.Second); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if rc, err := store.Get(ctx, agent, "", "", storeRel); err == nil {
		rc.Close()
		t.Fatalf("the deleted file came back through the sibling container's sync")
	}
}
