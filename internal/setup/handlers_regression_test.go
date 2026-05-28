package setup

import (
	"context"
	"reflect"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// TestLoadAgentSkillEntriesForUser_ReturnsCorrectSkills tests that
// loadAgentSkillEntriesForUser returns the correct skill entries after
// refactoring from N GetConfigByName calls to 1 BatchGetConfigsByAgentIDs call.
func TestLoadAgentSkillEntriesForUser_ReturnsCorrectSkills(t *testing.T) {
	db := setupBatchTestStore(t)
	ctx := context.Background()

	// Seed 2 agents for user_1
	agent1 := &store.AgentRecord{
		ID:     "agt_1",
		UserID: "user_1",
		Name:   "Agent 1",
		Config: map[string]interface{}{},
	}
	db.SaveAgent(ctx, agent1)

	agent2 := &store.AgentRecord{
		ID:     "agt_2",
		UserID: "user_1",
		Name:   "Agent 2",
		Config: map[string]interface{}{},
	}
	db.SaveAgent(ctx, agent2)

	// Seed skills config for agent1
	cfg1 := &store.ConfigRecord{
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_1",
		Name:    "skills.entries",
		Enabled: true,
		Data: map[string]interface{}{
			"skill_a": map[string]interface{}{
				"enabled": true,
				"apiKey":  "key_a",
			},
			"skill_b": map[string]interface{}{
				"enabled": false,
			},
		},
	}
	db.SaveConfig(ctx, cfg1)

	// Seed skills config for agent2
	cfg2 := &store.ConfigRecord{
		Kind:    store.KindSetting,
		UserID:  "",
		AgentID: "agt_2",
		Name:    "skills.entries",
		Enabled: true,
		Data: map[string]interface{}{
			"skill_c": map[string]interface{}{
				"enabled": true,
				"apiKey":  "key_c",
			},
		},
	}
	db.SaveConfig(ctx, cfg2)

	// Call loadAgentSkillEntriesForUser
	result, err := loadAgentSkillEntriesForUser(ctx, db, "user_1")
	if err != nil {
		t.Fatalf("loadAgentSkillEntriesForUser: %v", err)
	}

	// Assert agent1 skills
	if result["agt_1"] == nil {
		t.Fatal("missing agt_1 in result")
	}
	if len(result["agt_1"]) != 2 {
		t.Errorf("agt_1 has %d skills, want 2", len(result["agt_1"]))
	}

	skillA := result["agt_1"]["skill_a"]
	if !skillA.Enabled {
		t.Error("skill_a.enabled = false, want true")
	}

	skillB := result["agt_1"]["skill_b"]
	if skillB.Enabled {
		t.Error("skill_b.enabled = true, want false")
	}

	// Assert agent2 skills
	if result["agt_2"] == nil {
		t.Fatal("missing agt_2 in result")
	}
	if len(result["agt_2"]) != 1 {
		t.Errorf("agt_2 has %d skills, want 1", len(result["agt_2"]))
	}

	skillC := result["agt_2"]["skill_c"]
	if !skillC.Enabled {
		t.Error("skill_c.enabled = false, want true")
	}
}

// TestLoadAgentSkillEntriesForUser_NoSkills tests that the function
// correctly handles agents without skill configs (returns empty map).
func TestLoadAgentSkillEntriesForUser_NoSkills(t *testing.T) {
	db := setupBatchTestStore(t)
	ctx := context.Background()

	// Seed agent without skills config
	agent := &store.AgentRecord{
		ID:     "agt_noskills",
		UserID: "user_1",
		Name:   "No Skills Agent",
		Config: map[string]interface{}{},
	}
	db.SaveAgent(ctx, agent)

	result, err := loadAgentSkillEntriesForUser(ctx, db, "user_1")
	if err != nil {
		t.Fatalf("loadAgentSkillEntriesForUser: %v", err)
	}

	// Agent without skills config should not appear in result
	// (function only returns agents that have skills.entries config)
	if result["agt_noskills"] != nil {
		t.Errorf("agt_noskills should not be in result, got %v", result["agt_noskills"])
	}
}

// TestLoadAgentSkillEntriesForUser_EmptyUser tests that the function
// returns nil for empty userID.
func TestLoadAgentSkillEntriesForUser_EmptyUser(t *testing.T) {
	db := setupBatchTestStore(t)
	ctx := context.Background()

	result, err := loadAgentSkillEntriesForUser(ctx, db, "")
	if err != nil {
		t.Fatalf("loadAgentSkillEntriesForUser: %v", err)
	}
	if result != nil {
		t.Errorf("result = %v, want nil", result)
	}
}

// TestLoadAgentSkillEntriesForUser_NoAgents tests that the function
// returns empty map when user has no agents.
func TestLoadAgentSkillEntriesForUser_NoAgents(t *testing.T) {
	db := setupBatchTestStore(t)
	ctx := context.Background()

	result, err := loadAgentSkillEntriesForUser(ctx, db, "user_noagents")
	if err != nil {
		t.Fatalf("loadAgentSkillEntriesForUser: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("result has %d entries, want 0", len(result))
	}
}

// setupBatchTestStore creates a SQLite in-memory store for testing.
func setupBatchTestStore(t *testing.T) *store.DBStore {
	t.Helper()
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestLoadAgentSkillEntriesForUser_EquivalenceTest verifies that the
// refactored batch implementation returns identical results to the
// original serial implementation.
func TestLoadAgentSkillEntriesForUser_EquivalenceTest(t *testing.T) {
	db := setupBatchTestStore(t)
	ctx := context.Background()

	// Seed test data
	for i := 1; i <= 3; i++ {
		agentID := string(rune('a') + rune(i-1))
		agent := &store.AgentRecord{
			ID:     "agt_" + agentID,
			UserID: "user_test",
			Name:   "Agent " + agentID,
			Config: map[string]interface{}{},
		}
		db.SaveAgent(ctx, agent)

		cfg := &store.ConfigRecord{
			Kind:    store.KindSetting,
			UserID:  "",
			AgentID: "agt_" + agentID,
			Name:    "skills.entries",
			Enabled: true,
			Data: map[string]interface{}{
				"skill_" + agentID: map[string]interface{}{
					"enabled": true,
					"apiKey":  "key_" + agentID,
				},
			},
		}
		db.SaveConfig(ctx, cfg)
	}

	// Simulate serial approach (old code)
	agents, _ := db.ListAgents(ctx, "user_test")
	serialResult := map[string]map[string]config.SkillEntryCfg{}
	for _, ag := range agents {
		rec, _ := db.GetConfigByName(ctx, store.KindSetting, "", ag.ID, "skills.entries")
		if rec != nil {
			entries := map[string]config.SkillEntryCfg{}
			for name, val := range rec.Data {
				var entry config.SkillEntryCfg
				if m, ok := val.(map[string]interface{}); ok {
					if en, ok := m["enabled"].(bool); ok {
						entry.Enabled = en
					}
					if apiKey, ok := m["apiKey"].(string); ok {
						entry.APIKey = apiKey
					}
					if env, ok := m["env"].(map[string]string); ok {
						entry.Env = env
					}
				}
				entries[name] = entry
			}
			serialResult[ag.ID] = entries
		}
	}

	// Batch approach (new code)
	batchResult, err := loadAgentSkillEntriesForUser(ctx, db, "user_test")
	if err != nil {
		t.Fatalf("loadAgentSkillEntriesForUser: %v", err)
	}

	// Compare
	if !reflect.DeepEqual(serialResult, batchResult) {
		t.Errorf("batch result differs from serial:\nserial: %+v\nbatch: %+v", serialResult, batchResult)
	}
}
