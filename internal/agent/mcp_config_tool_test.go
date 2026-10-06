package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/fastclaw-ai/fastclaw/internal/agentconfig"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// TestMcpToolAddRemovePersistsE2E drives `mcp add` / `mcp remove` through
// the real sqlite store: add persists the server into the agent config
// blob and fires the reload notify; conflict/invalid inputs are refused;
// remove is the exact inverse and clears the row.
func TestMcpToolAddRemovePersistsE2E(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "agent-mcp.db")
	st, err := store.New(&store.StorageConfig{Type: "sqlite", DSN: dsn, AutoMigrate: true}, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	db, ok := st.(*store.DBStore)
	if !ok {
		t.Fatalf("store is %T, want *store.DBStore", st)
	}
	defer db.Close()

	now := time.Now().UTC()
	rc := config.ResolvedAgent{ID: "agent-1", UserID: "owner-1"}
	if err := db.SaveAgent(t.Context(), &store.AgentRecord{
		ID: rc.ID, UserID: rc.UserID, Name: "test",
		Config: map[string]interface{}{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	var notifyCalls int
	var notifiedUser, notifiedAgent string
	ag := &Agent{
		dataStore: db,
		mcpConfigNotify: func(userID, agentID string) {
			notifyCalls++
			notifiedUser, notifiedAgent = userID, agentID
		},
	}
	fn := mcpToolFnWithAgent(&oauth.Bootstrap{}, rc, "owner-1", ag)

	// add: static server
	out, err := callToolJSON(t, fn, map[string]any{
		"action": "add", "serverName": "plain", "url": "https://plain.example/mcp",
	})
	if err != nil {
		t.Fatalf("add static: %v", err)
	}
	if !strings.Contains(out, "registered") {
		t.Fatalf("add output = %q, want registered", out)
	}
	if notifyCalls != 1 || notifiedUser != "owner-1" || notifiedAgent != "agent-1" {
		t.Fatalf("notify = (%d,%q,%q), want (1,owner-1,agent-1)", notifyCalls, notifiedUser, notifiedAgent)
	}

	// add: OAuth server with scopes
	if _, err := callToolJSON(t, fn, map[string]any{
		"action": "add", "serverName": "quandora",
		"url":           "https://mcp.quandora.ai/quant",
		"oauthResource": "https://mcp.quandora.ai/quant",
		"scopes":        []string{"factor_mining:status"},
	}); err != nil {
		t.Fatalf("add oauth: %v", err)
	}

	servers, err := db.ListMCPServers(context.Background(), rc.ID)
	if err != nil {
		t.Fatalf("load servers: %v", err)
	}
	if got := servers["plain"].URL; got != "https://plain.example/mcp" {
		t.Fatalf("plain url = %q", got)
	}
	q := servers["quandora"]
	if q.OAuthResource != "https://mcp.quandora.ai/quant" || len(q.Scopes) != 1 || q.Scopes[0] != "factor_mining:status" {
		t.Fatalf("quandora stored = %+v", q)
	}

	// Refuse: duplicate, bad scheme, scopes without oauthResource.
	if _, err := callToolJSON(t, fn, map[string]any{
		"action": "add", "serverName": "plain", "url": "https://other.example/mcp",
	}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate add error = %v, want already exists", err)
	}
	if _, err := callToolJSON(t, fn, map[string]any{
		"action": "add", "serverName": "ftp", "url": "ftp://x/mcp",
	}); err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Fatalf("bad scheme error = %v, want http(s)", err)
	}
	if _, err := callToolJSON(t, fn, map[string]any{
		"action": "add", "serverName": "x", "url": "https://x/mcp",
		"scopes": []string{"a:b"},
	}); err == nil || !strings.Contains(err.Error(), "scopes require oauthResource") {
		t.Fatalf("scopes w/o oauth error = %v", err)
	}

	// remove is the inverse of add.
	if _, err := callToolJSON(t, fn, map[string]any{
		"action": "remove", "serverName": "quandora",
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	servers, err = db.ListMCPServers(context.Background(), rc.ID)
	if err != nil {
		t.Fatalf("reload after remove: %v", err)
	}
	if _, exists := servers["quandora"]; exists {
		t.Fatal("quandora should be removed")
	}
	if _, exists := servers["plain"]; !exists {
		t.Fatal("plain should still exist")
	}
	if notifyCalls != 3 {
		t.Fatalf("notify calls = %d, want 3 (2 add + 1 remove)", notifyCalls)
	}
	if _, err := callToolJSON(t, fn, map[string]any{
		"action": "remove", "serverName": "missing",
	}); err == nil || !strings.Contains(err.Error(), "not a configured") {
		t.Fatalf("remove missing error = %v", err)
	}

	// Removing the last server deletes the key entirely.
	if _, err := callToolJSON(t, fn, map[string]any{
		"action": "remove", "serverName": "plain",
	}); err != nil {
		t.Fatalf("remove last: %v", err)
	}
	servers, err = db.ListMCPServers(context.Background(), rc.ID)
	if err != nil {
		t.Fatalf("reload agent: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("agent_mcp_servers should be empty after removing the last server, got %+v", servers)
	}
}

func callToolJSON(t *testing.T, fn func(context.Context, json.RawMessage) (string, error), in map[string]any) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(in)
	return fn(context.Background(), raw)
}

// The incident (2026-10-05): `mcp add notion …` then `mcp login notion` in the
// SAME session answered `"notion" is not a configured OAuth MCP server`,
// because the OAuth actions read the snapshot the agent was built with. The
// agent now reads its configuration through the versioned cache
// (internal/agentconfig), so the write is visible to the next read in the same
// turn (docs/fastagent/design/15-agent-config-consistency.md).
//
// Falsification: leave rcProvider nil (the "without a provider" test below) and
// that refusal comes back — the assertion is the incident verbatim.
func TestMcpLoginSeesAServerAddedInTheSameSession(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "agent-mcp-same-session.db")
	st, err := store.New(&store.StorageConfig{Type: "sqlite", DSN: dsn, AutoMigrate: true}, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	db, ok := st.(*store.DBStore)
	if !ok {
		t.Fatalf("store is %T, want *store.DBStore", st)
	}
	defer db.Close()

	now := time.Now().UTC()
	if err := db.SaveAgent(t.Context(), &store.AgentRecord{
		ID: "agent-1", UserID: "owner-1", Name: "test",
		Config: map[string]interface{}{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// The build produced this session's snapshot with no MCP servers.
	rc := config.ResolvedAgent{ID: "agent-1", UserID: "owner-1"}
	// The read is the real use case over the real store, not a hand-rolled
	// closure: the cache, the counter, and the read-repair loop are the ones
	// production runs. The gateway supplies the same two things this test wires:
	// a Store adapter, and a notify that stamps the counter.
	cache, err := agentconfig.New(
		mcpIncidentConfigStore{db: db},
		agentconfig.NewMemoryMemo(0),
	)
	if err != nil {
		t.Fatalf("build the read cache: %v", err)
	}
	ag := &Agent{
		dataStore: db, agentID: "agent-1", ownerUserID: "owner-1",
		// The store write stamps itself (P0b), so the notify below only has to
		// invalidate caches. The gateway wires NotifyAgentReload here.
		mcpConfigNotify: func(string, string) {},
	}
	ag.rcProvider = cache.For
	fn := mcpToolFnWithAgent(testToolBootstrap(), rc, "owner-1", ag)
	t.Setenv("FASTAGENT_OAUTH_CALLBACK_BASE", "https://app.example.com/oauth/mcp")

	// The session read its configuration before the write, which is the shape
	// of the incident: the build filled the cache, then the write landed. Warm
	// the cache here for the same reason, so the assertion below can tell a
	// stale hit from a cold miss.
	if _, err := cache.For(context.Background(), agentconfig.Scope{UserID: "owner-1", AgentID: "agent-1"}); err != nil {
		t.Fatalf("warm read: %v", err)
	}

	if _, err := callToolJSON(t, fn, map[string]any{
		"action": "add", "serverName": "notion",
		"url":           "https://mcp.notion.example/mcp",
		"oauthResource": "https://mcp.notion.example/mcp",
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	out, err := callTool(t, fn, "login", "notion")
	if err != nil {
		t.Fatalf("login in the same session after add: %v", err)
	}
	if !strings.Contains(out, "https://as.example/oauth/authorize") {
		t.Fatalf("login did not return an authorization URL: %q", out)
	}
	stats := cache.Stats()
	if stats.Checks == 0 {
		t.Fatal("login never read the configuration through the cache; it used the stale snapshot")
	}
	if stats.StaleHits == 0 {
		t.Fatal("the cache never noticed the add; the write did not move the counter")
	}

	// And the visible half of the same read: `status` lists the server the
	// session just added, which is what the incident's user was told was
	// missing.
	status, err := callTool(t, fn, "status", "")
	if err != nil {
		t.Fatalf("status in the same session after add: %v", err)
	}
	if !strings.Contains(status, "notion") {
		t.Fatalf("status in the same session did not list the new server: %q", status)
	}
}

// mcpIncidentConfigStore is the agentconfig.Store adapter for the incident
// test: the counter is the real one, and Resolve answers from the same rows the
// tool writes. The production adapter does the full merge; the cache behaviour
// under test does not depend on that.
type mcpIncidentConfigStore struct{ db *store.DBStore }

func (s mcpIncidentConfigStore) CurrentVersion(ctx context.Context) (agentconfig.Version, error) {
	return s.db.CurrentConfigEpoch(ctx)
}

func (s mcpIncidentConfigStore) Resolve(ctx context.Context, scope agentconfig.Scope) (config.ResolvedAgent, error) {
	servers, err := s.db.ListMCPServers(ctx, scope.AgentID)
	if err != nil {
		return config.ResolvedAgent{}, err
	}
	return config.ResolvedAgent{ID: scope.AgentID, UserID: scope.UserID, MCPServers: servers}, nil
}

// The boundary the review asked to keep honest: with no provider wired there is
// no newer source, so the snapshot stands and the old refusal is correct for
// the CLI and for tests. This is the falsification of the test above.
func TestMcpLoginWithoutAProviderKeepsTheSnapshotBehavior(t *testing.T) {
	rc := config.ResolvedAgent{ID: "agent-1", UserID: "owner-1"}
	fn := mcpToolFnWithAgent(testToolBootstrap(), rc, "owner-1", &Agent{agentID: "agent-1", ownerUserID: "owner-1"})
	t.Setenv("FASTAGENT_OAUTH_CALLBACK_BASE", "https://app.example.com/oauth/mcp")

	if _, err := callTool(t, fn, "login", "notion"); err == nil || !strings.Contains(err.Error(), "not a configured OAuth MCP server") {
		t.Fatalf("login with no provider = %v; want the not-configured refusal", err)
	}
}

// A provider that fails must not fall back to the snapshot: the caller gets the
// error, never a value of unknown age (agentconfig I3).
func TestMcpLoginFailsWhenTheConfigReadFails(t *testing.T) {
	rc := config.ResolvedAgent{ID: "agent-1", UserID: "owner-1"}
	ag := &Agent{agentID: "agent-1", ownerUserID: "owner-1"}
	ag.rcProvider = func(context.Context, agentconfig.Scope) (config.ResolvedAgent, error) {
		return config.ResolvedAgent{}, errors.New("store is unavailable")
	}
	fn := mcpToolFnWithAgent(testToolBootstrap(), rc, "owner-1", ag)
	t.Setenv("FASTAGENT_OAUTH_CALLBACK_BASE", "https://app.example.com/oauth/mcp")

	_, err := callTool(t, fn, "login", "notion")
	if err == nil || !strings.Contains(err.Error(), "agent configuration is unavailable") {
		t.Fatalf("login with a failing provider = %v; want the availability error", err)
	}
}
