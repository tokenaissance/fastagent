package setup

import (
	"context"
	"net/http"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// setupTestServer creates a minimal Server with a SQLite in-memory store
// for testing handler methods that only need dataStore.
func setupTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{dataStore: db}
}

// dummyRequest creates a minimal *http.Request with a background context.
func dummyRequest() *http.Request {
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/", nil)
	return req
}

// --- Tests for agentScopeModel ---

func TestAgentScopeModel_Found(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	// Seed: agent has a model override
	rec := &store.ConfigRecord{
		ID:      "cfg_model_1",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_test",
		Name:    "agents.defaults",
		Enabled: true,
		Data:    map[string]interface{}{"model": "openrouter/deepseek/deepseek-r1"},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got := s.agentScopeModel(dummyRequest(), "agt_test")
	if got != "openrouter/deepseek/deepseek-r1" {
		t.Errorf("agentScopeModel = %q, want %q", got, "openrouter/deepseek/deepseek-r1")
	}
}

func TestAgentScopeModel_NotFound(t *testing.T) {
	s := setupTestServer(t)

	got := s.agentScopeModel(dummyRequest(), "agt_nonexistent")
	if got != "" {
		t.Errorf("agentScopeModel = %q, want empty", got)
	}
}

func TestAgentScopeModel_NoModelField(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	// Seed: agents.defaults exists but has no "model" key
	rec := &store.ConfigRecord{
		ID:      "cfg_nomodel",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_nomodel",
		Name:    "agents.defaults",
		Enabled: true,
		Data:    map[string]interface{}{"promptMode": "structured"},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got := s.agentScopeModel(dummyRequest(), "agt_nomodel")
	if got != "" {
		t.Errorf("agentScopeModel = %q, want empty", got)
	}
}

// --- Tests for agentScopePromptMode ---

func TestAgentScopePromptMode_Found(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	rec := &store.ConfigRecord{
		ID:      "cfg_pm_1",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_pm",
		Name:    "agents.defaults",
		Enabled: true,
		Data:    map[string]interface{}{"promptMode": "structured"},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got := s.agentScopePromptMode(dummyRequest(), "agt_pm")
	if got != "structured" {
		t.Errorf("agentScopePromptMode = %q, want %q", got, "structured")
	}
}

func TestAgentScopePromptMode_NotFound(t *testing.T) {
	s := setupTestServer(t)

	got := s.agentScopePromptMode(dummyRequest(), "agt_nonexistent")
	if got != "" {
		t.Errorf("agentScopePromptMode = %q, want empty", got)
	}
}

func TestAgentScopePromptMode_NoField(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	rec := &store.ConfigRecord{
		ID:      "cfg_pm_nofield",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_pm_nofield",
		Name:    "agents.defaults",
		Enabled: true,
		Data:    map[string]interface{}{"model": "claude-sonnet-4-6"},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got := s.agentScopePromptMode(dummyRequest(), "agt_pm_nofield")
	if got != "" {
		t.Errorf("agentScopePromptMode = %q, want empty", got)
	}
}

// --- Tests for agentScopeSplitReplies ---

func TestAgentScopeSplitReplies_True(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	rec := &store.ConfigRecord{
		ID:      "cfg_sr_true",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_sr_true",
		Name:    "agents.defaults",
		Enabled: true,
		Data:    map[string]interface{}{"splitReplies": true},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got := s.agentScopeSplitReplies(dummyRequest(), "agt_sr_true")
	if got == nil {
		t.Fatal("agentScopeSplitReplies = nil, want *true")
	}
	if *got != true {
		t.Errorf("agentScopeSplitReplies = %v, want true", *got)
	}
}

func TestAgentScopeSplitReplies_False(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	rec := &store.ConfigRecord{
		ID:      "cfg_sr_false",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_sr_false",
		Name:    "agents.defaults",
		Enabled: true,
		Data:    map[string]interface{}{"splitReplies": false},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got := s.agentScopeSplitReplies(dummyRequest(), "agt_sr_false")
	if got == nil {
		t.Fatal("agentScopeSplitReplies = nil, want *false")
	}
	if *got != false {
		t.Errorf("agentScopeSplitReplies = %v, want false", *got)
	}
}

func TestAgentScopeSplitReplies_NotFound(t *testing.T) {
	s := setupTestServer(t)

	got := s.agentScopeSplitReplies(dummyRequest(), "agt_nonexistent")
	if got != nil {
		t.Errorf("agentScopeSplitReplies = %v, want nil", *got)
	}
}

func TestAgentScopeSplitReplies_NoField(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	rec := &store.ConfigRecord{
		ID:      "cfg_sr_nofield",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_sr_nofield",
		Name:    "agents.defaults",
		Enabled: true,
		Data:    map[string]interface{}{"model": "claude-sonnet-4-6"},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got := s.agentScopeSplitReplies(dummyRequest(), "agt_sr_nofield")
	if got != nil {
		t.Errorf("agentScopeSplitReplies = %v, want nil", *got)
	}
}

// --- Tests for agentScopeAutoPersist ---

func TestAgentScopeAutoPersist_True(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	rec := &store.ConfigRecord{
		ID:      "cfg_ap_true",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_ap_true",
		Name:    "agents.defaults",
		Enabled: true,
		Data:    map[string]interface{}{"autoPersist": true},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got := s.agentScopeAutoPersist(dummyRequest(), "agt_ap_true")
	if got == nil {
		t.Fatal("agentScopeAutoPersist = nil, want *true")
	}
	if *got != true {
		t.Errorf("agentScopeAutoPersist = %v, want true", *got)
	}
}

func TestAgentScopeAutoPersist_False(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	rec := &store.ConfigRecord{
		ID:      "cfg_ap_false",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_ap_false",
		Name:    "agents.defaults",
		Enabled: true,
		Data:    map[string]interface{}{"autoPersist": false},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got := s.agentScopeAutoPersist(dummyRequest(), "agt_ap_false")
	if got == nil {
		t.Fatal("agentScopeAutoPersist = nil, want *false")
	}
	if *got != false {
		t.Errorf("agentScopeAutoPersist = %v, want false", *got)
	}
}

func TestAgentScopeAutoPersist_NotFound(t *testing.T) {
	s := setupTestServer(t)

	got := s.agentScopeAutoPersist(dummyRequest(), "agt_nonexistent")
	if got != nil {
		t.Errorf("agentScopeAutoPersist = %v, want nil", *got)
	}
}

func TestAgentScopeAutoPersist_NoField(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	rec := &store.ConfigRecord{
		ID:      "cfg_ap_nofield",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_ap_nofield",
		Name:    "agents.defaults",
		Enabled: true,
		Data:    map[string]interface{}{"model": "claude-sonnet-4-6"},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got := s.agentScopeAutoPersist(dummyRequest(), "agt_ap_nofield")
	if got != nil {
		t.Errorf("agentScopeAutoPersist = %v, want nil", *got)
	}
}

// --- Combined test: all fields in one row ---

func TestAgentScopeDefaults_AllFields(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	// Seed: one row with all fields
	rec := &store.ConfigRecord{
		ID:      "cfg_all",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_all",
		Name:    "agents.defaults",
		Enabled: true,
		Data: map[string]interface{}{
			"model":        "openrouter/deepseek/deepseek-r1",
			"promptMode":   "structured",
			"splitReplies": true,
			"autoPersist":  false,
		},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
	}

	r := dummyRequest()

	// All 4 functions should read from the same row
	model := s.agentScopeModel(r, "agt_all")
	if model != "openrouter/deepseek/deepseek-r1" {
		t.Errorf("model = %q, want %q", model, "openrouter/deepseek/deepseek-r1")
	}

	promptMode := s.agentScopePromptMode(r, "agt_all")
	if promptMode != "structured" {
		t.Errorf("promptMode = %q, want %q", promptMode, "structured")
	}

	splitReplies := s.agentScopeSplitReplies(r, "agt_all")
	if splitReplies == nil || *splitReplies != true {
		t.Errorf("splitReplies = %v, want *true", splitReplies)
	}

	autoPersist := s.agentScopeAutoPersist(r, "agt_all")
	if autoPersist == nil || *autoPersist != false {
		t.Errorf("autoPersist = %v, want *false", autoPersist)
	}
}

// --- Test for agentScopePlugins (separate row) ---

func TestAgentScopePlugins_Found(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	// Written through the scope helper: the row lives under its own kind
	// (not kind=setting), which is what keeps its mirror keys out of the
	// "plugins" settings namespace.
	if err := scope.SaveAgentPluginEnabled(ctx, s.dataStore, "agt_plugins", map[string]bool{
		"web_search": true,
		"code_exec":  false,
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if rec, err := s.dataStore.GetConfigByName(ctx, store.KindPluginEnabled, "", "agt_plugins", "plugins.enabled"); err != nil || rec == nil {
		t.Fatalf("row not stored under KindPluginEnabled: %v %v", rec, err)
	}

	got := s.agentScopePlugins(dummyRequest(), "agt_plugins")
	if got == nil {
		t.Fatal("agentScopePlugins = nil, want map")
	}
	if got["web_search"] != true {
		t.Errorf("web_search = %v, want true", got["web_search"])
	}
	if got["code_exec"] != false {
		t.Errorf("code_exec = %v, want false", got["code_exec"])
	}
}

func TestAgentScopePlugins_NotFound(t *testing.T) {
	s := setupTestServer(t)

	got := s.agentScopePlugins(dummyRequest(), "agt_nonexistent")
	if got != nil {
		t.Errorf("agentScopePlugins = %v, want nil", got)
	}
}

// --- Read-model convergence ---

// The per-agent readers used to read the configs row themselves, so a row that
// exists only in the configs_kv mirror was invisible to the dashboard while the
// runtime (which resolves through the scope layer) served it — the "panel says
// nothing, the agent says something" shape that produced the web_search
// incident. They resolve through the same read model now, and this pins it: the
// row below has no blob counterpart at all.
func TestAgentScopeReadsSeeMirrorOnlyRows(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	// agents.defaults is the one namespace whose mirror prefix is not its name.
	if err := s.dataStore.SetConfigValue(ctx, store.KindSetting, "agent", "agt_mirror",
		"agent.model", store.StringValue("openrouter/mirror")); err != nil {
		t.Fatalf("seed mirror row: %v", err)
	}

	if got := s.agentScopeModel(dummyRequest(), "agt_mirror"); got != "openrouter/mirror" {
		t.Errorf("agentScopeModel = %q, want the mirror row's model", got)
	}
	if got := s.agentScopeDefaults(dummyRequest(), "agt_mirror"); got.Model != "openrouter/mirror" {
		t.Errorf("agentScopeDefaults.Model = %q, want the mirror row's model", got.Model)
	}
	if got := s.agentScopeDefaultsRead(dummyRequest(), "agt_mirror"); got["model"] != "openrouter/mirror" {
		t.Errorf("agentScopeDefaultsRead = %#v, want the mirror row's model", got)
	}
}

// A disabled row is the agent layer saying "not here": no reader serves it, and
// the mirror must not resurrect it.
func TestAgentScopeReadsHonourDisabledRows(t *testing.T) {
	s := setupTestServer(t)
	ctx := context.Background()

	if err := s.dataStore.SaveConfig(ctx, &store.ConfigRecord{
		ID: "cfg_disabled", Kind: store.KindSetting, AgentID: "agt_off",
		Name: "agents.defaults", Enabled: false,
		Data: map[string]interface{}{"model": "from-blob", "promptMode": "structured"},
	}); err != nil {
		t.Fatalf("save disabled row: %v", err)
	}
	if err := s.dataStore.SetConfigValue(ctx, store.KindSetting, "agent", "agt_off",
		"agent.model", store.StringValue("from-mirror")); err != nil {
		t.Fatalf("seed mirror row: %v", err)
	}

	if got := s.agentScopeModel(dummyRequest(), "agt_off"); got != "" {
		t.Errorf("agentScopeModel = %q, want empty for a disabled row", got)
	}
	if got := s.agentScopePromptMode(dummyRequest(), "agt_off"); got != "" {
		t.Errorf("agentScopePromptMode = %q, want empty for a disabled row", got)
	}
}

// The PATCH handlers build their next write by assigning into what this returns,
// so "no row" has to come back as an assignable map rather than nil.
func TestAgentScopeDefaultsReadIsAlwaysAssignable(t *testing.T) {
	s := setupTestServer(t)

	got := s.agentScopeDefaultsRead(dummyRequest(), "agt_absent")
	if got == nil {
		t.Fatal("agentScopeDefaultsRead returned nil, which callers cannot assign into")
	}
	got["model"] = "patched"
}
