package store

import (
	"context"
	"errors"
	"testing"
)

// TestMigratePluginEnabledKind pins the kind migration: the per-agent plugin
// opt-in row and its mirror keys move out of kind=setting into their own
// kind, while the "plugins" settings namespace's own scalar
// "plugins.enabled" mirror row stays exactly where it is.
func TestMigratePluginEnabledKind(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Legacy shape: one agent-scope opt-in row + the system "plugins"
	// namespace row, all under kind=setting.
	if err := db.SaveConfig(ctx, &ConfigRecord{
		Kind: KindSetting, UserID: "", AgentID: "agent-x", Name: "plugins.enabled",
		Enabled: true, Data: map[string]interface{}{"browserUse": true},
	}); err != nil {
		t.Fatalf("save legacy opt-in row: %v", err)
	}
	if err := db.SaveConfig(ctx, &ConfigRecord{
		Kind: KindSetting, UserID: "", AgentID: "", Name: "plugins",
		Enabled: true, Data: map[string]interface{}{"enabled": false},
	}); err != nil {
		t.Fatalf("save system plugins row: %v", err)
	}
	// Mirror rows for both, in the shape their writers produce.
	if err := db.SetConfigValue(ctx, KindSetting, "agent", "agent-x", "plugins.enabled.browserUse", "true"); err != nil {
		t.Fatalf("seed mirror opt-in: %v", err)
	}
	if err := db.SetConfigValue(ctx, KindSetting, "system", "", "plugins.enabled", "false"); err != nil {
		t.Fatalf("seed mirror scalar: %v", err)
	}

	if err := db.migratePluginEnabledKind(ctx); err != nil {
		t.Fatalf("migratePluginEnabledKind: %v", err)
	}

	// Blob row moved.
	if _, err := db.GetConfigByName(ctx, KindPluginEnabled, "", "agent-x", "plugins.enabled"); err != nil {
		t.Fatalf("opt-in row not under KindPluginEnabled: %v", err)
	}
	if _, err := db.GetConfigByName(ctx, KindSetting, "", "agent-x", "plugins.enabled"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy row still under KindSetting: %v", err)
	}
	// Mirror: the agent's key moved…
	if v, err := db.GetConfigValue(ctx, KindPluginEnabled, "agent", "agent-x", "plugins.enabled.browserUse"); err != nil || v != "true" {
		t.Fatalf("mirror opt-in not moved: %q %v", v, err)
	}
	// …and the system scalar did not.
	if v, err := db.GetConfigValue(ctx, KindSetting, "system", "", "plugins.enabled"); err != nil || v != "false" {
		t.Fatalf("system scalar mirror was disturbed: %q %v", v, err)
	}

	// Idempotent: a second run must not error (including the UNIQUE
	// (kind,user_id,agent_id,name) guard).
	if err := db.migratePluginEnabledKind(ctx); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
