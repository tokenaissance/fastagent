package scope

// These tests cover the read-authority lever and the per-row certification
// guard that make the configs -> configs_kv flip a one-name change instead of a
// rewrite of every reader. The lever defaults to configsKVFirst — the dev
// reconcile gate went green (165/165 certified, 0 gap) — and that must
// (a) let a certified mirror win, and (b) never serve a mirror the marker does
// not certify, which is the web_search shape. The guard is what carries the
// weight: the default only decides which order a *certified* row is answered in.
//
// There is deliberately no per-environment switch. The flip is safe because it
// is *per row*: a row the marker does not certify falls back to the blob in
// every process, so a database with no markers at all (a fresh prod) reads
// exactly as it does today and is converted row by row by reconcile-mirror.
// Two defaults would put dev and prod on two read paths — the divergence class
// this mechanism exists to catch.

import (
	"context"
	"reflect"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// withReadAuthority flips the package lever for one test and restores it, so a
// failed assertion cannot leak configsKVFirst into the rest of the suite.
func withReadAuthority(t *testing.T, a readAuthority) {
	t.Helper()
	prev := configsReadAuthority
	configsReadAuthority = a
	t.Cleanup(func() { configsReadAuthority = prev })
}

// The default is configsKVFirst, uniformly: one read model for every
// environment. This test exists so the flip stays a deliberate, reviewed act —
// if the default ever moves again, it moves here in the same commit as the gate
// evidence, not by accident — and to fail loudly if someone later reintroduces
// an environment-dependent default.
//
// The suite must not, however, depend on the default for correctness: the
// certification guard is tested at both orders below, and the blob still
// answers every row the marker does not certify (TestSettingAtConfigsKVFirst-
// FallsBackOnUncertifiedRow and friends pass under either value).
func TestReadAuthorityDefaultsToConfigsKVFirst(t *testing.T) {
	if configsReadAuthority != configsKVFirst {
		t.Fatalf("configsReadAuthority = %v, want configsKVFirst — one read model for every environment (roll back to blobFirst only with the reconcile gate evidence in hand)", configsReadAuthority)
	}
}

// A certified mirror answers the read under configsKVFirst, and the blob's
// value is what answers it under blobFirst — the same row, two orders.
func TestSettingAtConfigsKVFirstTrustsCertifiedRowsOverBlob(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_settling")
	defer db.Close()
	ctx := context.Background()

	// One dual-write: blob + mirror + a marker that certifies the mirror.
	if err := SaveSetting(ctx, db, "", "agt1", "agents.defaults",
		map[string]interface{}{"model": "mirror-model"}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	// Change the blob alone. The mirror and its marker are untouched, so the
	// marker still certifies what is on disk in configs_kv.
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindSetting, AgentID: "agt1", Name: "agents.defaults",
		Enabled: true, Data: map[string]interface{}{"model": "blob-model"},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	withReadAuthority(t, blobFirst)
	blob, err := SettingAt(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("SettingAt(blobFirst): %v", err)
	}
	if blob["model"] != "blob-model" {
		t.Fatalf("blobFirst model = %v, want blob-model", blob["model"])
	}

	withReadAuthority(t, configsKVFirst)
	mirror, err := SettingAt(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("SettingAt(configsKVFirst): %v", err)
	}
	if mirror["model"] != "mirror-model" {
		t.Fatalf("configsKVFirst model = %v, want mirror-model", mirror["model"])
	}
}

// A row with no marker at all (a hand-written configs_kv row) is not certified.
// configsKVFirst must fall back to the blob rather than serve it — an unmarked
// subset is indistinguishable from the whole, which is the failure mode here.
func TestSettingAtConfigsKVFirstFallsBackOnUncertifiedRow(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_uncertified")
	defer db.Close()
	ctx := context.Background()

	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindSetting, AgentID: "agt1", Name: "agents.defaults",
		Enabled: true, Data: map[string]interface{}{"model": "blob-model"},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	// Mirror rows written straight into configs_kv, with no marker.
	if err := db.SetConfigValue(ctx, store.KindSetting, Agent, "agt1",
		"agent.model", store.StringValue("mirror-model")); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	withReadAuthority(t, configsKVFirst)
	got, err := SettingAt(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("SettingAt: %v", err)
	}
	if got["model"] != "blob-model" {
		t.Fatalf("configsKVFirst model = %v, want blob-model (uncertified mirror must not be served)", got["model"])
	}
}

// The guard's real job: a mirror that was complete when the marker was
// written, then lost a leaf. The marker no longer covers the leaves, so
// configsKVFirst falls back to the blob and the dropped key comes back — this is
// the web_search regression, pinned.
func TestSettingAtConfigsKVFirstFallsBackWhenMarkerNoLongerCoversTheLeaves(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_drift")
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "agt1", "agents.defaults",
		map[string]interface{}{"model": "m", "note": "n"}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	// Drop one mirror leaf behind the layer's back: the marker still says two
	// keys, the mirror now has one.
	if err := db.DeleteConfigValue(ctx, store.KindSetting, Agent, "agt1", "agent.note"); err != nil {
		t.Fatalf("DeleteConfigValue: %v", err)
	}

	withReadAuthority(t, configsKVFirst)
	got, err := SettingAt(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("SettingAt: %v", err)
	}
	if got["model"] != "m" || got["note"] != "n" {
		t.Fatalf("configsKVFirst = %#v, want the full blob row {model:m note:n} — a truncated mirror must not be served", got)
	}
}

// A certified marker records the row's on/off decision, so configsKVFirst can
// answer the veto without the blob. With the blob row removed, blobFirst falls
// back to the mirror and sees only the payload (it cannot know the row is off),
// while configsKVFirst reads the decision out of the marker.
func TestProvidersAtConfigsKVFirstHonoursTheMarkerVeto(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_provider_veto")
	defer db.Close()
	ctx := context.Background()

	if err := SaveProviderState(ctx, db, "u1", "", "openai",
		config.ProviderConfig{APIKey: "sk-mir"}, false); err != nil {
		t.Fatalf("SaveProviderState: %v", err)
	}
	// Remove the blob row so the fallback can only be the mirror.
	rec, err := db.GetConfigByName(ctx, store.KindProvider, "u1", "", "openai")
	if err != nil || rec == nil {
		t.Fatalf("GetConfigByName: rec=%+v err=%v", rec, err)
	}
	if err := db.DeleteConfig(ctx, rec.ID); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}

	withReadAuthority(t, blobFirst)
	blob, err := ProvidersAt(ctx, db, "u1", "")
	if err != nil {
		t.Fatalf("ProvidersAt(blobFirst): %v", err)
	}
	if _, ok := blob["openai"]; !ok {
		t.Fatalf("blobFirst lost openai: %#v — the fixture needs the blob gone but the mirror present", blob)
	}

	withReadAuthority(t, configsKVFirst)
	mirror, err := ProvidersAt(ctx, db, "u1", "")
	if err != nil {
		t.Fatalf("ProvidersAt(configsKVFirst): %v", err)
	}
	if _, ok := mirror["openai"]; ok {
		t.Fatalf("configsKVFirst served a disabled provider: %#v", mirror)
	}
}

// ProviderStateAt's two halves under configsKVFirst: the payload survives the
// veto (a read-modify-write must not reset a disabled provider to preset
// defaults), and enabled reports the marker's decision.
func TestProviderStateAtConfigsKVFirstReportsPayloadAndVeto(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_provider_state")
	defer db.Close()
	ctx := context.Background()

	if err := SaveProviderState(ctx, db, "u1", "", "openai",
		config.ProviderConfig{APIKey: "sk-mir"}, false); err != nil {
		t.Fatalf("SaveProviderState: %v", err)
	}

	withReadAuthority(t, configsKVFirst)
	pc, present, enabled, err := ProviderStateAt(ctx, db, "openai", "u1", "")
	if err != nil {
		t.Fatalf("ProviderStateAt: %v", err)
	}
	if !present || enabled {
		t.Fatalf("present=%v enabled=%v, want present=true enabled=false", present, enabled)
	}
	if pc.APIKey != "sk-mir" {
		t.Fatalf("payload apiKey = %q, want sk-mir (payload must survive the veto)", pc.APIKey)
	}
}

// The plugin-enabled row is the third mirrored kind and follows the same rule:
// a certified mirror answers it under configsKVFirst.
func TestAgentPluginEnabledConfigsKVFirstServesCertifiedRows(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_plugin")
	defer db.Close()
	ctx := context.Background()

	if err := SaveAgentPluginEnabled(ctx, db, "agt1", map[string]bool{"demo": true}); err != nil {
		t.Fatalf("SaveAgentPluginEnabled: %v", err)
	}

	withReadAuthority(t, configsKVFirst)
	got, err := AgentPluginEnabled(ctx, db, "agt1")
	if err != nil {
		t.Fatalf("AgentPluginEnabled: %v", err)
	}
	if !got["demo"] {
		t.Fatalf("AgentPluginEnabled = %#v, want demo=true", got)
	}
}

// The merged resolver walks four layers; under configsKVFirst each layer is judged
// on its own marker, so a certified layer overrides its blob row while the walk
// still merges across layers.
func TestSettingConfigsKVFirstCertifiesEachLayer(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_merged_setting")
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "agents.defaults",
		map[string]interface{}{"model": "mirror-sys"}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	// Change the blob alone; the marker still certifies the mirror leaves.
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindSetting, Name: "agents.defaults",
		Enabled: true, Data: map[string]interface{}{"model": "blob-sys"},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	withReadAuthority(t, blobFirst)
	blob, err := Setting(ctx, db, "agents.defaults", "", "")
	if err != nil {
		t.Fatalf("Setting(blobFirst): %v", err)
	}
	if blob["model"] != "blob-sys" {
		t.Fatalf("blobFirst model = %v, want blob-sys", blob["model"])
	}

	withReadAuthority(t, configsKVFirst)
	mirror, err := Setting(ctx, db, "agents.defaults", "", "")
	if err != nil {
		t.Fatalf("Setting(configsKVFirst): %v", err)
	}
	if mirror["model"] != "mirror-sys" {
		t.Fatalf("configsKVFirst model = %v, want mirror-sys", mirror["model"])
	}
}

// The merged walk still merges outer and inner certified layers under
// configsKVFirst — the flip changes which table each layer is read from, not the
// precedence between layers.
func TestSettingConfigsKVFirstMergesAcrossLayers(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_merged_layers")
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "agents.defaults",
		map[string]interface{}{"model": "sys", "promptMode": "natural"}); err != nil {
		t.Fatalf("SaveSetting system: %v", err)
	}
	if err := SaveSetting(ctx, db, "u1", "", "agents.defaults",
		map[string]interface{}{"model": "user"}); err != nil {
		t.Fatalf("SaveSetting user: %v", err)
	}

	for _, auth := range []readAuthority{blobFirst, configsKVFirst} {
		withReadAuthority(t, auth)
		got, err := Setting(ctx, db, "agents.defaults", "u1", "")
		if err != nil {
			t.Fatalf("Setting(%v): %v", auth, err)
		}
		if got["model"] != "user" || got["promptMode"] != "natural" {
			t.Fatalf("Setting(%v) = %#v, want model=user promptMode=natural", auth, got)
		}
	}
}

