package setup

import (
	"context"
	"net/http"
	"testing"

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

	rec := &store.ConfigRecord{
		ID:      "cfg_plugins_1",
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_plugins",
		Name:    "plugins.enabled",
		Enabled: true,
		Data: map[string]interface{}{
			"web_search": true,
			"code_exec":  false,
		},
	}
	if err := s.dataStore.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("save config: %v", err)
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
