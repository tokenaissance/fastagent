package setup

// e2e for the session→agent deep-link fast path. Cloud deep links
// (/app/<sid>) carry no agent id, so the cloud proxy walks the paged
// GET /chats list to find which agent owns a session. That walk builds a
// preview per session (one ListSessionMessages each) and costs ~8 s on a
// cold account. GET /api/chat/sessions/locate?sessionId=<key> answers
// the same question with one indexed row and no preview.
//
// Pinned here:
//   - store.LookupSessionLocation: nil agentIDs = admin (any agent),
//     scoped = only the listed agents, empty = nothing, unknown key =
//     not found, and the returned user/agent/project fields.
//   - handleLocateSession scope: a session identity sees every agent it
//     owns, an agent-key identity sees only its ACL, and an
//     out-of-scope or unknown key returns 404 {"found": false}.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

type locateResponse struct {
	Found     bool   `json:"found"`
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	ProjectID string `json:"projectId"`
}

func seedLocateSession(t *testing.T, s *Server, owner, agent, key, project string) {
	t.Helper()
	ctx := store.WithChatterUserID(context.Background(), owner)
	rec := &store.SessionRecord{Channel: "web", ChatID: key, ProjectID: project}
	if err := s.dataStore.SaveSession(ctx, owner, agent, key, rec); err != nil {
		t.Fatalf("seed session %s: %v", key, err)
	}
}

// ---------------------------------------------------------------------------
// Store layer — LookupSessionLocation
// ---------------------------------------------------------------------------

func TestLookupSessionLocation_StoreLayer(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()
	for _, id := range []string{"agent_loc_a", "agent_loc_b"} {
		if err := s.dataStore.SaveAgent(ctx, &store.AgentRecord{ID: id, UserID: "owner_loc", Name: id}); err != nil {
			t.Fatalf("seed agent %s: %v", id, err)
		}
	}
	seedLocateSession(t, s, "owner_loc", "agent_loc_a", "loc_a1", "proj_x")
	seedLocateSession(t, s, "owner_loc", "agent_loc_b", "loc_b1", "")

	// nil agentIDs = admin: any agent's session resolves.
	loc, found, err := s.dataStore.LookupSessionLocation(ctx, nil, "loc_a1")
	if err != nil {
		t.Fatalf("LookupSessionLocation(nil): %v", err)
	}
	if !found || loc.UserID != "owner_loc" || loc.AgentID != "agent_loc_a" || loc.ProjectID != "proj_x" {
		t.Errorf("nil lookup = (%+v, %v), want owner_loc/agent_loc_a/proj_x", loc, found)
	}

	// Scoped to agent B: agent A's session is out of scope.
	if _, found, err := s.dataStore.LookupSessionLocation(ctx, []string{"agent_loc_b"}, "loc_a1"); err != nil || found {
		t.Errorf("scoped miss = (found=%v, err=%v), want (false, nil)", found, err)
	}
	// Scoped to agent A: its own session resolves.
	if loc, found, err := s.dataStore.LookupSessionLocation(ctx, []string{"agent_loc_a"}, "loc_a1"); err != nil || !found || loc.AgentID != "agent_loc_a" {
		t.Errorf("scoped hit = (%+v, %v, %v), want agent_loc_a", loc, found, err)
	}

	// Empty (non-nil) scope sees nothing.
	if _, found, err := s.dataStore.LookupSessionLocation(ctx, []string{}, "loc_a1"); err != nil || found {
		t.Errorf("empty scope = (found=%v, err=%v), want (false, nil)", found, err)
	}
	// Unknown key is "not found", not an error.
	if _, found, err := s.dataStore.LookupSessionLocation(ctx, nil, "loc_missing"); err != nil || found {
		t.Errorf("missing key = (found=%v, err=%v), want (false, nil)", found, err)
	}
	// A session with no project reports an empty projectId.
	if loc, found, _ := s.dataStore.LookupSessionLocation(ctx, nil, "loc_b1"); !found || loc.ProjectID != "" {
		t.Errorf("no-project lookup = (%+v, %v), want projectId=\"\"", loc, found)
	}
}

