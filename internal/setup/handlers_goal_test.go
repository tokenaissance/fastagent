package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// These tests guard the REST /goal surface — handlers_goal.go was
// previously untested end-to-end, relying on hand-eyeballing that
// it mirrors slash_goal.go behavior. Now every public verb's happy
// path and the high-traffic failure modes (auth, validation, state
// conflict) are pinned. The fixture is intentionally heavy on
// behavior assertions (DB state after each call) and light on
// response-body shape — shape changes are cheap to fix; behavior
// drift would silently break the dashboard.

// goalTestFixture wires the bare minimum a Server needs to serve
// the /goal handlers: a fresh in-memory DBStore, a seeded agent
// row that the configured owner can claim, and the Server itself.
// No userResolver — that means triggerGoalRuntime is a quiet no-op,
// which is fine for these tests; the lifecycle hook is exercised
// separately in wire_goals_test.go.
type goalTestFixture struct {
	t       *testing.T
	srv     *Server
	db      *store.DBStore
	agentID string
	ownerID string
}

func newGoalTestFixture(t *testing.T) *goalTestFixture {
	t.Helper()
	// Per-test unique DSN keeps the shared-cache in-memory DB from
	// leaking rows between tests in the same package run.
	dsn := "file:goaltest-" + t.Name() + "?mode=memory&cache=shared"
	dsn = strings.ReplaceAll(dsn, "/", "_") // t.Name() may contain "/"
	db, err := store.NewDBStore("sqlite", dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ownerID := "user-1"
	agentID := "agent-A"
	if err := db.SaveAgent(context.Background(), &store.AgentRecord{
		ID:        agentID,
		UserID:    ownerID,
		Name:      "Test Agent",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	srv := NewServer(0)
	srv.SetStore(db)
	return &goalTestFixture{t: t, srv: srv, db: db, agentID: agentID, ownerID: ownerID}
}

// req builds an authenticated *http.Request for the agent owner.
// Use reqAs to override the caller (for forbidden-access tests).
func (f *goalTestFixture) req(method, path string, body any) *http.Request {
	return f.reqAs(f.ownerID, method, path, body)
}

func (f *goalTestFixture) reqAs(uid, method, path string, body any) *http.Request {
	f.t.Helper()
	var bodyReader *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewBuffer(b)
	}
	var r *http.Request
	if bodyReader != nil {
		r = httptest.NewRequest(method, path, bodyReader)
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.SetPathValue("id", f.agentID)
	r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{UserID: uid}))
	return r
}

// seedGoal inserts a goal directly via the store adapter, bypassing
// the REST surface so a "GET shows existing goal" test isn't
// hostage to a prior POST passing.
func (f *goalTestFixture) seedGoal(sessionKey, objective string, status goal.Status, budget *int64) *goal.Goal {
	f.t.Helper()
	st := goal.NewStoreAdapter(f.db)
	g := &goal.Goal{
		ID:          "g-seeded-" + sessionKey,
		AgentID:     f.agentID,
		SessionKey:  sessionKey,
		OwnerUserID: f.ownerID,
		Channel:     "web",
		ChatID:      "chat-1",
		Objective:   objective,
		Status:      status,
		TokenBudget: budget,
	}
	if err := st.CreateGoal(context.Background(), g); err != nil {
		f.t.Fatalf("seed goal: %v", err)
	}
	return g
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, w.Body.String())
	}
	return out
}

// --- GET ---

