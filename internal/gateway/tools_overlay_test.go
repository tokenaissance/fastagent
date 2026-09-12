package gateway

// e2e for the EnsureAgent tool-chain gap.
//
// UserSpace.EnsureAgent is the lazy foreign-attach path (channel binding,
// public link, apikey-shared chatter, super_admin browse). It wired sandbox,
// runtime and hook plugins onto the agent it built, but never called
// registerAgentToolChains — the call loadUserSpace makes for every eagerly
// loaded agent. Provider-backed categories are *only* registered from a
// chain (web_search / image_gen / tts have no builtin registration), so a
// lazily attached agent answered "Unknown tool: web_search" for an agent the
// owner's own web chat could search with. Owner-scope tools.* rows were not
// overlaid either, so a chatter with no tool config of their own could not
// inherit the owner's backend.

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

func openToolsTestDB(t *testing.T) *store.DBStore {
	t.Helper()
	db, err := store.NewDBStore("sqlite", "file:tools_overlay?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// seedToolChainOwner creates an owner + agent pair and the dev-shaped
// system-scope tool config (searxng provider + web_search category chain).
func seedToolChainOwner(t *testing.T, db *store.DBStore) (ownerID, agentID string) {
	t.Helper()
	ctx := context.Background()
	owner := &store.UserRecord{
		ID: "u_tool_owner", Username: "owner", Email: "owner@example.com",
		PasswordHash: "x", Role: users.RoleUser, Status: users.StatusActive,
		AgentQuota: -1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.CreateUser(ctx, owner); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.SaveAgent(ctx, &store.AgentRecord{ID: "agt_tool", UserID: owner.ID, Name: "tooler"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	if err := scope.SaveSetting(ctx, db, "", "", "tools.categories", map[string]interface{}{
		"web_search": map[string]interface{}{"primary": "searxng/default"},
	}); err != nil {
		t.Fatalf("save system tools.categories: %v", err)
	}
	if err := scope.SaveSetting(ctx, db, "", "", "tools.providers", map[string]interface{}{
		"searxng": map[string]interface{}{"endpoint": "https://searxng.example"},
	}); err != nil {
		t.Fatalf("save system tools.providers: %v", err)
	}
	return owner.ID, "agt_tool"
}

// TestEnsureAgentRegistersToolChains is the regression: a chatter who does
// not own the agent must still get the configured web_search chain.
func TestEnsureAgentRegistersToolChains(t *testing.T) {
	t.Setenv("FASTAGENT_HOME", t.TempDir())
	db := openToolsTestDB(t)
	ctx := context.Background()
	ownerID, agentID := seedToolChainOwner(t, db)

	// The chatter's own snapshot: system + their own (empty) user scope.
	cfg, err := assembleConfig(ctx, db, "u_chatter", "")
	if err != nil {
		t.Fatalf("assembleConfig: %v", err)
	}
	if _, ok := cfg.Tools["web_search"]; !ok {
		t.Fatalf("precondition: chatter's merged view lost web_search: %v", cfg.Tools)
	}

	sp := &UserSpace{UserID: "u_chatter", Config: cfg}
	mgr, err := agent.NewManager(nil, nil, bus.New(), agent.WithUserID("u_chatter"))
	if err != nil {
		t.Fatalf("agent.NewManager: %v", err)
	}
	sp.Agents = mgr
	if err := sp.EnsureAgent(ctx, db, bus.New(), nil, agentID); err != nil {
		t.Fatalf("EnsureAgent: %v", err)
	}
	ag := mgr.AgentByID(agentID)
	if ag == nil {
		t.Fatalf("agent %q not attached", agentID)
	}
	names := make([]string, 0, 16)
	for _, ti := range ag.RegisteredTools() {
		names = append(names, ti.Name)
	}
	if !slices.Contains(names, "web_search") {
		t.Fatalf("lazily attached agent registered %v, want web_search (owner %s)", names, ownerID)
	}
	// Negative control: a category nobody configured stays unregistered, so
	// the assertion above is really about the chain, not a builtin.
	if slices.Contains(names, "tts") {
		t.Fatalf("tts registered without a configured chain: %v", names)
	}
}

// TestToolConfigForAgentOwnerOverlay pins the overlay semantics: the owner's
// user-scope tools.* rows win over the viewer's, the owner's full merged view
// is never spliced in, and the caller's snapshot is not mutated.
func TestToolConfigForAgentOwnerOverlay(t *testing.T) {
	db := openToolsTestDB(t)
	ctx := context.Background()
	ownerID, _ := seedToolChainOwner(t, db)

	// Owner-scope overrides on top of the system rows seeded above.
	if err := scope.SaveSetting(ctx, db, ownerID, "", "tools.categories", map[string]interface{}{
		"web_search": map[string]interface{}{"primary": "exa/exa"},
	}); err != nil {
		t.Fatalf("save owner tools.categories: %v", err)
	}
	if err := scope.SaveSetting(ctx, db, ownerID, "", "tools.providers", map[string]interface{}{
		"exa": map[string]interface{}{"apiKey": "exa-owner-key"},
	}); err != nil {
		t.Fatalf("save owner tools.providers: %v", err)
	}

	base := &config.Config{
		Tools:         map[string]config.ToolCategoryCfg{"web_search": {Primary: "searxng/default"}},
		ToolProviders: map[string]config.ToolProviderCfg{"searxng": {Endpoint: "https://searxng.example"}},
	}

	// No overlay asked for → same pointer, no store traffic.
	if got := toolConfigForAgent(ctx, db, base, ownerID, false); got != base {
		t.Fatalf("overlay disabled should return the caller's config unchanged")
	}

	got := toolConfigForAgent(ctx, db, base, ownerID, true)
	if got == base {
		t.Fatalf("overlay enabled should return a copy")
	}
	if got.Tools["web_search"].Primary != "exa/exa" {
		t.Fatalf("owner category did not win: %+v", got.Tools)
	}
	if got.ToolProviders["exa"].APIKey != "exa-owner-key" {
		t.Fatalf("owner provider missing: %+v", got.ToolProviders)
	}
	// System rows still visible, and the caller's snapshot untouched.
	if got.ToolProviders["searxng"].Endpoint != "https://searxng.example" {
		t.Fatalf("system provider lost: %+v", got.ToolProviders)
	}
	if base.Tools["web_search"].Primary != "searxng/default" {
		t.Fatalf("base config was mutated: %+v", base.Tools)
	}
	if _, ok := base.ToolProviders["exa"]; ok {
		t.Fatalf("base config gained the owner's provider: %+v", base.ToolProviders)
	}
}
