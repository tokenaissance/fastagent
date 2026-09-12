package scope

// These tests cover the read-authority lever and the per-row certification
// guard that make the configs -> configs_kv flip a one-name change instead of a
// rewrite of every reader. The migration stays on the blob until the reconcile
// gate is green, so the lever defaults to blobFirst; turning it to mirrorFirst
// must (a) let a certified projection win, and (b) never serve a projection the
// marker does not certify — the web_search shape.

import (
	"context"
	"reflect"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// withReadAuthority flips the package lever for one test and restores it, so a
// failed assertion cannot leak mirrorFirst into the rest of the suite.
func withReadAuthority(t *testing.T, a readAuthority) {
	t.Helper()
	prev := configsReadAuthority
	configsReadAuthority = a
	t.Cleanup(func() { configsReadAuthority = prev })
}

// The default must stay blobFirst: the flip is gated on
// `reconcile-mirror --strict`, and a build that silently shipped mirrorFirst
// would be the outage this whole mechanism exists to prevent.
func TestReadAuthorityDefaultsToBlobFirst(t *testing.T) {
	if configsReadAuthority != blobFirst {
		t.Fatalf("configsReadAuthority = %v, want blobFirst (the flip must wait for the reconcile gate)", configsReadAuthority)
	}
}

// A certified projection answers the read under mirrorFirst, and the blob's
// value is what answers it under blobFirst — the same row, two orders.
func TestSettingAtMirrorFirstTrustsCertifiedMirrorOverBlob(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_settling")
	defer db.Close()
	ctx := context.Background()

	// One dual-write: blob + mirror + a marker that certifies the projection.
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

	withReadAuthority(t, mirrorFirst)
	mirror, err := SettingAt(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("SettingAt(mirrorFirst): %v", err)
	}
	if mirror["model"] != "mirror-model" {
		t.Fatalf("mirrorFirst model = %v, want mirror-model", mirror["model"])
	}
}

// A row with no marker at all (a hand-written configs_kv row) is not certified.
// mirrorFirst must fall back to the blob rather than serve it — an unmarked
// subset is indistinguishable from the whole, which is the failure mode here.
func TestSettingAtMirrorFirstFallsBackOnUncertifiedRow(t *testing.T) {
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

	withReadAuthority(t, mirrorFirst)
	got, err := SettingAt(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("SettingAt: %v", err)
	}
	if got["model"] != "blob-model" {
		t.Fatalf("mirrorFirst model = %v, want blob-model (uncertified mirror must not be served)", got["model"])
	}
}

// The guard's real job: a projection that was complete when the marker was
// written, then lost a leaf. The marker no longer covers the leaves, so
// mirrorFirst falls back to the blob and the dropped key comes back — this is
// the web_search regression, pinned.
func TestSettingAtMirrorFirstFallsBackWhenMarkerNoLongerCoversTheLeaves(t *testing.T) {
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

	withReadAuthority(t, mirrorFirst)
	got, err := SettingAt(ctx, db, "agents.defaults", "", "agt1")
	if err != nil {
		t.Fatalf("SettingAt: %v", err)
	}
	if got["model"] != "m" || got["note"] != "n" {
		t.Fatalf("mirrorFirst = %#v, want the full blob row {model:m note:n} — a truncated mirror must not be served", got)
	}
}

// A certified marker records the row's on/off decision, so mirrorFirst can
// answer the veto without the blob. With the blob row removed, blobFirst falls
// back to the mirror and sees only the payload (it cannot know the row is off),
// while mirrorFirst reads the decision out of the marker.
func TestProvidersAtMirrorFirstHonoursTheMarkerVeto(t *testing.T) {
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

	withReadAuthority(t, mirrorFirst)
	mirror, err := ProvidersAt(ctx, db, "u1", "")
	if err != nil {
		t.Fatalf("ProvidersAt(mirrorFirst): %v", err)
	}
	if _, ok := mirror["openai"]; ok {
		t.Fatalf("mirrorFirst served a disabled provider: %#v", mirror)
	}
}

// ProviderStateAt's two halves under mirrorFirst: the payload survives the
// veto (a read-modify-write must not reset a disabled provider to preset
// defaults), and enabled reports the marker's decision.
func TestProviderStateAtMirrorFirstReportsPayloadAndVeto(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_provider_state")
	defer db.Close()
	ctx := context.Background()

	if err := SaveProviderState(ctx, db, "u1", "", "openai",
		config.ProviderConfig{APIKey: "sk-mir"}, false); err != nil {
		t.Fatalf("SaveProviderState: %v", err)
	}

	withReadAuthority(t, mirrorFirst)
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
// a certified projection answers it under mirrorFirst.
func TestAgentPluginEnabledMirrorFirstServesCertifiedMirror(t *testing.T) {
	db := openScopeDBNamed(t, "readauth_plugin")
	defer db.Close()
	ctx := context.Background()

	if err := SaveAgentPluginEnabled(ctx, db, "agt1", map[string]bool{"demo": true}); err != nil {
		t.Fatalf("SaveAgentPluginEnabled: %v", err)
	}

	withReadAuthority(t, mirrorFirst)
	got, err := AgentPluginEnabled(ctx, db, "agt1")
	if err != nil {
		t.Fatalf("AgentPluginEnabled: %v", err)
	}
	if !got["demo"] {
		t.Fatalf("AgentPluginEnabled = %#v, want demo=true", got)
	}
}

// The merged resolver walks four layers; under mirrorFirst each layer is judged
// on its own marker, so a certified layer overrides its blob row while the walk
// still merges across layers.
func TestSettingMirrorFirstCertifiesEachLayer(t *testing.T) {
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

	withReadAuthority(t, mirrorFirst)
	mirror, err := Setting(ctx, db, "agents.defaults", "", "")
	if err != nil {
		t.Fatalf("Setting(mirrorFirst): %v", err)
	}
	if mirror["model"] != "mirror-sys" {
		t.Fatalf("mirrorFirst model = %v, want mirror-sys", mirror["model"])
	}
}

// The merged walk still merges outer and inner certified layers under
// mirrorFirst — the flip changes which table each layer is read from, not the
// precedence between layers.
func TestSettingMirrorFirstMergesAcrossLayers(t *testing.T) {
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

	for _, auth := range []readAuthority{blobFirst, mirrorFirst} {
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
// mirrorFirst falls back to the blob for that layer, exactly as the single
// scope resolvers do.
func TestSettingMirrorFirstFallsBackPerLayerOnUncertified(t *testing.T) {
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

	withReadAuthority(t, mirrorFirst)
	got, err := Setting(ctx, db, "agents.defaults", "", "")
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	if got["model"] != "blob-sys" {
		t.Fatalf("mirrorFirst model = %v, want blob-sys (uncertified layer must not be served)", got["model"])
	}
}

// Providers is the merged provider resolver; a certified layer overrides its
// blob row under mirrorFirst, and the blob answers under blobFirst.
func TestProvidersMirrorFirstCertifiedOverridesBlob(t *testing.T) {
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

	withReadAuthority(t, mirrorFirst)
	mirror, err := Providers(ctx, db, "", "")
	if err != nil {
		t.Fatalf("Providers(mirrorFirst): %v", err)
	}
	if mirror["openai"].APIKey != "sk-mir" {
		t.Fatalf("mirrorFirst apiKey = %q, want sk-mir", mirror["openai"].APIKey)
	}
}

// Providers honours a marker's veto under mirrorFirst: with the blob row gone,
// blobFirst can only see the payload, while mirrorFirst reads the "off"
// decision out of the marker and drops the name.
func TestProvidersMirrorFirstHonoursTheMarkerVeto(t *testing.T) {
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

	withReadAuthority(t, mirrorFirst)
	got, err := Providers(ctx, db, "", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if _, ok := got["openai"]; ok {
		t.Fatalf("mirrorFirst served a disabled provider: %#v", got)
	}
}

// BatchSettings must not be able to disagree with Setting in either mode — the
// batch form exists only to replace the per-namespace queries. The fixture
// covers every branch of the batched resolver: a certified row (mirror wins), a
// blob-only uncertified row (blob answers), a disabled row (veto), and a row
// that exists only in the raw mirror (last-resort fallback).
func TestBatchSettingsMatchesSettingUnderMirrorFirst(t *testing.T) {
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

	for _, auth := range []readAuthority{blobFirst, mirrorFirst} {
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
