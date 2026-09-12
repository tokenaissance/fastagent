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
	if got := toolConfigForAgent(ctx, db, base, "u_chatter", "agt_tool", ownerID, false); got != base {
		t.Fatalf("overlay disabled should return the caller's config unchanged")
	}

	got := toolConfigForAgent(ctx, db, base, "u_chatter", "agt_tool", ownerID, true)
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

// TestToolConfigForAgentLayerPrecedence pins the layer order the runtime now
// uses for tools.*, matching scope.Setting (outer → inner, inner wins):
//
//	base (system + caller user)  <  owner user  <  agent  <  (caller, agent)
//
// The agent layer is the one that was missing: `fastagent tools category set
// --agent X` wrote a row the gateway never read, so the CLI said "Set" and the
// agent still reported no web_search. The (caller,agent) layer is the
// per-chatter override, and rows authored by *other* users must stay invisible
// (BatchGetConfigsByAgentIDs matches every user_id).
func TestToolConfigForAgentLayerPrecedence(t *testing.T) {
	db := openToolsTestDB(t)
	ctx := context.Background()
	ownerID, agentID := seedToolChainOwner(t, db)

	base := &config.Config{
		Tools:         map[string]config.ToolCategoryCfg{"web_search": {Primary: "searxng/default"}},
		ToolProviders: map[string]config.ToolProviderCfg{"searxng": {Endpoint: "https://searxng.example"}},
	}
	primary := func(cfg *config.Config) string { return cfg.Tools["web_search"].Primary }
	save := func(uid, aid, p string) {
		t.Helper()
		if err := scope.SaveSetting(ctx, db, uid, aid, "tools.categories", map[string]interface{}{
			"web_search": map[string]interface{}{"primary": p},
		}); err != nil {
			t.Fatalf("save tools.categories(%q,%q): %v", uid, aid, err)
		}
	}
	overlay := func(o bool) *config.Config {
		return toolConfigForAgent(ctx, db, base, "u_chatter", agentID, ownerID, o)
	}

	// Nothing stored at any inner layer: the caller's snapshot is reused, so
	// the common case costs no allocation and no copy.
	if got := overlay(true); got != base {
		t.Fatalf("no inner rows should return the caller's config unchanged")
	}

	// Owner user scope (foreign viewer, shareModelConfig on).
	save(ownerID, "", "exa/exa")
	if got := primary(overlay(true)); got != "exa/exa" {
		t.Fatalf("owner user scope should win over base, got %q", got)
	}
	if got := primary(overlay(false)); got != "searxng/default" {
		t.Fatalf("owner rows must not apply when the owner overlay is off, got %q", got)
	}

	// Agent scope — the regression this test exists for.
	save("", agentID, "brave/default")
	if got := primary(overlay(true)); got != "brave/default" {
		t.Fatalf("agent scope should win over owner user scope, got %q", got)
	}
	if got := primary(overlay(false)); got != "brave/default" {
		t.Fatalf("agent scope must apply even without the owner overlay, got %q", got)
	}

	// Another user's per-agent override stays invisible.
	save("u_other", agentID, "nope/nope")
	if got := primary(overlay(true)); got != "brave/default" {
		t.Fatalf("another user's per-agent row leaked in: %q", got)
	}

	// The caller's own per-(caller,agent) row is the innermost scope.
	save("u_chatter", agentID, "tavily/default")
	if got := primary(overlay(true)); got != "tavily/default" {
		t.Fatalf("caller+agent row should win over agent scope, got %q", got)
	}

	// Copy-on-write: the UserSpace snapshot is shared, never mutated.
	if primary(base) != "searxng/default" {
		t.Fatalf("base config was mutated: %+v", base.Tools)
	}
}

