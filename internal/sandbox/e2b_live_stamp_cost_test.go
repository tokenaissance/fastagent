package sandbox_test

// G22, measured on real infrastructure before it is fixed: how many whole-object
// READS does one sync pay for a file the tools wrote, in a project session?
//
// The stamp exists so the reconcile can settle "same version" with size+mtime
// and read nothing. It reads the store with the SANDBOX scope
// (`Stat(agent, projectID, sessionID, key)`), while a coding-root project
// session's tools write the PROJECT ROOT — so the Stat misses, no stamp lands,
// and every later sync falls back to `equalToStore`, which fetches the whole
// object body.
//
// This test counts those bodies. The control is a file hydrate delivered (it
// carries the store's mtime from the tar), which must cost zero reads.
//
//	FASTAGENT_E2B_LIVE=1 E2B_API_KEY=e2b_... \
//	  go test ./internal/sandbox/ -run TestE2BLiveSyncReadsNoBodiesForStampablePaths -v -count=1

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// countingStore counts the two store calls a sync makes: Stat (a HEAD) and Get
// (the whole body).
type countingStore struct {
	workspace.Store
	gets  atomic.Int64
	stats atomic.Int64
}

func (c *countingStore) Get(ctx context.Context, agentID, projectID, sessionID, path string) (io.ReadCloser, error) {
	c.gets.Add(1)
	return c.Store.Get(ctx, agentID, projectID, sessionID, path)
}

func (c *countingStore) Stat(ctx context.Context, agentID, projectID, sessionID, path string) (*workspace.ObjectInfo, error) {
	c.stats.Add(1)
	return c.Store.Stat(ctx, agentID, projectID, sessionID, path)
}

func TestE2BLiveSyncReadsNoBodiesForStampablePaths(t *testing.T) {
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

	agent := fmt.Sprintf("live_stamp_%d", time.Now().UnixNano())
	const (
		projectID = "proj-stamp"
		chatID    = "chat-stamp"
		hydrated  = "app/hydrated.md"
		edited    = "app/edited.md"
	)
	base := workspace.NewLocalFS(t.TempDir())
	// Both live at the PROJECT ROOT: that is where the tools write in a coding
	// session, and it is the key the stamp fails to find.
	for _, p := range []string{hydrated, edited} {
		if err := base.Put(ctx, agent, projectID, "", p, strings.NewReader("body\n"), -1, ""); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}
	store := &countingStore{Store: base}

	inner := sandbox.NewE2BExecutorPool(apiKey, template, t.TempDir(), 10*time.Minute)
	inner.SetWorkspace(store)
	lp := sandbox.NewLifecyclePool(inner, time.Hour, time.Hour)
	lp.SetWorkspace(store)
	defer lp.CloseAll()

	ex, err := lp.Get(ctx, agent, projectID, chatID)
	if err != nil {
		t.Fatalf("sandbox up: %v", err)
	}
	if out, err := ex.Exec(ctx, "cat /workspace/"+hydrated, 60*time.Second); err != nil || !strings.Contains(out, "body") {
		t.Fatalf("hydrate: %v (%s)", err, out)
	}

	// Mirror a tool write the way the file tools do. This is the call whose stamp
	// is supposed to make later syncs free.
	// (The tool writes the store FIRST, then mirrors — otherwise the two copies
	// disagree and the sync's verdict would be BLOCKED rather than "same".)
	if err := store.Put(ctx, agent, projectID, "", edited, strings.NewReader("edited body\n"), -1, ""); err != nil {
		t.Fatalf("tool store write: %v", err)
	}
	wt, ok := ex.(sandbox.WriteThroughExecutor)
	if !ok {
		t.Fatalf("executor %T lacks the write-through capability", ex)
	}
	// The scope the FILE TOOL passes in a coding session: scopeSessionID()
	// collapses to "", i.e. the project root — which is where this file lives.
	if _, err := wt.WriteThroughScope(sandbox.StoreScope{ProjectID: projectID, SessionID: ""}, edited, "/workspace/"+edited, "edited body\n", "body\n"); err != nil {
		t.Fatalf("write-through: %v", err)
	}

	// Measure one sync (any exec triggers it).
	store.gets.Store(0)
	store.stats.Store(0)
	if _, err := ex.Exec(ctx, "true", 60*time.Second); err != nil {
		t.Fatalf("exec: %v", err)
	}
	gets, stats := store.gets.Load(), store.stats.Load()
	t.Logf("one sync in a project session: %d whole-object reads, %d stats (2 paths in the sandbox)", gets, stats)

	// The claim G22 is about: a path the mirror wrote should cost ZERO body reads
	// on later syncs — that is precisely what the stamp buys. (The hydrated path
	// is the control: its mtime came from the store in the tar, so it was already
	// cheap before any fix.)
	if gets != 0 {
		t.Errorf("a sync in a project session read %d whole object(s) from the store; "+
			"the stamp is supposed to make that unnecessary (docs 10 §4 G22). "+
			"Measured: %d reads, %d stats.", gets, gets, stats)
	}
}
