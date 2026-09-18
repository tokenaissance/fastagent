package setup

// The file panel's delete: the path it sends is AGENT-relative and already
// carries its scope prefix, and the fix has to keep that path whole — applying
// the scope query params on top of it deleted nothing at all, silently
// (docs 10 §4 G21). The sandbox half (decision d1) is pinned here too: the
// gateway must be asked to drop the live sandbox's copy, with the scope the
// sandbox is actually keyed by.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/api"
)

// sandboxRemoval records what the gateway would have been asked to remove.
type sandboxRemoval struct {
	agentID   string
	projectID string
	sessionID string
	storePath string
	calls     int
}

// panelResolver is a UserResolver that also carries the sandbox-removal hook the
// delete handler looks for — the same optional-interface shape the real gateway
// uses.
type panelResolver struct {
	sandboxRemoval
}

func (r *panelResolver) UserSpaceFor(string) (*api.UserSpaceView, error) { return nil, nil }
func (r *panelResolver) LocalAgentManager() *agent.Manager               { return nil }
func (r *panelResolver) IsCloudMode() bool                               { return true }

func (r *panelResolver) RemoveWorkspaceFile(_ context.Context, agentID, projectID, sessionID, storePath string) error {
	r.calls++
	r.agentID, r.projectID, r.sessionID, r.storePath = agentID, projectID, sessionID, storePath
	return nil
}

func deletePanelFile(t *testing.T, s *Server, uid, aid, path, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/"+query, nil)
	req.SetPathValue("id", aid)
	req.SetPathValue("path", path)
	req = stampAuth(req, uid, false)
	w := httptest.NewRecorder()
	s.handleAgentFileDelete(w, req)
	return w
}

// The regression this fix exists for: the panel sends what the list returned.
// Before the fix the handler added the scope a second time, addressed a key that
// cannot exist, and reported success while the file stayed put.
func TestHandleAgentFileDelete_UsesThePathThePanelClicked(t *testing.T) {
	s, ctx, uid, aid := setupFileDeleteTest(t)

	// What the list returns for a loose chat's file: agent-relative, prefixed.
	if err := s.workspaceStore.Put(ctx, aid, "", "sess_x", "notes.py",
		strings.NewReader("hello"), -1, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := deletePanelFile(t, s, uid, aid, "sessions/sess_x/notes.py", "?sessionId=sess_x")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	if _, err := s.workspaceStore.Get(ctx, aid, "", "sess_x", "notes.py"); err == nil {
		t.Fatal("the panel's delete left the file in the store — the path was scoped twice (G21)")
	}
}

// A project-root file: the store key carries the project, while the sandbox that
// holds it is the chat's (that is the instance the agent's turns use), so the
// chat has to come from the request the panel made.
func TestHandleAgentFileDelete_ProjectPathTargetsTheChatSandbox(t *testing.T) {
	s, ctx, uid, aid := setupFileDeleteTest(t)
	removal := &panelResolver{}
	s.userResolver = removal

	if err := s.workspaceStore.Put(ctx, aid, "proj_1", "", "notes.md",
		strings.NewReader("root file"), -1, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := deletePanelFile(t, s, uid, aid, "projects/proj_1/notes.md", "?sessionId=chat_9")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	if _, err := s.workspaceStore.Get(ctx, aid, "proj_1", "", "notes.md"); err == nil {
		t.Fatal("the project-root file survived a panel delete")
	}
	if removal.calls != 1 {
		t.Fatalf("gateway asked %d times to drop the sandbox copy; want 1", removal.calls)
	}
	if removal.agentID != aid || removal.projectID != "proj_1" || removal.sessionID != "chat_9" {
		t.Fatalf("sandbox scope = (%s,%s,%s); want (%s,proj_1,chat_9) — the chat comes from the request",
			removal.agentID, removal.projectID, removal.sessionID, aid)
	}
	if removal.storePath != "projects/proj_1/notes.md" {
		t.Fatalf("sandbox removal got %q; want the agent-relative path unchanged", removal.storePath)
	}
}

// The older, scope-relative shape (a bare name plus the query params) must keep
// working: the prefix wins when it is there, the query params when it is not.
func TestHandleAgentFileDelete_ScopeRelativePathStillWorks(t *testing.T) {
	s, ctx, uid, aid := setupFileDeleteTest(t)
	removal := &panelResolver{}
	s.userResolver = removal

	if err := s.workspaceStore.Put(ctx, aid, "", "sess_y", "plain.txt",
		strings.NewReader("x"), -1, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := deletePanelFile(t, s, uid, aid, "plain.txt", "?sessionId=sess_y")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	if _, err := s.workspaceStore.Get(ctx, aid, "", "sess_y", "plain.txt"); err == nil {
		t.Fatal("a scope-relative delete stopped working")
	}
	if removal.sessionID != "sess_y" || removal.projectID != "" {
		t.Fatalf("sandbox scope = (%q,%q); want ('',sess_y)", removal.projectID, removal.sessionID)
	}
}

// A failed sandbox removal is reported, not swallowed: the library delete stands,
// but the caller (and the logs) learn that a running sandbox still holds a copy
// which can come back on its next sync.
func TestHandleAgentFileDelete_ReportsAFailedSandboxRemoval(t *testing.T) {
	s, ctx, uid, aid := setupFileDeleteTest(t)
	s.userResolver = &failingRemover{panelResolver: &panelResolver{}}

	if err := s.workspaceStore.Put(ctx, aid, "", "sess_z", "keep.txt",
		strings.NewReader("x"), -1, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := deletePanelFile(t, s, uid, aid, "sessions/sess_z/keep.txt", "?sessionId=sess_z")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["sandboxRemoved"] != false {
		t.Fatalf("body = %v; want sandboxRemoved=false", body)
	}
	if _, err := s.workspaceStore.Get(ctx, aid, "", "sess_z", "keep.txt"); err == nil {
		t.Fatal("the store delete should stand even when the sandbox removal fails")
	}
}

// failingRemover implements the delete-side hook and always errors.
type failingRemover struct{ *panelResolver }

func (f failingRemover) RemoveWorkspaceFile(context.Context, string, string, string, string) error {
	return errors.New("sandbox removal failed (injected)")
}
