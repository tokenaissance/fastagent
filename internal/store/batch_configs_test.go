package store

import (
	"context"
	"testing"
)

func setupBatchTestStore(t *testing.T) *DBStore {
	t.Helper()
	db, err := NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestBatchGetConfigsByAgentIDs_MultipleAgents(t *testing.T) {
	st := setupBatchTestStore(t)
	ctx := context.Background()

	configs := []*ConfigRecord{
		{ID: "cfg_b1", Kind: KindSetting, UserID: "", AgentID: "agt_1", Name: "agents.defaults", Enabled: true, Data: map[string]interface{}{"model": "claude-sonnet-4-6"}},
		{ID: "cfg_b2", Kind: KindSetting, UserID: "", AgentID: "agt_2", Name: "agents.defaults", Enabled: true, Data: map[string]interface{}{"model": "claude-opus-4-6"}},
		{ID: "cfg_b3", Kind: KindSetting, UserID: "", AgentID: "agt_3", Name: "agents.defaults", Enabled: true, Data: map[string]interface{}{"model": "claude-haiku-4-5"}},
	}
	for _, cfg := range configs {
		if err := st.SaveConfig(ctx, cfg); err != nil {
			t.Fatalf("save config: %v", err)
		}
	}

	results, err := st.BatchGetConfigsByAgentIDs(ctx, KindSetting, "agents.defaults", []string{"agt_1", "agt_2", "agt_3"})
	if err != nil {
		t.Fatalf("BatchGetConfigsByAgentIDs: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	modelMap := make(map[string]string)
	for _, r := range results {
		if model, ok := r.Data["model"].(string); ok {
			modelMap[r.AgentID] = model
		}
	}
	if modelMap["agt_1"] != "claude-sonnet-4-6" {
		t.Errorf("agt_1 model = %q, want %q", modelMap["agt_1"], "claude-sonnet-4-6")
	}
	if modelMap["agt_2"] != "claude-opus-4-6" {
		t.Errorf("agt_2 model = %q, want %q", modelMap["agt_2"], "claude-opus-4-6")
	}
	if modelMap["agt_3"] != "claude-haiku-4-5" {
		t.Errorf("agt_3 model = %q, want %q", modelMap["agt_3"], "claude-haiku-4-5")
	}
}

func TestBatchGetConfigsByAgentIDs_Empty(t *testing.T) {
	st := setupBatchTestStore(t)

	results, err := st.BatchGetConfigsByAgentIDs(context.Background(), KindSetting, "agents.defaults", []string{})
	if err != nil {
		t.Fatalf("BatchGetConfigsByAgentIDs: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestBatchGetConfigsByAgentIDs_OnlyEnabled(t *testing.T) {
	st := setupBatchTestStore(t)
	ctx := context.Background()

	cfg := &ConfigRecord{
		ID:      "cfg_disabled",
		Kind:    KindSetting,
		UserID:  "",
		AgentID: "agt_disabled",
		Name:    "agents.defaults",
		Enabled: false,
		Data:    map[string]interface{}{"model": "test"},
	}
	if err := st.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	results, err := st.BatchGetConfigsByAgentIDs(ctx, KindSetting, "agents.defaults", []string{"agt_disabled"})
	if err != nil {
		t.Fatalf("BatchGetConfigsByAgentIDs: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 (disabled row should be excluded)", len(results))
	}
}

func TestBatchGetConfigsByAgentIDs_PartialMatch(t *testing.T) {
	st := setupBatchTestStore(t)
	ctx := context.Background()

	cfg := &ConfigRecord{
		ID:      "cfg_partial",
		Kind:    KindSetting,
		UserID:  "",
		AgentID: "agt_exists",
		Name:    "agents.defaults",
		Enabled: true,
		Data:    map[string]interface{}{"model": "gpt-4"},
	}
	if err := st.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// Query includes an agent that doesn't have a config row
	results, err := st.BatchGetConfigsByAgentIDs(ctx, KindSetting, "agents.defaults", []string{"agt_exists", "agt_missing"})
	if err != nil {
		t.Fatalf("BatchGetConfigsByAgentIDs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].AgentID != "agt_exists" {
		t.Errorf("result agent_id = %q, want %q", results[0].AgentID, "agt_exists")
	}
}

func TestBatchGetConfigsByAgentIDs_DifferentName(t *testing.T) {
	st := setupBatchTestStore(t)
	ctx := context.Background()

	// Save a row with name "plugins.enabled", not "agents.defaults"
	cfg := &ConfigRecord{
		ID:      "cfg_plugins",
		Kind:    KindSetting,
		UserID:  "",
		AgentID: "agt_plug",
		Name:    "plugins.enabled",
		Enabled: true,
		Data:    map[string]interface{}{"web_search": true},
	}
	if err := st.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// Query for "agents.defaults" should NOT return the plugins row
	results, err := st.BatchGetConfigsByAgentIDs(ctx, KindSetting, "agents.defaults", []string{"agt_plug"})
	if err != nil {
		t.Fatalf("BatchGetConfigsByAgentIDs: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 (wrong name should be excluded)", len(results))
	}

	// Query for "plugins.enabled" should return it
	results, err = st.BatchGetConfigsByAgentIDs(ctx, KindSetting, "plugins.enabled", []string{"agt_plug"})
	if err != nil {
		t.Fatalf("BatchGetConfigsByAgentIDs plugins: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("got %d results, want 1", len(results))
	}
}
