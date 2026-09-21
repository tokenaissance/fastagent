package setup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/domain"
	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/port"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// Deleting an identity must not leave its stored MCP credentials behind.
// Before this, both delete paths swept per-agent tables (agent_mcp_servers
// included) but not mcp_oauth_tokens — the table is owned by the OAuth
// side, the store does not know the key shape, and no foreign key connects
// them. The rows survived with no reachable reader: every call site
// resolves the agent row first (oauthAgentOwner), so the credential
// answered 403 forever while still holding a refresh token.
//
// These are delivery-point witnesses (the rule itself is pinned in
// internal/mcp/oauth/adapter/token_store_prefix_delete_test.go): they
// drive the real handlers over the real store and the real bootstrap, and
// they check both directions — the deleted identity's credentials go,
// and nobody else's do.
func saveTestAgent(t *testing.T, st store.Store, agentID, userID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.SaveAgent(context.Background(), &store.AgentRecord{
		ID: agentID, UserID: userID, Name: agentID, Config: map[string]any{},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save agent %s: %v", agentID, err)
	}
}

func createTestUser(t *testing.T, st store.Store, username, role string) string {
	t.Helper()
	accts, err := users.NewAccounts(st)
	if err != nil {
		t.Fatalf("accounts: %v", err)
	}
	acct, err := accts.Create(context.Background(), users.CreateInput{
		Username: username, Email: username + "@test.dev", Password: "pw", Role: role,
	})
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return acct.ID
}

func storeTestCredential(t *testing.T, srv *Server, userID, agentID, serverName string) string {
	t.Helper()
	key := domain.StoreKey(userID, agentID, serverName)
	if err := srv.mcpOAuth.Tokens.Save(context.Background(), key, &domain.OAuthTokens{
		AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("save credential %s: %v", key, err)
	}
	return key
}

func credentialState(t *testing.T, srv *Server, key string) string {
	t.Helper()
	if _, err := srv.mcpOAuth.Tokens.Load(context.Background(), key); errors.Is(err, port.ErrNotFound) {
		return "gone"
	} else if err != nil {
		t.Fatalf("load credential %s: %v", key, err)
	}
	return "present"
}

func TestDeleteAgentForgetsItsStoredMcpCredentials(t *testing.T) {
	srv, _ := newOAuthTestServer(t)
	ownerID := createTestUser(t, srv.dataStore, "forget-owner", users.RoleUser)
	otherUserID := createTestUser(t, srv.dataStore, "forget-bystander", users.RoleUser)

	const victim, bystander, otherUsersAgent = "agt-forget-victim", "agt-forget-bystander", "agt-forget-other-user"
	saveTestAgent(t, srv.dataStore, victim, ownerID)
	saveTestAgent(t, srv.dataStore, bystander, ownerID)
	saveTestAgent(t, srv.dataStore, otherUsersAgent, otherUserID)

	victimA := storeTestCredential(t, srv, ownerID, victim, "quandora")
	victimB := storeTestCredential(t, srv, ownerID, victim, "notion")
	bystanderKey := storeTestCredential(t, srv, ownerID, bystander, "quandora")
	otherUserKey := storeTestCredential(t, srv, otherUserID, otherUsersAgent, "quandora")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/agents/"+victim, nil).
		WithContext(withIdentity(ownerID, users.RoleUser))
	req.SetPathValue("id", victim) // the mux supplies this in production
	srv.handleDeleteAgent(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE agent = %d: %s", rec.Code, rec.Body.String())
	}

	for _, key := range []string{victimA, victimB} {
		if got := credentialState(t, srv, key); got != "gone" {
			t.Errorf("credential %s after deleting its agent = %s, want gone", key, got)
		}
	}
	for _, key := range []string{bystanderKey, otherUserKey} {
		if got := credentialState(t, srv, key); got != "present" {
			t.Errorf("credential %s was swept by an unrelated agent delete = %s, want present", key, got)
		}
	}
}

func TestDeleteUserForgetsItsAgentsStoredMcpCredentials(t *testing.T) {
	srv, _ := newOAuthTestServer(t)
	victimUserID := createTestUser(t, srv.dataStore, "forget-user", users.RoleUser)
	bystanderUserID := createTestUser(t, srv.dataStore, "forget-user-bystander", users.RoleUser)

	const firstAgent, secondAgent, bystanderAgent = "agt-user-first", "agt-user-second", "agt-user-bystander"
	saveTestAgent(t, srv.dataStore, firstAgent, victimUserID)
	saveTestAgent(t, srv.dataStore, secondAgent, victimUserID)
	saveTestAgent(t, srv.dataStore, bystanderAgent, bystanderUserID)

	victimKeys := []string{
		storeTestCredential(t, srv, victimUserID, firstAgent, "quandora"),
		storeTestCredential(t, srv, victimUserID, secondAgent, "notion"),
	}
	bystanderKey := storeTestCredential(t, srv, bystanderUserID, bystanderAgent, "quandora")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/users/"+victimUserID, nil).
		WithContext(withIdentity("admin-1", users.RoleSuperAdmin))
	req.SetPathValue("id", victimUserID)
	srv.handleDeleteUser(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE user = %d: %s", rec.Code, rec.Body.String())
	}
	// The cascade really ran (both agents are gone), so the credential
	// assertions below are about the credentials, not about a no-op delete.
	for _, agentID := range []string{firstAgent, secondAgent} {
		if rec, err := srv.dataStore.GetAgent(context.Background(), agentID); err == nil && rec != nil {
			t.Fatalf("agent %s survived the user delete", agentID)
		}
	}

	for _, key := range victimKeys {
		if got := credentialState(t, srv, key); got != "gone" {
			t.Errorf("credential %s after deleting its owner = %s, want gone", key, got)
		}
	}
	if got := credentialState(t, srv, bystanderKey); got != "present" {
		t.Errorf("credential %s was swept by an unrelated user delete = %s, want present", bystanderKey, got)
	}
}