func TestHandleGetAgentGoalMissingSessionKey(t *testing.T) {
	f := newGoalTestFixture(t)
	w := httptest.NewRecorder()
	f.srv.handleGetAgentGoal(w, f.req(http.MethodGet, "/api/agents/agent-A/goal", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestHandleGetAgentGoalNotFound(t *testing.T) {
	f := newGoalTestFixture(t)
	w := httptest.NewRecorder()
	f.srv.handleGetAgentGoal(w, f.req(http.MethodGet, "/api/agents/agent-A/goal?sessionKey=s-1", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestHandleGetAgentGoalReturnsSeededGoal(t *testing.T) {
	f := newGoalTestFixture(t)
	f.seedGoal("s-1", "translate README", goal.StatusActive, nil)
	w := httptest.NewRecorder()
	f.srv.handleGetAgentGoal(w, f.req(http.MethodGet, "/api/agents/agent-A/goal?sessionKey=s-1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	g, ok := body["goal"].(map[string]any)
	if !ok {
		t.Fatalf("response missing goal: %v", body)
	}
	if g["Objective"] != "translate README" {
		t.Errorf("objective = %v, want %q", g["Objective"], "translate README")
	}
	if g["Status"] != string(goal.StatusActive) {
		t.Errorf("status = %v, want %q", g["Status"], goal.StatusActive)
	}
}

func TestHandleGetAgentGoalWrongUserForbidden(t *testing.T) {
	f := newGoalTestFixture(t)
	f.seedGoal("s-1", "x", goal.StatusActive, nil)
	w := httptest.NewRecorder()
	f.srv.handleGetAgentGoal(w, f.reqAs("other-user", http.MethodGet, "/api/agents/agent-A/goal?sessionKey=s-1", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

func TestHandleGetAgentGoalUnknownAgent(t *testing.T) {
	f := newGoalTestFixture(t)
	r := httptest.NewRequest(http.MethodGet, "/api/agents/nonexistent/goal?sessionKey=s-1", nil)
	r.SetPathValue("id", "nonexistent")
	r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{UserID: f.ownerID}))
	w := httptest.NewRecorder()
	f.srv.handleGetAgentGoal(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// --- POST ---

func TestHandlePostInvalidJSON(t *testing.T) {
	f := newGoalTestFixture(t)
	r := httptest.NewRequest(http.MethodPost, "/api/agents/agent-A/goal", strings.NewReader("not json"))
	r.SetPathValue("id", f.agentID)
	r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{UserID: f.ownerID}))
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestHandlePostMissingSessionKey(t *testing.T) {
	f := newGoalTestFixture(t)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.req(http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:    "create",
		Objective: "x",
	}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestHandlePostUnknownAction(t *testing.T) {
	f := newGoalTestFixture(t)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.req(http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:     "delete",
		SessionKey: "s-1",
	}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestHandlePostCreateEmptyObjective(t *testing.T) {
	f := newGoalTestFixture(t)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.req(http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:     "create",
		SessionKey: "s-1",
	}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestHandlePostCreateNegativeBudget(t *testing.T) {
	f := newGoalTestFixture(t)
	neg := int64(-1)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.req(http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:      "create",
		SessionKey:  "s-1",
		Objective:   "x",
		TokenBudget: &neg,
	}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestHandlePostCreateHappyPath(t *testing.T) {
	f := newGoalTestFixture(t)
	budget := int64(200_000)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.req(http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:      "create",
		SessionKey:  "s-1",
		Objective:   "translate README to English",
		TokenBudget: &budget,
		Channel:     "web",
		ChatID:      "chat-1",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	// Behavior assertion (the load-bearing one): the row landed in DB
	// with the right shape, not just that the response said 201.
	g, err := goal.NewStoreAdapter(f.db).GetGoalBySession(context.Background(), f.agentID, "s-1")
	if err != nil {
		t.Fatalf("post-create read failed: %v", err)
	}
	if g.Status != goal.StatusActive {
		t.Errorf("created goal status = %q, want active", g.Status)
	}
	if g.Objective != "translate README to English" {
		t.Errorf("objective = %q", g.Objective)
	}
	if g.TokenBudget == nil || *g.TokenBudget != 200_000 {
		t.Errorf("budget round-trip failed: %v", g.TokenBudget)
	}
	if g.Channel != "web" || g.ChatID != "chat-1" {
		t.Errorf("routing tuple round-trip failed: channel=%q chat=%q", g.Channel, g.ChatID)
	}
}

func TestHandlePostCreateDuplicateConflict(t *testing.T) {
	f := newGoalTestFixture(t)
	f.seedGoal("s-1", "first", goal.StatusActive, nil)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.req(http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:     "create",
		SessionKey: "s-1",
		Objective:  "second",
	}))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
}

func TestHandlePostPauseHappyPath(t *testing.T) {
	f := newGoalTestFixture(t)
	f.seedGoal("s-1", "x", goal.StatusActive, nil)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.req(http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:     "pause",
		SessionKey: "s-1",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	g, _ := goal.NewStoreAdapter(f.db).GetGoalBySession(context.Background(), f.agentID, "s-1")
	if g.Status != goal.StatusPaused {
		t.Errorf("post-pause status = %q, want paused", g.Status)
	}
}

func TestHandlePostPauseRejectsWrongState(t *testing.T) {
	f := newGoalTestFixture(t)
	f.seedGoal("s-1", "x", goal.StatusPaused, nil)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.req(http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:     "pause",
		SessionKey: "s-1",
	}))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
}

func TestHandlePostResumeHappyPath(t *testing.T) {
	f := newGoalTestFixture(t)
	f.seedGoal("s-1", "x", goal.StatusPaused, nil)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.req(http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:     "resume",
		SessionKey: "s-1",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	g, _ := goal.NewStoreAdapter(f.db).GetGoalBySession(context.Background(), f.agentID, "s-1")
	if g.Status != goal.StatusActive {
		t.Errorf("post-resume status = %q, want active", g.Status)
	}
}

func TestHandlePostResumeRejectsWrongState(t *testing.T) {
	f := newGoalTestFixture(t)
	f.seedGoal("s-1", "x", goal.StatusActive, nil)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.req(http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:     "resume",
		SessionKey: "s-1",
	}))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
}

func TestHandlePostTransitionMissingGoal(t *testing.T) {
	f := newGoalTestFixture(t)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.req(http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:     "pause",
		SessionKey: "s-1",
	}))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestHandlePostWrongUserForbidden(t *testing.T) {
	f := newGoalTestFixture(t)
	w := httptest.NewRecorder()
	f.srv.handlePostAgentGoal(w, f.reqAs("other-user", http.MethodPost, "/api/agents/agent-A/goal", goalActionBody{
		Action:     "create",
		SessionKey: "s-1",
		Objective:  "x",
	}))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

// --- DELETE ---

func TestHandleDeleteMissingSessionKey(t *testing.T) {
	f := newGoalTestFixture(t)
	w := httptest.NewRecorder()
	f.srv.handleDeleteAgentGoal(w, f.req(http.MethodDelete, "/api/agents/agent-A/goal", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestHandleDeleteIdempotentOnMissingGoal(t *testing.T) {
	f := newGoalTestFixture(t)
	w := httptest.NewRecorder()
	f.srv.handleDeleteAgentGoal(w, f.req(http.MethodDelete, "/api/agents/agent-A/goal?sessionKey=s-1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (idempotent); body=%s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	if body["deleted"] != false {
		t.Errorf("deleted = %v, want false", body["deleted"])
	}
}

func TestHandleDeleteRemovesGoal(t *testing.T) {
	f := newGoalTestFixture(t)
	f.seedGoal("s-1", "x", goal.StatusActive, nil)
	w := httptest.NewRecorder()
	f.srv.handleDeleteAgentGoal(w, f.req(http.MethodDelete, "/api/agents/agent-A/goal?sessionKey=s-1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	if body["deleted"] != true {
		t.Errorf("deleted = %v, want true", body["deleted"])
	}
	// Behavior assertion: row really gone, not just "200 said so".
	g, err := goal.NewStoreAdapter(f.db).GetGoalBySession(context.Background(), f.agentID, "s-1")
	if g != nil || err == nil {
		t.Errorf("goal still in DB after delete: g=%v err=%v", g, err)
	}
}

// --- GET /goals (list) ---

func TestHandleListEmptyList(t *testing.T) {
	f := newGoalTestFixture(t)
	w := httptest.NewRecorder()
	f.srv.handleListAgentGoals(w, f.req(http.MethodGet, "/api/agents/agent-A/goals", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	goals, _ := body["goals"].([]any)
	if len(goals) != 0 {
		t.Errorf("expected empty list, got %d goals", len(goals))
	}
}

func TestHandleListReturnsAgentGoals(t *testing.T) {
	f := newGoalTestFixture(t)
	f.seedGoal("s-1", "first", goal.StatusActive, nil)
	f.seedGoal("s-2", "second", goal.StatusPaused, nil)
	w := httptest.NewRecorder()
	f.srv.handleListAgentGoals(w, f.req(http.MethodGet, "/api/agents/agent-A/goals", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	goals, _ := body["goals"].([]any)
	if len(goals) != 2 {
		t.Errorf("expected 2 goals on this agent, got %d (body=%s)", len(goals), w.Body.String())
	}
}

func TestHandleListWrongUserForbidden(t *testing.T) {
	f := newGoalTestFixture(t)
	w := httptest.NewRecorder()
	f.srv.handleListAgentGoals(w, f.reqAs("other-user", http.MethodGet, "/api/agents/agent-A/goals", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}
