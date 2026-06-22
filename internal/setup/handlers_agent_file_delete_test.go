package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// setupFileDeleteTest is a convenience helper that creates a test Server with
// a seeded user, agent, and LocalFS workspace store.
func setupFileDeleteTest(t *testing.T) (*Server, context.Context, string, string) {
	t.Helper()
	s := setupTestServer(t)
	ctx := context.Background()

	uid := "user_delete_test"
	if err := s.dataStore.CreateUser(ctx, &store.UserRecord{
		ID:           uid,
		Username:     "deletetest",
		Email:        "delete@test.com",
		PasswordHash: "nope",
		Role:         users.RoleUser,
		Status:       "active",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	aid := "agt_delete_test"
	if err := s.dataStore.SaveAgent(ctx, &store.AgentRecord{
		ID:     aid,
		UserID: uid,
		Name:   "Delete Test Agent",
	}); err != nil {
		t.Fatalf("save agent: %v", err)
	}

	s.workspaceStore = workspace.NewLocalFS(t.TempDir())
	return s, ctx, uid, aid
}

// stampAuth sets a session-based auth identity on a request context so the
// handler can read it via auth.FromContext. When readOnly is true the
// identity gets a non-empty ActAsUserID, which triggers ReadOnly() = true.
func stampAuth(r *http.Request, userID string, readOnly bool) *http.Request {
	id := auth.Identity{
		UserID:     userID,
		Role:       users.RoleUser,
		AuthMethod: "session",
	}
	if readOnly {
		id.ActAsUserID = "admin_acting"
	}
	return r.WithContext(auth.WithIdentity(r.Context(), id))
}

// ---------------------------------------------------------------------------

func TestHandleAgentFileDelete_Success(t *testing.T) {
	s, ctx, uid, aid := setupFileDeleteTest(t)

	content := strings.NewReader("hello world")
	if err := s.workspaceStore.Put(ctx, aid, "", "", "test.txt", content, -1, ""); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.SetPathValue("id", aid)
	req.SetPathValue("path", "test.txt")
	req = stampAuth(req, uid, false)
	w := httptest.NewRecorder()

	s.handleAgentFileDelete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if body["ok"] != true {
		t.Errorf("body[ok] = %v, want true", body["ok"])
	}

	// File should be gone from disk
	if _, err := s.workspaceStore.Get(ctx, aid, "", "", "test.txt"); err == nil {
		t.Error("expected error for deleted file, got nil")
	}
}

func TestHandleAgentFileDelete_WithSessionID(t *testing.T) {
	s, ctx, uid, aid := setupFileDeleteTest(t)

	content := bytes.NewReader([]byte("session content"))
	if err := s.workspaceStore.Put(ctx, aid, "", "sess_abc", "notes.py", content, -1, ""); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/?sessionId=sess_abc", nil)
	req.SetPathValue("id", aid)
	req.SetPathValue("path", "notes.py")
	req = stampAuth(req, uid, false)
	w := httptest.NewRecorder()

	s.handleAgentFileDelete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	if _, err := s.workspaceStore.Get(ctx, aid, "", "sess_abc", "notes.py"); err == nil {
		t.Error("expected error for deleted session file, got nil")
	}
}

func TestHandleAgentFileDelete_NonExistent(t *testing.T) {
	s, _, uid, aid := setupFileDeleteTest(t)

	// LocalFS.Delete returns nil when the file doesn't exist on disk, so
	// deleting a never-seeded file should still return 200.
	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.SetPathValue("id", aid)
	req.SetPathValue("path", "never-existed.py")
	req = stampAuth(req, uid, false)
	w := httptest.NewRecorder()

	s.handleAgentFileDelete(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestHandleAgentFileDelete_EmptyPath(t *testing.T) {
	s, _, uid, aid := setupFileDeleteTest(t)

	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.SetPathValue("id", aid)
	req.SetPathValue("path", "")
	req = stampAuth(req, uid, false)
	w := httptest.NewRecorder()

	s.handleAgentFileDelete(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body = %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestHandleAgentFileDelete_NoWorkspaceStore(t *testing.T) {
	s := setupTestServer(t) // workspaceStore is nil
	ctx := context.Background()

	uid := "user_no_ws"
	if err := s.dataStore.CreateUser(ctx, &store.UserRecord{
		ID: uid, Username: "nows", Email: "nows@test.com",
		PasswordHash: "x", Role: users.RoleUser, Status: "active",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	aid := "agt_no_ws"
	if err := s.dataStore.SaveAgent(ctx, &store.AgentRecord{
		ID: aid, UserID: uid, Name: "No WS",
	}); err != nil {
		t.Fatalf("save agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.SetPathValue("id", aid)
	req.SetPathValue("path", "f.txt")
	req = stampAuth(req, uid, false)
	w := httptest.NewRecorder()

	s.handleAgentFileDelete(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d; body = %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
}

func TestHandleAgentFileDelete_NonExistentAgent(t *testing.T) {
	s, _, _, _ := setupFileDeleteTest(t)

	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.SetPathValue("id", "agt_does_not_exist")
	req.SetPathValue("path", "f.txt")
	req = stampAuth(req, "random_user", false)
	w := httptest.NewRecorder()

	s.handleAgentFileDelete(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d; body = %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestHandleAgentFileDelete_ReadOnlyIdentity(t *testing.T) {
	s, _, uid, aid := setupFileDeleteTest(t)

	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.SetPathValue("id", aid)
	req.SetPathValue("path", "f.txt")
	req = stampAuth(req, uid, true) // readOnly = true → ActAsUserID set
	w := httptest.NewRecorder()

	s.handleAgentFileDelete(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d; body = %s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

func TestHandleAgentFileDelete_NotOwner(t *testing.T) {
	s, ctx, _, aid := setupFileDeleteTest(t)

	otherUID := "other_user"
	if err := s.dataStore.CreateUser(ctx, &store.UserRecord{
		ID: otherUID, Username: "other", Email: "other@test.com",
		PasswordHash: "x", Role: users.RoleUser, Status: "active",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.SetPathValue("id", aid)
	req.SetPathValue("path", "f.txt")
	req = stampAuth(req, otherUID, false)
	w := httptest.NewRecorder()

	s.handleAgentFileDelete(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d; body = %s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

func TestHandleAgentFileDelete_SubdirPath(t *testing.T) {
	s, ctx, uid, aid := setupFileDeleteTest(t)

	content := strings.NewReader("nested content")
	if err := s.workspaceStore.Put(ctx, aid, "", "", "subdir/deep/file.go", content, -1, ""); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.SetPathValue("id", aid)
	req.SetPathValue("path", "subdir/deep/file.go")
	req = stampAuth(req, uid, false)
	w := httptest.NewRecorder()

	s.handleAgentFileDelete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	if _, err := s.workspaceStore.Get(ctx, aid, "", "", "subdir/deep/file.go"); err == nil {
		t.Error("expected error for deleted subdir file, got nil")
	}
}