// TestEnsureAgentHonorsAgentScopeTools is the end-to-end regression: with NO
// system tool config at all, an agent whose *own* agent-scope row declares the
// web_search chain must still get the tool when lazily attached.
func TestEnsureAgentHonorsAgentScopeTools(t *testing.T) {
	t.Setenv("FASTAGENT_HOME", t.TempDir())
	db := openToolsTestDB(t)
	ctx := context.Background()
	owner := &store.UserRecord{
		ID: "u_agent_tools_owner", Username: "owner", Email: "owner2@example.com",
		PasswordHash: "x", Role: users.RoleUser, Status: users.StatusActive,
		AgentQuota: -1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.CreateUser(ctx, owner); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.SaveAgent(ctx, &store.AgentRecord{ID: "agt_agent_tools", UserID: owner.ID, Name: "agenttools"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	// Only the agent scope carries a chain — nothing at system/user scope.
	if err := scope.SaveSetting(ctx, db, "", "agt_agent_tools", "tools.categories", map[string]interface{}{
		"web_search": map[string]interface{}{"primary": "searxng/default"},
	}); err != nil {
		t.Fatalf("save agent tools.categories: %v", err)
	}
	if err := scope.SaveSetting(ctx, db, "", "agt_agent_tools", "tools.providers", map[string]interface{}{
		"searxng": map[string]interface{}{"endpoint": "https://searxng.example"},
	}); err != nil {
		t.Fatalf("save agent tools.providers: %v", err)
	}

	cfg, err := assembleConfig(ctx, db, "u_chatter", "")
	if err != nil {
		t.Fatalf("assembleConfig: %v", err)
	}
	if _, ok := cfg.Tools["web_search"]; ok {
		t.Fatalf("precondition: system/user layers must not carry web_search: %v", cfg.Tools)
	}

	sp := &UserSpace{UserID: "u_chatter", Config: cfg}
	mgr, err := agent.NewManager(nil, nil, bus.New(), agent.WithUserID("u_chatter"))
	if err != nil {
		t.Fatalf("agent.NewManager: %v", err)
	}
	sp.Agents = mgr
	if err := sp.EnsureAgent(ctx, db, bus.New(), nil, "agt_agent_tools"); err != nil {
		t.Fatalf("EnsureAgent: %v", err)
	}
	ag := mgr.AgentByID("agt_agent_tools")
	if ag == nil {
		t.Fatalf("agent not attached")
	}
	names := make([]string, 0, 16)
	for _, ti := range ag.RegisteredTools() {
		names = append(names, ti.Name)
	}
	if !slices.Contains(names, "web_search") {
		t.Fatalf("agent-scope chain was ignored; registered %v", names)
	}
}

// TestToolOverlaysForAgentsBatch pins the batched loader loadUserSpace uses:
// it must agree with the single-agent loader, keep the (caller,agent) layer
// innermost, and drop rows authored by other users.
func TestToolOverlaysForAgentsBatch(t *testing.T) {
	db := openToolsTestDB(t)
	ctx := context.Background()
	_, agentID := seedToolChainOwner(t, db)
	if err := db.SaveAgent(ctx, &store.AgentRecord{ID: "agt_plain", UserID: "u_tool_owner", Name: "plain"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	save := func(uid, aid, p string) {
		t.Helper()
		if err := scope.SaveSetting(ctx, db, uid, aid, "tools.categories", map[string]interface{}{
			"web_search": map[string]interface{}{"primary": p},
		}); err != nil {
			t.Fatalf("save tools.categories(%q,%q): %v", uid, aid, err)
		}
	}
	save("", agentID, "brave/default")        // agent layer
	save("u_chatter", agentID, "tavily/self") // caller's per-agent layer wins
	save("u_other", agentID, "nope/nope")     // must be invisible
	save("", "agt_plain", "duckduckgo/plain") // second agent, agent layer only

	batch := toolOverlaysForAgents(ctx, db, "u_chatter", []string{agentID, "agt_plain"})
	if got := batch[agentID].categories["web_search"].Primary; got != "tavily/self" {
		t.Fatalf("batched overlay for %s = %q, want the caller's per-agent row", agentID, got)
	}
	if got := batch["agt_plain"].categories["web_search"].Primary; got != "duckduckgo/plain" {
		t.Fatalf("batched overlay for agt_plain = %q", got)
	}
	// Agree with the single-agent loader (the two must not drift).
	single := applyToolOverlay(&config.Config{}, loadToolScopeRows(ctx, db, "", agentID), loadToolScopeRows(ctx, db, "u_chatter", agentID))
	if got := single.Tools["web_search"].Primary; got != "tavily/self" {
		t.Fatalf("single-agent loader disagrees with the batch: %q", got)
	}
}

// reversedRowsStore hands BatchGetConfigsByAgentIDs rows back in reverse of
// the backing store's order. sqlite happens to return the agent-scope row
// (user_id="") first — index order, not a guarantee — so a test that only
// inserts rows in a certain order can pass against a loader that merges in
// query order. Reversing makes the outer layer arrive last and forces the
// bug out.
type reversedRowsStore struct {
	store.Store
}

func (s reversedRowsStore) BatchGetConfigsByAgentIDs(ctx context.Context, kind, name string, agentIDs []string) ([]store.ConfigRecord, error) {
	rows, err := s.Store.BatchGetConfigsByAgentIDs(ctx, kind, name, agentIDs)
	if err != nil {
		return nil, err
	}
	slices.Reverse(rows)
	return rows, nil
}

// TestToolOverlaysForAgentsLayerOrderIsDeterministic is the regression for a
// precedence bug the batch loader had: it merged rows in whatever order
// BatchGetConfigsByAgentIDs returned them, and that query has no ORDER BY
// (so on Postgres the order is storage-dependent). Whichever layer landed
// last won, letting the OUTER agent-scope row beat the caller's inner row —
// the opposite of toolConfigForAgent, which walks agent scope then the caller
// layer. The caller row must win no matter how the query ordered the rows.
func TestToolOverlaysForAgentsLayerOrderIsDeterministic(t *testing.T) {
	db := openToolsTestDB(t)
	ctx := context.Background()
	_, agentID := seedToolChainOwner(t, db)
	save := func(uid, aid, p string) {
		t.Helper()
		if err := scope.SaveSetting(ctx, db, uid, aid, "tools.categories", map[string]interface{}{
			"web_search": map[string]interface{}{"primary": p},
		}); err != nil {
			t.Fatalf("save tools.categories(%q,%q): %v", uid, aid, err)
		}
	}
	save("", agentID, "brave/default")        // outer layer
	save("u_chatter", agentID, "tavily/self") // inner layer — must win

	// Reversed order: the agent-scope row is now the last one merged.
	rows := reversedRowsStore{Store: db}
	batch := toolOverlaysForAgents(ctx, rows, "u_chatter", []string{agentID})
	if got := batch[agentID].categories["web_search"].Primary; got != "tavily/self" {
		t.Fatalf("batched overlay = %q, want the caller's per-agent row to win regardless of query row order", got)
	}
	single := applyToolOverlay(&config.Config{}, loadToolScopeRows(ctx, db, "", agentID), loadToolScopeRows(ctx, db, "u_chatter", agentID))
	if got := single.Tools["web_search"].Primary; got != "tavily/self" {
		t.Fatalf("single-agent loader disagrees: %q", got)
	}
}