// An uncertified layer row (mirror leaves with no marker) is not trusted:
// configsKVFirst falls back to the blob for that layer, exactly as the single
// scope resolvers do.
func TestSettingConfigsKVFirstFallsBackPerLayerOnUncertified(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_merged_uncertified")
	defer db.Close()
	ctx := context.Background()

	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindSetting, Name: "agents.defaults",
		Enabled: true, Data: map[string]interface{}{"model": "blob-sys"},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := db.SetConfigValue(ctx, store.KindSetting, System, "",
		"agent.model", store.StringValue("mirror-sys")); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	withReadAuthority(t, configsKVFirst)
	got, err := Setting(ctx, db, "agents.defaults", "", "")
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	if got["model"] != "blob-sys" {
		t.Fatalf("configsKVFirst model = %v, want blob-sys (uncertified layer must not be served)", got["model"])
	}
}

// Providers is the merged provider resolver; a certified layer overrides its
// blob row under configsKVFirst, and the blob answers under blobFirst.
func TestProvidersConfigsKVFirstCertifiedOverridesBlob(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_merged_providers")
	defer db.Close()
	ctx := context.Background()

	if err := SaveProvider(ctx, db, "", "", "openai",
		config.ProviderConfig{APIKey: "sk-mir"}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindProvider, Name: "openai",
		Enabled: true, Data: map[string]interface{}{"apiKey": "sk-blob"},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	withReadAuthority(t, blobFirst)
	blob, err := Providers(ctx, db, "", "")
	if err != nil {
		t.Fatalf("Providers(blobFirst): %v", err)
	}
	if blob["openai"].APIKey != "sk-blob" {
		t.Fatalf("blobFirst apiKey = %q, want sk-blob", blob["openai"].APIKey)
	}

	withReadAuthority(t, configsKVFirst)
	mirror, err := Providers(ctx, db, "", "")
	if err != nil {
		t.Fatalf("Providers(configsKVFirst): %v", err)
	}
	if mirror["openai"].APIKey != "sk-mir" {
		t.Fatalf("configsKVFirst apiKey = %q, want sk-mir", mirror["openai"].APIKey)
	}
}

// Providers honours a marker's veto under configsKVFirst: with the blob row gone,
// blobFirst can only see the payload, while configsKVFirst reads the "off"
// decision out of the marker and drops the name.
func TestProvidersConfigsKVFirstHonoursTheMarkerVeto(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_merged_provider_veto")
	defer db.Close()
	ctx := context.Background()

	if err := SaveProviderState(ctx, db, "", "", "openai",
		config.ProviderConfig{APIKey: "sk-mir"}, false); err != nil {
		t.Fatalf("SaveProviderState: %v", err)
	}
	rec, err := db.GetConfigByName(ctx, store.KindProvider, "", "", "openai")
	if err != nil || rec == nil {
		t.Fatalf("GetConfigByName: rec=%+v err=%v", rec, err)
	}
	if err := db.DeleteConfig(ctx, rec.ID); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}

	withReadAuthority(t, configsKVFirst)
	got, err := Providers(ctx, db, "", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if _, ok := got["openai"]; ok {
		t.Fatalf("configsKVFirst served a disabled provider: %#v", got)
	}
}

// BatchSettings must not be able to disagree with Setting in either mode — the
// batch form exists only to replace the per-namespace queries. The fixture
// covers every branch of the batched resolver: a certified row (mirror wins), a
// blob-only uncertified row (blob answers), a disabled row (veto), and a row
// that exists only in the raw mirror (last-resort fallback).
func TestBatchSettingsMatchesSettingUnderConfigsKVFirst(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_merged_batch")
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "u1", "", "agents.defaults",
		map[string]interface{}{"model": "certified"}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindSetting, UserID: "u1", Name: "tools.categories",
		Enabled: true, Data: map[string]interface{}{"search": true},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindSetting, UserID: "u1", Name: "sandbox",
		Enabled: false, Data: map[string]interface{}{"enabled": true},
	}); err != nil {
		t.Fatalf("SaveConfig disabled: %v", err)
	}
	if err := db.SetConfigValue(ctx, store.KindSetting, User, "u1",
		"tools.providers.searxng.endpoint", store.StringValue("http://searx")); err != nil {
		t.Fatalf("SetConfigValue raw-mirror row: %v", err)
	}
	namespaces := []string{"agents.defaults", "tools.categories", "sandbox", "tools.providers"}

	for _, auth := range []readAuthority{blobFirst, configsKVFirst} {
		withReadAuthority(t, auth)
		batch, err := BatchSettings(ctx, db, namespaces, "u1", "")
		if err != nil {
			t.Fatalf("BatchSettings(%v): %v", auth, err)
		}
		for _, ns := range namespaces {
			single, err := Setting(ctx, db, ns, "u1", "")
			if err != nil {
				t.Fatalf("Setting(%v, %s): %v", auth, ns, err)
			}
			// BatchSettings omits an empty namespace; Setting returns an empty
			// map. Normalize before comparing, as the parity test does.
			got := batch[ns]
			if got == nil {
				got = map[string]interface{}{}
			}
			if !reflect.DeepEqual(got, single) {
				t.Fatalf("%v: BatchSettings[%s] = %#v but Setting = %#v", auth, ns, got, single)
			}
		}
	}
}

