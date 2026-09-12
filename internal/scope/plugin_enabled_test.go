package scope

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// TestAgentPluginEnabledRoundTrip pins the storage shape of the per-agent
// plugin opt-in row: its own kind (so it can't share a configs_kv partition
// with the "plugins" settings namespace), plugin ids kept verbatim, and
// reset deleting both the blob row and the mirror.
func TestAgentPluginEnabledRoundTrip(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := SaveAgentPluginEnabled(ctx, db, "agent-x", map[string]bool{
		"browserUse":   true,
		"BROWSER_TOOL": false,
		"mem0":         true,
	}); err != nil {
		t.Fatalf("SaveAgentPluginEnabled: %v", err)
	}

	// Blob row exists under its own kind only — the old kind=setting name
	// is what collided with the "plugins" namespace.
	if _, err := db.GetConfigByName(ctx, store.KindPluginEnabled, "", "agent-x", PluginEnabledNamespace); err != nil {
		t.Fatalf("row not stored under KindPluginEnabled: %v", err)
	}
	if _, err := db.GetConfigByName(ctx, store.KindSetting, "", "agent-x", PluginEnabledNamespace); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row leaked into kind=setting: err=%v", err)
	}

	got, err := AgentPluginEnabled(ctx, db, "agent-x")
	if err != nil {
		t.Fatalf("AgentPluginEnabled: %v", err)
	}
	if !got["browserUse"] || got["BROWSER_TOOL"] || !got["mem0"] {
		t.Fatalf("round trip lost values: %v", got)
	}

	// Mirror rows carry the plugin id as a data key (verbatim), which is
	// the kvkeys {"plugins","enabled","*"} rule.
	kv, err := db.ListConfigValues(ctx, store.KindPluginEnabled, Agent, "agent-x", PluginEnabledNamespace+".")
	if err != nil {
		t.Fatalf("ListConfigValues: %v", err)
	}
	if kv["plugins.enabled.browserUse"].Value != "true" || kv["plugins.enabled.BROWSER_TOOL"].Value != "false" {
		t.Fatalf("mirror keys were folded: %v", kv)
	}

	// Reset drops the row and the mirror.
	if err := SaveAgentPluginEnabled(ctx, db, "agent-x", nil); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if again, err := AgentPluginEnabled(ctx, db, "agent-x"); err != nil || again != nil {
		t.Fatalf("reset left a row: %v %v", again, err)
	}
	if kv, err := db.ListConfigValues(ctx, store.KindPluginEnabled, Agent, "agent-x", PluginEnabledNamespace+"."); err == nil && len(kv) != 0 {
		t.Fatalf("reset left mirror rows: %v", kv)
	}
}

// TestPluginsNamespaceIgnoresAgentOptIns is the collision regression: the
// "plugins" settings namespace and the per-agent opt-in row must never
// appear in each other's reads — including on the configs_kv fallback path,
// where both used to live under kind=setting ("plugins.enabled" scalar vs
// "plugins.enabled.<id>" keys in the same partition, merged in random map
// order).
func TestPluginsNamespaceIgnoresAgentOptIns(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "plugins", map[string]interface{}{
		"enabled": false,
		"paths":   []interface{}{"/opt/plugins"},
	}); err != nil {
		t.Fatalf("save system plugins: %v", err)
	}
	if err := SaveAgentPluginEnabled(ctx, db, "agent-x", map[string]bool{"browserUse": true}); err != nil {
		t.Fatalf("save agent opt-in: %v", err)
	}

	assertClean := func(when string) {
		t.Helper()
		merged, err := Setting(ctx, db, "plugins", "", "agent-x")
		if err != nil {
			t.Fatalf("%s: Setting(plugins): %v", when, err)
		}
		if merged["enabled"] != false {
			t.Fatalf("%s: plugins.enabled must stay the system bool, got %#v", when, merged["enabled"])
		}
		if _, ok := merged["browserUse"]; ok {
			t.Fatalf("%s: agent opt-in leaked into the plugins namespace: %#v", when, merged)
		}
	}

	// Blob present: the system row shadows nothing.
	assertClean("blob")

	// Force the configs_kv fallback: with the opt-in rows still in the
	// "setting" partition the merged map would gain "enabled" as a map (or
	// worse, overwrite the bool).
	// Pin the partition itself too — deterministic, unlike merge order. The
	// agent-scope partition is where the opt-in rows used to land when the
	// row was still kind=setting.
	for _, sc := range []struct{ scope, scopeID string }{{System, ""}, {Agent, "agent-x"}} {
		kv, err := db.ListConfigValues(ctx, store.KindSetting, sc.scope, sc.scopeID, "plugins.")
		if err != nil {
			t.Fatalf("ListConfigValues(setting, %s, %s): %v", sc.scope, sc.scopeID, err)
		}
		for name := range kv {
			if strings.HasPrefix(name, "plugins.enabled.") {
				t.Fatalf("agent opt-in row shares the settings partition (%s): %q", sc.scope, name)
			}
		}
	}
	rec, err := db.GetConfigByName(ctx, store.KindSetting, "", "", "plugins")
	if err != nil {
		t.Fatalf("reload system plugins row: %v", err)
	}
	if err := db.DeleteConfig(ctx, rec.ID); err != nil {
		t.Fatalf("delete system plugins row: %v", err)
	}
	assertClean("kv fallback")
}
