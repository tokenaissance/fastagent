package scope

// The per-scope read models are the entry points adapters use instead of naming
// the configs table, so these tests pin the two things that makes them worth
// having: they resolve the same answer the merged resolvers do for a single
// layer, and they see rows the blob does not have (the configs_kv half) without
// resurrecting rows the layer decided to drop.

import (
	"context"
	"reflect"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// A namespace that only ever existed in configs_kv is exactly what the layer
// has to serve: a blob-first reader would answer "nothing here" for it.
func TestSettingAtServesConfigsKVOnlyRows(t *testing.T) {
	db := openScopeDBNamed(t, "readmodel_mirror_only")
	defer db.Close()
	ctx := context.Background()

	if err := db.SetConfigValue(ctx, store.KindSetting, Agent, "agt1",
		"agent.model", store.StringValue("mirror-model")); err != nil {
		t.Fatalf("seed mirror row: %v", err)
	}
	got, err := SettingAt(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("SettingAt: %v", err)
	}
	if got["model"] != "mirror-model" {
		t.Fatalf("SettingAt = %#v, want model=mirror-model", got)
	}
	// The merged resolver reads the same layer through the same rule, so the
	// two must not be able to disagree about a single-layer row.
	merged, err := Setting(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	if !reflect.DeepEqual(got, merged) {
		t.Fatalf("SettingAt = %#v but Setting = %#v", got, merged)
	}
}

// A disabled row is this layer's decision that the namespace has no value: it
// resolves to nothing and it keeps the mirror out, rather than letting a
// projection resurrect what the row switched off.
func TestSettingAtHonoursTheDisabledVeto(t *testing.T) {
	db := openScopeDBNamed(t, "readmodel_veto")
	defer db.Close()
	ctx := context.Background()

	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindSetting, AgentID: "agt1", Name: "agents.defaults",
		Enabled: false, Data: map[string]interface{}{"model": "from-blob"},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := db.SetConfigValue(ctx, store.KindSetting, Agent, "agt1",
		"agent.model", store.StringValue("from-mirror")); err != nil {
		t.Fatalf("seed mirror row: %v", err)
	}
	got, err := SettingAt(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("SettingAt: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("disabled row resolved to %#v, want nothing", got)
	}
}

// The result is a copy: the CLI patches one key and saves the merged map, and
// that must not be the map the store handed out.
func TestSettingAtResultIsACopy(t *testing.T) {
	db := openScopeDBNamed(t, "readmodel_copy")
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "agt1", "agents.defaults",
		map[string]interface{}{"model": "original"}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	first, err := SettingAt(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("SettingAt: %v", err)
	}
	first["model"] = "mutated-by-the-caller"
	second, err := SettingAt(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("SettingAt: %v", err)
	}
	if second["model"] != "original" {
		t.Fatalf("caller mutation leaked into the store: %#v", second)
	}
}

// Existence and enabled-ness are the only thing a row-listing reader wants, so
// a disabled row must not show up as a namespace the scope "has".
func TestSettingNamesAtSkipsDisabledRows(t *testing.T) {
	db := openScopeDBNamed(t, "readmodel_names")
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "agt1", "prefs",
		map[string]interface{}{"timezone": "UTC"}); err != nil {
		t.Fatalf("SaveSetting prefs: %v", err)
	}
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindSetting, AgentID: "agt1", Name: "sandbox",
		Enabled: false, Data: map[string]interface{}{"enabled": true},
	}); err != nil {
		t.Fatalf("SaveConfig disabled: %v", err)
	}
	got, err := SettingNamesAt(ctx, db, "", "agt1")
	if err != nil {
		t.Fatalf("SettingNamesAt: %v", err)
	}
	if _, ok := got["sandbox"]; ok {
		t.Fatalf("disabled row listed as present: %#v", got)
	}
	if got["prefs"]["timezone"] != "UTC" {
		t.Fatalf("SettingNamesAt = %#v, want prefs.timezone=UTC", got)
	}
}

// The single-scope provider reader has to agree with the resolvers it replaces:
// ProvidersAt with one layer filled is what AgentScopeProviders /
// UserScopeProviders now are, and with no layer filled it is the system view.
func TestProvidersAtMatchesTheScopeReaders(t *testing.T) {
	db := openScopeDBNamed(t, "readmodel_providers")
	defer db.Close()
	ctx := context.Background()

	if err := SaveProvider(ctx, db, "", "", "system-prov",
		config.ProviderConfig{APIKey: "sk-system"}); err != nil {
		t.Fatalf("SaveProvider system: %v", err)
	}
	if err := SaveProvider(ctx, db, "", "agt1", "agent-prov",
		config.ProviderConfig{APIKey: "sk-agent"}); err != nil {
		t.Fatalf("SaveProvider agent: %v", err)
	}
	// A disabled provider row: gone from the merged/resolved views, but its
	// payload is still on disk, which is what the present/enabled split keeps
	// reachable for the read-modify-write callers.
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindProvider, AgentID: "agt1", Name: "off-prov",
		Enabled: false, Data: map[string]interface{}{"apiKey": "sk-off", "apiBase": "https://off.example"},
	}); err != nil {
		t.Fatalf("SaveConfig disabled provider: %v", err)
	}

	systemOnly, err := ProvidersAt(ctx, db, "", "")
	if err != nil {
		t.Fatalf("ProvidersAt(system): %v", err)
	}
	if _, ok := systemOnly["agent-prov"]; ok {
		t.Fatalf("system scope leaked the agent's provider: %#v", systemOnly)
	}
	if _, ok := systemOnly["off-prov"]; ok {
		t.Fatalf("disabled provider listed as present: %#v", systemOnly)
	}
	if systemOnly["system-prov"].APIKey != "sk-system" {
		t.Fatalf("ProvidersAt(system) = %#v", systemOnly)
	}

	agentOnly, err := ProvidersAt(ctx, db, "", "agt1")
	if err != nil {
		t.Fatalf("ProvidersAt(agent): %v", err)
	}
	wrapper, err := AgentScopeProviders(ctx, db, "agt1")
	if err != nil {
		t.Fatalf("AgentScopeProviders: %v", err)
	}
	if !reflect.DeepEqual(agentOnly, wrapper) {
		t.Fatalf("ProvidersAt = %#v but AgentScopeProviders = %#v", agentOnly, wrapper)
	}
	if _, ok := agentOnly["off-prov"]; ok {
		t.Fatalf("disabled provider leaked into ProvidersAt: %#v", agentOnly)
	}

	// ProviderStateAt is the same read, addressed by name. An enabled row is
	// present and enabled.
	pc, present, enabled, err := ProviderStateAt(ctx, db, "agent-prov", "", "agt1")
	if err != nil || !present || !enabled || pc.APIKey != "sk-agent" {
		t.Fatalf("ProviderStateAt = %#v present=%v enabled=%v err=%v", pc, present, enabled, err)
	}
	// A disabled row is present (its payload is still here) but not enabled.
	offPC, offPresent, offEnabled, err := ProviderStateAt(ctx, db, "off-prov", "", "agt1")
	if err != nil || !offPresent || offEnabled || offPC.APIKey != "sk-off" {
		t.Fatalf("ProviderStateAt(disabled) = %#v present=%v enabled=%v err=%v", offPC, offPresent, offEnabled, err)
	}
	// A name this scope does not have at all is neither present nor enabled.
	if _, present, enabled, err := ProviderStateAt(ctx, db, "agent-prov", "", ""); err != nil || present || enabled {
		t.Fatalf("ProviderStateAt at the wrong scope: present=%v enabled=%v err=%v", present, enabled, err)
	}
}