// The settings veto has to survive the flip: a namespace the inner layer
// switched off must read as "nothing here" in both orders, and the mirror has
// to be able to answer that on its own — otherwise the flip would resurrect a
// vetoed namespace from a stale certified marker. SaveSettingState is the
// writer that makes the veto representable (the readers always had the rule;
// nothing could record it), so this test drives the writer, not the internals.
func TestSaveSettingStateVetoesTheSameWayInBothOrders(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_setting_veto")
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "agents.defaults",
		map[string]interface{}{"model": "sys-model"}); err != nil {
		t.Fatalf("seed system layer: %v", err)
	}
	if err := SaveSetting(ctx, db, "u1", "", "agents.defaults",
		map[string]interface{}{"model": "user-model"}); err != nil {
		t.Fatalf("seed user layer: %v", err)
	}
	// The inner layer switches the namespace off. Payload erased, decision
	// recorded — the shape a disabled provider already has.
	if err := SaveSettingState(ctx, db, "u1", "", "agents.defaults", nil, false); err != nil {
		t.Fatalf("SaveSettingState(veto): %v", err)
	}

	for _, auth := range []readAuthority{blobFirst, configsKVFirst} {
		withReadAuthority(t, auth)
		got, err := Setting(ctx, db, "agents.defaults", "u1", "")
		if err != nil {
			t.Fatalf("Setting(%v): %v", auth, err)
		}
		if len(got) != 0 {
			t.Fatalf("%v: vetoed namespace came back as %#v; the mirror's stale enabled=true won", auth, got)
		}
	}

	// The marker carries the decision, so the mirror alone can answer the
	// veto — that is what configsKVFirst relies on.
	m, ok, err := db.GetConfigMirror(ctx, store.KindSetting, User, "u1", "agents.defaults")
	if err != nil || !ok {
		t.Fatalf("GetConfigMirror = (%+v, %v, %v), want a marker", m, ok, err)
	}
	if m.Enabled == nil || *m.Enabled {
		t.Fatalf("marker enabled = %v, want an explicit false", m.Enabled)
	}
	if m.KeyCount != 0 {
		t.Fatalf("marker key_count = %d, want 0 (the payload is gone)", m.KeyCount)
	}

	// And reconcile agrees with the dual-write: the vetoed row is a complete
	// mirror of the blob (both empty), certified, and not a gap. The old
	// dual-write deleted the marker here instead, so the row was uncertified
	// while reconcile kept re-certifying it — two writers, two answers.
	rep, err := db.ReconcileConfigMirrors(ctx, false)
	if err != nil {
		t.Fatalf("ReconcileConfigMirrors: %v", err)
	}
	if len(rep.Gaps) != 0 {
		t.Fatalf("reconcile reported %d gap(s): %+v", len(rep.Gaps), rep.Gaps)
	}
	if rep.Certified != rep.Examined {
		t.Fatalf("certified %d of %d examined; the vetoed row cannot be served by the mirror", rep.Certified, rep.Examined)
	}
}

