package scope

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func setupScopeTestStore(t *testing.T) *store.DBStore {
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

func TestBatchSettings_UserOverridesSystem(t *testing.T) {
	st := setupScopeTestStore(t)
	ctx := context.Background()

	// System-level agents.defaults
	if err := st.SaveConfig(ctx, &store.ConfigRecord{
		ID: "cfg_sys_ad", Kind: store.KindSetting, UserID: "", AgentID: "",
		Name: "agents.defaults", Enabled: true,
		Data: map[string]interface{}{"model": "claude-sonnet-4-6", "promptMode": "natural"},
	}); err != nil {
		t.Fatalf("save system config: %v", err)
	}

	// User-level agents.defaults (overrides model)
	if err := st.SaveConfig(ctx, &store.ConfigRecord{
		ID: "cfg_usr_ad", Kind: store.KindSetting, UserID: "u_test", AgentID: "",
		Name: "agents.defaults", Enabled: true,
		Data: map[string]interface{}{"model": "claude-opus-4-6"},
	}); err != nil {
		t.Fatalf("save user config: %v", err)
	}

	merged, err := BatchSettings(ctx, st, []string{"agents.defaults"}, "u_test", "")
	if err != nil {
		t.Fatalf("BatchSettings: %v", err)
	}

	ad := merged["agents.defaults"]
	if ad == nil {
		t.Fatal("agents.defaults not found in merged result")
	}
	// User overrides system for "model"
	if ad["model"] != "claude-opus-4-6" {
		t.Errorf("model = %v, want %q", ad["model"], "claude-opus-4-6")
	}
	// System value preserved for "promptMode" (user didn't override)
	if ad["promptMode"] != "natural" {
		t.Errorf("promptMode = %v, want %q", ad["promptMode"], "natural")
	}
}

func TestBatchSettings_SystemOnly(t *testing.T) {
	st := setupScopeTestStore(t)
	ctx := context.Background()

	if err := st.SaveConfig(ctx, &store.ConfigRecord{
		ID: "cfg_sys_sb", Kind: store.KindSetting, UserID: "", AgentID: "",
		Name: "sandbox", Enabled: true,
		Data: map[string]interface{}{"enabled": true},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	merged, err := BatchSettings(ctx, st, []string{"sandbox"}, "u_new", "")
	if err != nil {
		t.Fatalf("BatchSettings: %v", err)
	}

	sb := merged["sandbox"]
	if sb == nil {
		t.Fatal("sandbox not found")
	}
	if sb["enabled"] != true {
		t.Errorf("enabled = %v, want true", sb["enabled"])
	}
}

func TestBatchSettings_MissingNamespace(t *testing.T) {
	st := setupScopeTestStore(t)
	ctx := context.Background()

	// No rows exist for "objectstore"
	merged, err := BatchSettings(ctx, st, []string{"objectstore"}, "u_test", "")
	if err != nil {
		t.Fatalf("BatchSettings: %v", err)
	}

	if merged["objectstore"] != nil {
		t.Errorf("objectstore = %v, want nil", merged["objectstore"])
	}
}

func TestBatchSettings_MultipleNamespaces(t *testing.T) {
	st := setupScopeTestStore(t)
	ctx := context.Background()

	configs := []*store.ConfigRecord{
		{ID: "cfg_m1", Kind: store.KindSetting, UserID: "", AgentID: "", Name: "agents.defaults", Enabled: true, Data: map[string]interface{}{"model": "claude-sonnet-4-6"}},
		{ID: "cfg_m2", Kind: store.KindSetting, UserID: "", AgentID: "", Name: "sandbox", Enabled: true, Data: map[string]interface{}{"enabled": true}},
		{ID: "cfg_m3", Kind: store.KindSetting, UserID: "", AgentID: "", Name: "objectstore", Enabled: true, Data: map[string]interface{}{"bucket": "my-bucket"}},
	}
	for _, cfg := range configs {
		if err := st.SaveConfig(ctx, cfg); err != nil {
			t.Fatalf("save config: %v", err)
		}
	}

	merged, err := BatchSettings(ctx, st, []string{"agents.defaults", "sandbox", "objectstore", "nonexistent"}, "", "")
	if err != nil {
		t.Fatalf("BatchSettings: %v", err)
	}

	if merged["agents.defaults"]["model"] != "claude-sonnet-4-6" {
		t.Errorf("agents.defaults.model = %v", merged["agents.defaults"]["model"])
	}
	if merged["sandbox"]["enabled"] != true {
		t.Errorf("sandbox.enabled = %v", merged["sandbox"]["enabled"])
	}
	if merged["objectstore"]["bucket"] != "my-bucket" {
		t.Errorf("objectstore.bucket = %v", merged["objectstore"]["bucket"])
	}
	if merged["nonexistent"] != nil {
		t.Errorf("nonexistent = %v, want nil", merged["nonexistent"])
	}
}

func TestBatchSettings_DisabledRowIgnored(t *testing.T) {
	st := setupScopeTestStore(t)
	ctx := context.Background()

	if err := st.SaveConfig(ctx, &store.ConfigRecord{
		ID: "cfg_dis", Kind: store.KindSetting, UserID: "", AgentID: "",
		Name: "agents.defaults", Enabled: false,
		Data: map[string]interface{}{"model": "should-not-appear"},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	merged, err := BatchSettings(ctx, st, []string{"agents.defaults"}, "", "")
	if err != nil {
		t.Fatalf("BatchSettings: %v", err)
	}

	if merged["agents.defaults"] != nil {
		t.Errorf("disabled row should be ignored, got %v", merged["agents.defaults"])
	}
}

func TestBatchSettings_NilStore(t *testing.T) {
	_, err := BatchSettings(context.Background(), nil, []string{"agents.defaults"}, "", "")
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

// TestBatchSettings_MatchesSetting verifies that BatchSettings produces the
// same result as calling Setting() individually for each namespace. This is
// the key behavioral contract — the optimization must not change semantics.
func TestBatchSettings_MatchesSetting(t *testing.T) {
	st := setupScopeTestStore(t)
	ctx := context.Background()

	// Seed: system + user configs across multiple namespaces
	configs := []*store.ConfigRecord{
		{ID: "cfg_eq1", Kind: store.KindSetting, UserID: "", AgentID: "", Name: "agents.defaults", Enabled: true, Data: map[string]interface{}{"model": "sys-model", "promptMode": "natural"}},
		{ID: "cfg_eq2", Kind: store.KindSetting, UserID: "u1", AgentID: "", Name: "agents.defaults", Enabled: true, Data: map[string]interface{}{"model": "user-model"}},
		{ID: "cfg_eq3", Kind: store.KindSetting, UserID: "", AgentID: "", Name: "sandbox", Enabled: true, Data: map[string]interface{}{"enabled": true, "timeout": float64(30)}},
	}
	for _, cfg := range configs {
		if err := st.SaveConfig(ctx, cfg); err != nil {
			t.Fatalf("save config: %v", err)
		}
	}

	namespaces := []string{"agents.defaults", "sandbox"}

	// Get result from BatchSettings
	batch, err := BatchSettings(ctx, st, namespaces, "u1", "")
	if err != nil {
		t.Fatalf("BatchSettings: %v", err)
	}

	// Compare with individual Setting() calls
	for _, ns := range namespaces {
		individual, err := Setting(ctx, st, ns, "u1", "")
		if err != nil {
			t.Fatalf("Setting(%q): %v", ns, err)
		}
		batchNS := batch[ns]
		if batchNS == nil {
			batchNS = map[string]interface{}{}
		}
		// Compare each key
		for k, v := range individual {
			if batchNS[k] != v {
				t.Errorf("namespace %q key %q: batch=%v, individual=%v", ns, k, batchNS[k], v)
			}
		}
		for k, v := range batchNS {
			if individual[k] != v {
				t.Errorf("namespace %q key %q: batch=%v, individual=%v (extra in batch)", ns, k, v, individual[k])
			}
		}
	}
}
