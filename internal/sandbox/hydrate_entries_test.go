package sandbox

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// unreadableFile fails Get for one path, the way a store does when a single
// object is unreadable while the rest of the listing is fine. That is the shape
// the completeness check exists for: the listing succeeds, one file cannot be
// read, and the archive would otherwise go out looking complete.
type unreadableFile struct {
	*fakeWorkspace
	path string
}

func (u *unreadableFile) Get(ctx context.Context, agentID, projectID, sessionID, p string) (io.ReadCloser, error) {
	if p == u.path {
		return nil, errors.New("store read failed")
	}
	return u.fakeWorkspace.Get(ctx, agentID, projectID, sessionID, p)
}

func TestHydrateEntriesBundlesEverythingReadable(t *testing.T) {
	ws := newFakeWorkspace()
	ws.put("agent-entries", "notes.md", []byte("one"))
	ws.put("agent-entries", "report.pdf", []byte("two"))
	ex := &E2BExecutor{workspace: ws, agentID: "agent-entries"}
	objs, err := ex.listWorkspaceWithRetry(context.Background(), "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	bundled := ex.hydrateWorkspaceEntries(context.Background(), objs, newTarBundle(), "", "")

	if bundled != len(objs) || bundled != 2 {
		t.Fatalf("bundled = %d, want %d (everything listed)", bundled, len(objs))
	}
	if ex.WorkspaceUnhydrated() {
		t.Fatal("a complete archive marked the executor unhydrated")
	}
}

func TestHydrateEntriesMarksTheScopeUnhydratedOnAShortfall(t *testing.T) {
	ws := newFakeWorkspace()
	ws.put("agent-short", "notes.md", []byte("one"))
	ws.put("agent-short", "locked.bin", []byte("two"))
	store := &unreadableFile{fakeWorkspace: ws, path: "locked.bin"}
	ex := &E2BExecutor{workspace: store, agentID: "agent-short"}
	objs, err := ex.listWorkspaceWithRetry(context.Background(), "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	bundled := ex.hydrateWorkspaceEntries(context.Background(), objs, newTarBundle(), "", "")

	// Best-effort is preserved: the readable file still goes out.
	if bundled != len(objs)-1 {
		t.Fatalf("bundled = %d, want %d (the readable one)", bundled, len(objs)-1)
	}
	// …but the shortfall is not silent: "listed 2, bundled 1" is exactly the
	// state that must not look like a complete workspace.
	if !ex.WorkspaceUnhydrated() {
		t.Fatal("a shortfall left the executor looking hydrated")
	}
}

var _ workspace.Store = (*unreadableFile)(nil)