// An empty object is a value, not an absence, and the flip must not lose it.
// `{"config":{}}` flattening to no rows would make a mirror-first read answer
// nothing for a key the blob holds — the plugin-config case, where the field
// is `{}` and the reader has to hand back an empty object rather than a nil.
func TestEmptyObjectSurvivesTheMirrorUnderBothOrders(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_empty_object")
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "plugins", map[string]interface{}{
		"entries": map[string]interface{}{
			"empty-hook": map[string]interface{}{"config": map[string]interface{}{}},
		},
	}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	// On disk the key is one object-valued leaf, not a missing row.
	v, err := db.GetConfigValue(ctx, store.KindSetting, System, "", "plugins.entries.empty-hook.config")
	if err != nil {
		t.Fatalf("the empty-object key has no row: %v", err)
	}
	if v.Kind != store.ValueKindObject || v.Value != "{}" {
		t.Fatalf("empty-object row = %+v, want the tagged literal {}", v)
	}

	for _, auth := range []readAuthority{blobFirst, configsKVFirst} {
		withReadAuthority(t, auth)
		got, err := Setting(ctx, db, "plugins", "", "")
		if err != nil {
			t.Fatalf("Setting(%v): %v", auth, err)
		}
		entries, _ := got["entries"].(map[string]interface{})
		hook, _ := entries["empty-hook"].(map[string]interface{})
		cfg, ok := hook["config"].(map[string]interface{})
		if !ok || cfg == nil || len(cfg) != 0 {
			t.Fatalf("%v: config = %#v, want an empty object (absent and {} are different values)", auth, hook["config"])
		}
	}
}
