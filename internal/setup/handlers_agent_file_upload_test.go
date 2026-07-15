package setup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// setupFileUploadTest creates a minimal Server with dataStore + LocalFS
// workspace, a seeded user and agent, and returns them for use in upload tests.
func setupFileUploadTest(t *testing.T) (*Server, string /*uid*/, string /*aid*/) {
	t.Helper()
	s := setupTestServer(t)
	ctx := context.Background()

	uid := "user_upload_test"
	if err := s.dataStore.CreateUser(ctx, &store.UserRecord{
		ID: uid, Username: "uploadtest", Email: "upload@test.com",
		PasswordHash: "x", Role: users.RoleUser, Status: "active",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	aid := "agt_upload_test"
	if err := s.dataStore.SaveAgent(ctx, &store.AgentRecord{
		ID: aid, UserID: uid, Name: "Upload Test",
	}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	s.workspaceStore = workspace.NewLocalFS(t.TempDir())
	return s, uid, aid
}

// multipartRequest builds a multipart/form-data POST with one "file" field.
func multipartRequest(t *testing.T, url, filename, content string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.WriteString(fw, content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// stampUserIDInContext stamps config.UserID (used by workspaceSessionScope)
// onto the request context alongside the auth identity.
func stampUserIDInContext(r *http.Request, uid string) *http.Request {
	ctx := config.WithUserID(r.Context(), uid)
	return r.WithContext(ctx)
}

// stampAuthAndUserID combines stampAuth + stampUserIDInContext so both the
// auth middleware identity AND the config.UserID key are present. Most
// handlers need auth.FromContext (owner check) while workspaceSessionScope
// reads config.UserIDFromContext.
func stampAuthAndUserID(r *http.Request, uid string) *http.Request {
	r = stampAuth(r, uid, false)
	return stampUserIDInContext(r, uid)
}

// ---------------------------------------------------------------------------
// Test: session not yet in DB → file lands at sessions/<sessionKey>/
// ---------------------------------------------------------------------------

// TestFileUpload_NoSession_FallsBackToRawKey verifies the Bug 1 fix:
// when the client sends a sessionId that does not yet exist in the DB
// (no message has been sent yet), the upload handler should store the
// file at sessions/<sessionKey>/<filename> rather than the agent root.
func TestFileUpload_NoSession_FallsBackToRawKey(t *testing.T) {
	s, uid, aid := setupFileUploadTest(t)

	sessionKey := "abc-123-fresh"
	url := fmt.Sprintf("/?sessionId=%s", sessionKey)
	req := multipartRequest(t, url, "report.pdf", "pdf content")
	req.SetPathValue("id", aid)
	req = stampAuthAndUserID(req, uid)

	w := httptest.NewRecorder()
	s.handleAgentFileUpload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	// File must be at sessions/<sessionKey>/report.pdf — NOT at agent root.
	ctx := context.Background()
	if _, err := s.workspaceStore.Get(ctx, aid, "", sessionKey, "report.pdf"); err != nil {
		t.Errorf("file not found at sessions/%s/report.pdf: %v", sessionKey, err)
	}
	// Agent root must be empty for this filename.
	if _, err := s.workspaceStore.Get(ctx, aid, "", "", "report.pdf"); err == nil {
		t.Error("file unexpectedly found at agent root — scope fallback did not apply")
	}
}

// ---------------------------------------------------------------------------
// Test: session already in DB → same path as fallback
// ---------------------------------------------------------------------------

// TestFileUpload_ExistingSession_UsesResolvedChatID verifies that once a
// session row exists in the DB, the file still lands at
// sessions/<chatID>/ where chatID == sessionKey for web channel.
func TestFileUpload_ExistingSession_UsesResolvedChatID(t *testing.T) {
	s, uid, aid := setupFileUploadTest(t)
	ctx := context.Background()

	sessionKey := "abc-123-existing"
	// Seed the session row (simulates: user already sent a message).
	if err := s.dataStore.SaveSession(ctx, uid, aid, sessionKey, &store.SessionRecord{
		Channel:  "web",
		ChatID:   sessionKey,
		Messages: nil,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	url := fmt.Sprintf("/?sessionId=%s", sessionKey)
	req := multipartRequest(t, url, "notes.txt", "hello")
	req.SetPathValue("id", aid)
	req = stampAuthAndUserID(req, uid)

	w := httptest.NewRecorder()
	s.handleAgentFileUpload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	// Path should match the fallback path exactly — sessions/<sessionKey>/.
	if _, err := s.workspaceStore.Get(ctx, aid, "", sessionKey, "notes.txt"); err != nil {
		t.Errorf("file not found at sessions/%s/notes.txt: %v", sessionKey, err)
	}
}

// ---------------------------------------------------------------------------
// Test: no sessionId at all → file lands at agent root (unchanged behaviour)
// ---------------------------------------------------------------------------

func TestFileUpload_NoSessionKey_LandsAtAgentRoot(t *testing.T) {
	s, uid, aid := setupFileUploadTest(t)

	req := multipartRequest(t, "/", "data.csv", "a,b,c")
	req.SetPathValue("id", aid)
	req = stampAuthAndUserID(req, uid)

	w := httptest.NewRecorder()
	s.handleAgentFileUpload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	ctx := context.Background()
	if _, err := s.workspaceStore.Get(ctx, aid, "", "", "data.csv"); err != nil {
		t.Errorf("file not found at agent root: %v", err)
	}
}
