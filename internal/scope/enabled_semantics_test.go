package scope

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// The enabled column is a resolution rule, not decoration: a row with
// enabled=false is this layer's decision that the name does not exist. It
// contributes nothing AND it blocks every outer layer's entry of the same
// name, so "disable" cannot silently fall back to the operator's value. A
// blob row of any kind also keeps the configs_kv mirror out of the answer.
//
// These tests pin the rule for every reader, because the whole point of the
// alignment is that they all answer the same way.

func seedRow(t *testing.T, db *store.DBStore, kind, uid, aid, name string, enabled bool, data map[string]interface{}) {
	t.Helper()
	if err := db.SaveConfig(context.Background(), &store.ConfigRecord{
		ID: "cfg_" + kind + "_" + uid + aid + name, Kind: kind,
		UserID: uid, AgentID: aid, Name: name, Enabled: enabled, Data: data,
	}); err != nil {
		t.Fatalf("seed %s/%s/%s: %v", kind, uid+aid, name, err)
	}
}

// A disabled user row erases the system provider: the user opted out, so the
// operator's credential must not come back.
func TestEnabledVeto_ProviderErasesOuter(t *testing.T) {
	db := openScopeDBNamed(t, "enabled_providers")
	defer db.Close()
	ctx := context.Background()

	seedRow(t, db, store.KindProvider, "", "", "openai", true, map[string]interface{}{"apiKey": "system-key"})
	seedRow(t, db, store.KindProvider, "u1", "", "openai", false, nil)

	got, err := Providers(ctx, db, "u1", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if pc, ok := got["openai"]; ok {
		t.Fatalf("disabled user row did not erase the system provider: %+v", pc)
	}
}

// The veto is per layer, not global: an inner enabled row re-enables a name
// an outer layer switched off.
func TestEnabledVeto_InnerLayerReenables(t *testing.T) {
	db := openScopeDBNamed(t, "enabled_reenable")
	defer db.Close()
	ctx := context.Background()

	seedRow(t, db, store.KindProvider, "", "", "openai", false, map[string]interface{}{"apiKey": "off"})
	seedRow(t, db, store.KindProvider, "u1", "", "openai", true, map[string]interface{}{"apiKey": "user-key"})

	got, err := Providers(ctx, db, "u1", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if got["openai"].APIKey != "user-key" {
		t.Fatalf("inner row should re-enable the name, got %+v", got["openai"])
	}
}

// Settings merge field-wise, so the veto drops the fields the outer layers
// contributed for that namespace (and the disabled row's own data with it).
func TestEnabledVeto_SettingDropsOuterFields(t *testing.T) {
	db := openScopeDBNamed(t, "enabled_setting")
	defer db.Close()
	ctx := context.Background()

	seedRow(t, db, store.KindSetting, "", "", "agents.defaults", true,
		map[string]interface{}{"model": "sys-model", "promptMode": "natural"})
	seedRow(t, db, store.KindSetting, "u1", "", "agents.defaults", false,
		map[string]interface{}{"maxTokens": 123})

	got, err := Setting(ctx, db, "agents.defaults", "u1", "")
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("disabled row should drop the outer fields, got %#v", got)
	}
}

// A blob row owns the name even when it is disabled: the mirror holds rows
// written straight into configs_kv, and a veto is a decision, not an absence.
func TestEnabledVeto_BlocksMirrorFallback(t *testing.T) {
	db := openScopeDBNamed(t, "enabled_mirror_block")
	defer db.Close()
	ctx := context.Background()

	if err := db.SetConfigValue(ctx, store.KindProvider, System, "", "openai.api_key",
		store.EncodeConfigValue("mirror-key")); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	if err := db.SetConfigValue(ctx, store.KindSetting, System, "", "prefs.timezone",
		store.EncodeConfigValue("Asia/Shanghai")); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	seedRow(t, db, store.KindProvider, "", "", "openai", false, nil)
	seedRow(t, db, store.KindSetting, "", "", "prefs", false, nil)

	provs, err := Providers(ctx, db, "", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if len(provs) != 0 {
		t.Fatalf("mirror resurrected a vetoed provider: %#v", provs)
	}
	ns, err := Setting(ctx, db, "prefs", "", "")
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	if len(ns) != 0 {
		t.Fatalf("mirror resurrected a vetoed namespace: %#v", ns)
	}
}

// The mirror fallback is decided per name, not per chain: a provider that
// only the mirror carries stays visible next to blob-backed siblings.
func TestProvidersMirrorFallbackIsPerName(t *testing.T) {
	db := openScopeDBNamed(t, "enabled_mirror_pername")
	defer db.Close()
	ctx := context.Background()

	seedRow(t, db, store.KindProvider, "", "", "openai", true, map[string]interface{}{"apiKey": "blob-key"})
	if err := db.SetConfigValue(ctx, store.KindProvider, System, "", "anthropic.api_key",
		store.EncodeConfigValue("mirror-key")); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	got, err := Providers(ctx, db, "", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if got["openai"].APIKey != "blob-key" {
		t.Fatalf("blob provider lost: %#v", got)
	}
	if got["anthropic"].APIKey != "mirror-key" {
		t.Fatalf("mirror-only provider hidden behind a blob sibling: %#v", got)
	}

	// Same rule for the single-layer readers used by the foreign-agent path.
	if err := db.SetConfigValue(ctx, store.KindProvider, User, "u1", "anthropic.api_key",
		store.EncodeConfigValue("user-mirror-key")); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	seedRow(t, db, store.KindProvider, "u1", "", "openai", true, map[string]interface{}{"apiKey": "user-blob-key"})
	scoped, err := UserScopeProviders(ctx, db, "u1")
	if err != nil {
		t.Fatalf("UserScopeProviders: %v", err)
	}
	if scoped["anthropic"].APIKey != "user-mirror-key" || scoped["openai"].APIKey != "user-blob-key" {
		t.Fatalf("UserScopeProviders mixed up blob and mirror: %#v", scoped)
	}
}

// ExactSetting reads one layer: a disabled row there means "nothing", and it
// must neither fall through to the mirror nor to another layer.
func TestExactSettingVeto(t *testing.T) {
	db := openScopeDBNamed(t, "enabled_exact")
	defer db.Close()
	ctx := context.Background()

	if err := db.SetConfigValue(ctx, store.KindSetting, User, "u1", "prefs.timezone",
		store.EncodeConfigValue("Asia/Shanghai")); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	seedRow(t, db, store.KindSetting, "u1", "", "prefs", false, nil)

	var out config.PrefsCfg
	if err := ExactSetting(ctx, db, "prefs", "u1", "", &out); err != nil {
		t.Fatalf("ExactSetting: %v", err)
	}
	if out.Timezone != "" {
		t.Fatalf("mirror resurrected a vetoed row: %q", out.Timezone)
	}
}

// The per-agent plugin opt-in row and the timezone resolver are readers too.
func TestEnabledVeto_PluginEnabledAndTimezone(t *testing.T) {
	db := openScopeDBNamed(t, "enabled_plugin_tz")
	defer db.Close()
	ctx := context.Background()

	seedRow(t, db, store.KindPluginEnabled, "", "a1", PluginEnabledNamespace, false,
		map[string]interface{}{"webSearch": true})
	got, err := AgentPluginEnabled(ctx, db, "a1")
	if err != nil {
		t.Fatalf("AgentPluginEnabled: %v", err)
	}
	if got != nil {
		t.Fatalf("disabled opt-in row should read as no overrides, got %#v", got)
	}

	seedRow(t, db, store.KindSetting, "u1", "", PrefsNamespace, false,
		map[string]interface{}{"timezone": "Asia/Shanghai"})
	seedRow(t, db, store.KindSetting, "", "", PrefsNamespace, true,
		map[string]interface{}{"timezone": "UTC"})
	if tz := Timezone(ctx, db, "u1", ""); tz != "" {
		t.Fatalf("disabled prefs row should veto the outer timezone, got %q", tz)
	}
}

// The write half: SaveProvider marks a provider live, SaveProviderState can
// switch it off, and switching it back on restores it.
func TestSaveProviderStateToggle(t *testing.T) {
	db := openScopeDBNamed(t, "enabled_write")
	defer db.Close()
	ctx := context.Background()

	pc := config.ProviderConfig{APIKey: "sk-1"}
	if err := SaveProvider(ctx, db, "u1", "", "openai", pc); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}
	if err := SaveProviderState(ctx, db, "u1", "", "openai", pc, false); err != nil {
		t.Fatalf("SaveProviderState(false): %v", err)
	}
	off, err := Providers(ctx, db, "u1", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if _, ok := off["openai"]; ok {
		t.Fatal("provider still visible after being switched off")
	}
	if err := SaveProviderState(ctx, db, "u1", "", "openai", pc, true); err != nil {
		t.Fatalf("SaveProviderState(true): %v", err)
	}
	on, err := Providers(ctx, db, "u1", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if _, ok := on["openai"]; !ok {
		t.Fatal("provider did not come back after being switched on")
	}
}

// BatchSettings is an optimization of Setting, so the two must agree on the
// whole enabled matrix — including the case that motivated the alignment: a
// disabled inner row over an enabled outer one.
func TestBatchSettingsMatchesSettingOnEnabledMatrix(t *testing.T) {
	cases := []struct {
		name              string
		system, user      bool // row enabled
		systemData, userD map[string]interface{}
	}{
		{"both enabled", true, true,
			map[string]interface{}{"model": "sys", "promptMode": "natural"},
			map[string]interface{}{"model": "user"}},
		{"user vetoes system", true, false,
			map[string]interface{}{"model": "sys"}, nil},
		{"system off, user on", false, true,
			map[string]interface{}{"model": "sys"}, map[string]interface{}{"model": "user"}},
		{"both off", false, false, map[string]interface{}{"model": "sys"}, nil},
		{"system only", true, false, map[string]interface{}{"model": "sys"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openScopeDBNamed(t, "enabled_parity_"+tc.name)
			defer db.Close()
			ctx := context.Background()
			const ns = "agents.defaults"

			seedRow(t, db, store.KindSetting, "", "", ns, tc.system, tc.systemData)
			if tc.user || tc.userD != nil {
				seedRow(t, db, store.KindSetting, "u1", "", ns, tc.user, tc.userD)
			}

			single, err := Setting(ctx, db, ns, "u1", "")
			if err != nil {
				t.Fatalf("Setting: %v", err)
			}
			batch, err := BatchSettings(ctx, db, []string{ns}, "u1", "")
			if err != nil {
				t.Fatalf("BatchSettings: %v", err)
			}
			got := batch[ns] // absent means "resolved to nothing"
			if len(single) != len(got) {
				t.Fatalf("Setting = %#v, BatchSettings = %#v", single, got)
			}
			for k, v := range single {
				if got[k] != v {
					t.Fatalf("Setting[%s] = %#v, BatchSettings[%s] = %#v", k, v, k, got[k])
				}
			}
		})
	}
}
