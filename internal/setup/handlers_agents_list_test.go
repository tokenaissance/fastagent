package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// listAgentsResponse is the shape the MCP egress reads: one agent with the fields the
// listing promises, and nothing fetched twice.
type listAgentsResponse struct {
	Agents []struct {
		ID          string          `json:"id"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Model       string          `json:"model"`
		CreatedAt   string          `json:"createdAt"`
		UpdatedAt   string          `json:"updatedAt"`
		UserID      string          `json:"userId"`
		Role        string          `json:"role"`
		IsPublic    bool            `json:"isPublic"`
		SkillCount  *int            `json:"skillCount"`
		CountError  string          `json:"skillCountError"`
		Owner       json.RawMessage `json:"owner"`
	} `json:"agents"`
}

func listAgents(t *testing.T, s *Server, ctx context.Context, resolver *auth.Resolver, userID string) listAgentsResponse {
	t.Helper()
	rr := httptest.NewRecorder()
	// Through the middleware, not the handler directly: the identity this listing
	// resolves the caller from is stamped by the resolver, and calling the handler
	// bare would test a request no real caller can make.
	handler := s.authMiddleware(s.handleListAgents)
	handler(rr, authTestRequest(t, ctx, resolver, http.MethodGet, "/api/agents", userID))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s); want 200", rr.Code, rr.Body.String())
	}
	var body listAgentsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	return body
}

// The fields the MCP listing shows are pinned here as they go out: the wire is where
// they are read, and a rule that lives only in the handler is a rule the egress can
// silently disagree with.
func TestListAgentsCarriesOwnerModelTimestampsAndSkillCount(t *testing.T) {
	ctx := context.Background()
	s, resolver, _, regularUser := newAuthTestServer(t, ctx)
	home := t.TempDir()
	t.Setenv("FASTAGENT_HOME", home)

	rec := &store.AgentRecord{
		ID:     "agt_listed",
		UserID: regularUser.ID,
		Name:   "sakurain",
		Config: map[string]interface{}{"description": "reads skills over MCP"},
	}
	if err := s.dataStore.SaveAgent(ctx, rec); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	if err := scope.SaveSettingByScope(ctx, s.dataStore, scope.Agent, rec.ID, "agents.defaults",
		map[string]interface{}{"model": "claude-sonnet-4-5"}); err != nil {
		t.Fatalf("SaveSettingByScope: %v", err)
	}
	writeAgentSkill(t, filepath.Join(home, "skills"), "platform-skill")
	writeAgentSkill(t, filepath.Join(home, "agents", rec.ID, "agent", "skills"), "refunds")

	body := listAgents(t, s, ctx, resolver, regularUser.ID)
	if len(body.Agents) != 1 {
		t.Fatalf("agents = %+v; want one", body.Agents)
	}
	got := body.Agents[0]

	if got.Model != "claude-sonnet-4-5" {
		t.Errorf("model = %q", got.Model)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Errorf("timestamps = %q / %q; both must travel", got.CreatedAt, got.UpdatedAt)
	}
	if got.SkillCount == nil || *got.SkillCount != 2 {
		t.Errorf("skillCount = %v; want 2", got.SkillCount)
	}
	if got.Description != "reads skills over MCP" {
		t.Errorf("description = %q", got.Description)
	}

	var owner struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	if err := json.Unmarshal(got.Owner, &owner); err != nil {
		t.Fatalf("owner is not an object: %v (%s)", err, got.Owner)
	}
	if owner.ID != regularUser.ID || owner.Username != regularUser.Username || owner.Email != regularUser.Email {
		t.Errorf("owner = %+v; want the account that owns the agent", owner)
	}
}

// An agent whose layers cannot be read is still listed — its row is real — but the
// answer says so instead of reporting zero skills, which a reader would take for a
// fact about the agent.
func TestListAgentsReportsAnUnreadableSkillLayerAsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable directory is still readable")
	}
	ctx := context.Background()
	s, resolver, _, regularUser := newAuthTestServer(t, ctx)
	home := t.TempDir()
	t.Setenv("FASTAGENT_HOME", home)

	rec := &store.AgentRecord{ID: "agt_degraded", UserID: regularUser.ID, Name: "half-readable"}
	if err := s.dataStore.SaveAgent(ctx, rec); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	agentLayer := filepath.Join(home, "agents", rec.ID, "agent", "skills")
	writeAgentSkill(t, agentLayer, "refunds")
	if err := os.Chmod(agentLayer, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(agentLayer, 0o755) })

	body := listAgents(t, s, ctx, resolver, regularUser.ID)
	if len(body.Agents) != 1 {
		t.Fatalf("agents = %+v; want one", body.Agents)
	}
	got := body.Agents[0]
	if got.SkillCount != nil {
		t.Fatalf("skillCount = %d; a count nobody could read must not be sent", *got.SkillCount)
	}
	if got.CountError == "" {
		t.Fatal("skillCountError is empty; the reader cannot tell why the count is missing")
	}
}

// writeAgentSkill drops one publishable skill into a layer directory.
func writeAgentSkill(t *testing.T, dir, name string) {
	t.Helper()
	base := filepath.Join(dir, name)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: " + name + "\ndescription: " + name + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(base, "SKILL.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}