// ---------------------------------------------------------------------------
// Handler — handleLocateSession
// ---------------------------------------------------------------------------

func decodeLocate(t *testing.T, body []byte) locateResponse {
	t.Helper()
	var resp locateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode locate response: %v\nbody: %s", err, body)
	}
	return resp
}

func TestHandleLocateSession_OwnerScope(t *testing.T) {
	s, accts := chatsOwnerE2EServer(t)
	owner := createChatsUser(t, accts, "loc_owner", "loc_ext", "Loc Owner")
	other := createChatsUser(t, accts, "loc_other", "loc_other_ext", "Other")
	ctx := context.Background()
	if err := s.dataStore.SaveAgent(ctx, &store.AgentRecord{ID: "agent_loc_e2e", UserID: owner.ID, Name: "Loc Agent"}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	seedLocateSession(t, s, owner.ID, "agent_loc_e2e", "sess_loc", "proj_e2e")

	locate := func(userID, sessionID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/locate?sessionId="+sessionID, nil)
		req = stampAuth(req, userID, false)
		w := httptest.NewRecorder()
		s.handleLocateSession(w, req)
		return w
	}

	// The owner resolves their own session, with agent name and project.
	w := locate(owner.ID, "sess_loc")
	if w.Code != http.StatusOK {
		t.Fatalf("owner locate status = %d; body = %s", w.Code, w.Body.String())
	}
	resp := decodeLocate(t, w.Body.Bytes())
	if !resp.Found || resp.AgentID != "agent_loc_e2e" || resp.AgentName != "Loc Agent" || resp.ProjectID != "proj_e2e" || resp.SessionID != "sess_loc" {
		t.Errorf("owner locate = %+v, want agent_loc_e2e/Loc Agent/proj_e2e", resp)
	}

	// A different user does not see the owner's session.
	w = locate(other.ID, "sess_loc")
	if w.Code != http.StatusNotFound {
		t.Errorf("stranger locate status = %d, want 404; body = %s", w.Code, w.Body.String())
	} else if resp := decodeLocate(t, w.Body.Bytes()); resp.Found {
		t.Errorf("stranger locate found = true, want false")
	}

	// A missing sessionId is a bad request, not a silent miss.
	req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/locate", nil)
	req = stampAuth(req, owner.ID, false)
	w = httptest.NewRecorder()
	s.handleLocateSession(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing sessionId status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

// agentKey builds an apikey identity scoped to an explicit agent ACL.
func agentKey(r *http.Request, agents ...string) *http.Request {
	id := auth.Identity{
		UserID:       "apikey_owner",
		AuthMethod:   "apikey",
		APIKeyType:   users.APIKeyTypeAgent,
		APIKeyAgents: agents,
	}
	return r.WithContext(auth.WithIdentity(r.Context(), id))
}

func TestHandleLocateSession_AgentKeyScope(t *testing.T) {
	s, accts := chatsOwnerE2EServer(t)
	owner := createChatsUser(t, accts, "loc_acl", "loc_acl_ext", "ACL Owner")
	ctx := context.Background()
	for _, id := range []string{"agent_loc_in", "agent_loc_out"} {
		if err := s.dataStore.SaveAgent(ctx, &store.AgentRecord{ID: id, UserID: owner.ID, Name: id}); err != nil {
			t.Fatalf("seed agent %s: %v", id, err)
		}
	}
	seedLocateSession(t, s, owner.ID, "agent_loc_in", "sess_in", "")
	seedLocateSession(t, s, owner.ID, "agent_loc_out", "sess_out", "")

	locate := func(agents []string, sessionID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/chat/sessions/locate?sessionId="+sessionID, nil)
		req = agentKey(req, agents...)
		w := httptest.NewRecorder()
		s.handleLocateSession(w, req)
		return w
	}

	// The key holds agent_loc_in: its session resolves, the other does not.
	if w := locate([]string{"agent_loc_in"}, "sess_in"); w.Code != http.StatusOK {
		t.Errorf("in-ACL status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if w := locate([]string{"agent_loc_in"}, "sess_out"); w.Code != http.StatusNotFound {
		t.Errorf("out-of-ACL status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}
