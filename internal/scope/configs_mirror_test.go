package scope

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// mirrorCertified asserts that the row named name has a completeness marker
// which verifies against what is actually stored: the leaves under prefix and
// the enabled decision on the paired configs row. Reading the decision back out
// of the row (rather than passing it in) is the point — the marker is only
// worth something if it agrees with the row it claims to describe.
func mirrorCertified(t *testing.T, db *store.DBStore, kind, sc, sid, name, prefix string) {
	t.Helper()
	ctx := context.Background()
	m, ok, err := db.GetConfigMirror(ctx, kind, sc, sid, name)
	if err != nil {
		t.Fatalf("GetConfigMirror(%s/%s/%s/%s): %v", kind, sc, sid, name, err)
	}
	if !ok {
		t.Fatalf("no mirror marker for %s/%s/%s/%s", kind, sc, sid, name)
	}
	leaves, err := db.ListConfigValues(ctx, kind, sc, sid, prefix)
	if err != nil {
		t.Fatalf("ListConfigValues(%s): %v", prefix, err)
	}
	if len(leaves) == 0 {
		t.Fatalf("marker %s/%s/%s covers an empty projection", kind, sc, sid)
	}
	uid, aid := OwnershipFromScope(sc, sid)
	rec, err := db.GetConfigByName(ctx, kind, uid, aid, name)
	if err != nil || rec == nil {
		t.Fatalf("GetConfigByName(%s/%s/%s/%s): rec=%+v err=%v", kind, uid, aid, name, rec, err)
	}
	if !store.VerifyConfigMirror(m, rec.Enabled, leaves) {
		t.Fatalf("marker for %s/%s/%s/%s does not verify its leaves: %+v",
			kind, sc, sid, name, m)
	}
}

// Every dual-write entry point leaves a marker that certifies exactly the rows
// it wrote; rewriting updates it and clearing removes it.
func TestDualWriteRecordsConfigMirrorMarker(t *testing.T) {
	db := openScopeDBNamed(t, "mirror_dual")
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "u1", "", "prefs",
		map[string]interface{}{"timezone": "Asia/Shanghai"}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	mirrorCertified(t, db, store.KindSetting, User, "u1", "prefs", "prefs.")

	if err := SaveProvider(ctx, db, "u1", "", "openai",
		config.ProviderConfig{APIKey: "sk-1"}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}
	mirrorCertified(t, db, store.KindProvider, User, "u1", "openai", "openai.")

	if err := SaveAgentPluginEnabled(ctx, db, "a1", map[string]bool{"demo": true}); err != nil {
		t.Fatalf("SaveAgentPluginEnabled: %v", err)
	}
	mirrorCertified(t, db, store.KindPluginEnabled, Agent, "a1",
		PluginEnabledNamespace, PluginEnabledNamespace+".")

	// Rewriting a namespace updates the marker rather than leaving a stale one.
	if err := SaveSetting(ctx, db, "u1", "", "prefs",
		map[string]interface{}{"timezone": "UTC"}); err != nil {
		t.Fatalf("SaveSetting rewrite: %v", err)
	}
	mirrorCertified(t, db, store.KindSetting, User, "u1", "prefs", "prefs.")

	// Emptying a namespace clears its mirror and its marker together.
	if err := SaveSetting(ctx, db, "u1", "", "prefs", nil); err != nil {
		t.Fatalf("SaveSetting clear: %v", err)
	}
	if _, ok, _ := db.GetConfigMirror(ctx, store.KindSetting, User, "u1", "prefs"); ok {
		t.Fatal("marker survived an emptied namespace")
	}

	// Deleting a provider clears its marker too.
	DualDeleteProviderKV(ctx, db, "u1", "", "openai")
	if _, ok, _ := db.GetConfigMirror(ctx, store.KindProvider, User, "u1", "openai"); ok {
		t.Fatal("marker survived DualDeleteProviderKV")
	}
}

// A mirror row written without the dual-write — a historical row, a hand edit —
// has no marker, so it reads as uncertified rather than silently trusted.
func TestMirrorRowsWithoutMarkerAreUncertified(t *testing.T) {
	db := openScopeDBNamed(t, "mirror_uncertified")
	defer db.Close()
	ctx := context.Background()

	if err := db.SetConfigValue(ctx, store.KindSetting, User, "u1",
		"sandbox.timeout", store.StringValue("5")); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	if _, ok, err := db.GetConfigMirror(ctx, store.KindSetting, User, "u1", "sandbox"); err != nil || ok {
		t.Fatalf("hand-written mirror reported a marker: ok=%v err=%v", ok, err)
	}
}

// The marker also catches a projection that changed underneath it — a manual
// single-leaf delete, say — which is the second thing it is for.
func TestMarkerDetectsMirrorTampering(t *testing.T) {
	db := openScopeDBNamed(t, "mirror_tamper")
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "u1", "", "prefs",
		map[string]interface{}{"timezone": "UTC", "locale": "zh"}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	if err := db.DeleteConfigValue(ctx, store.KindSetting, User, "u1", "prefs.locale"); err != nil {
		t.Fatalf("DeleteConfigValue: %v", err)
	}
	m, ok, err := db.GetConfigMirror(ctx, store.KindSetting, User, "u1", "prefs")
	if err != nil || !ok {
		t.Fatalf("GetConfigMirror: ok=%v err=%v", ok, err)
	}
	leaves, err := db.ListConfigValues(ctx, store.KindSetting, User, "u1", "prefs.")
	if err != nil {
		t.Fatalf("ListConfigValues: %v", err)
	}
	if store.VerifyConfigMirror(m, true, leaves) {
		t.Fatal("marker verified a projection a manual delete had truncated")
	}
}

// A row's enabled decision is half of what the mirror has to be able to answer
// once it is the read source (a disabled row erases outer layers and blocks the
// fallback), so the marker records it — including for a row whose projection is
// empty, which is exactly the shape a disabled row takes when it has no payload.
func TestMarkerRecordsEnabledDecision(t *testing.T) {
	db := openScopeDBNamed(t, "mirror_enabled")
	defer db.Close()
	ctx := context.Background()

	if err := SaveProviderState(ctx, db, "u1", "", "openai",
		config.ProviderConfig{APIKey: "sk-1"}, false); err != nil {
		t.Fatalf("SaveProviderState(disabled): %v", err)
	}
	m, ok, err := db.GetConfigMirror(ctx, store.KindProvider, User, "u1", "openai")
	if err != nil || !ok {
		t.Fatalf("GetConfigMirror: ok=%v err=%v", ok, err)
	}
	if m.Enabled == nil || *m.Enabled {
		t.Fatalf("marker enabled = %v, want a recorded false", m.Enabled)
	}
	// The leaves are written either way: disabling is a row decision, not a
	// payload change, so the marker must still cover the full projection.
	leaves, err := db.ListConfigValues(ctx, store.KindProvider, User, "u1", "openai.")
	if err != nil || len(leaves) == 0 {
		t.Fatalf("disabled provider lost its leaves: %d err=%v", len(leaves), err)
	}
	if !store.VerifyConfigMirror(m, false, leaves) {
		t.Fatalf("disabled marker does not verify: %+v", m)
	}
	if store.VerifyConfigMirror(m, true, leaves) {
		t.Fatal("disabled marker verified an enabled row")
	}
	// And the reader honours it: a disabled provider is not served.
	if provs, err := UserScopeProviders(ctx, db, "u1"); err != nil || len(provs) != 0 {
		t.Fatalf("UserScopeProviders(disabled) = %+v err=%v", provs, err)
	}
}

// The certification pass has to certify every row it examines — an empty
// projection is a legal, certifiable row (a disabled one), not a case that
// quietly goes unmarked and leaves "gap zero" satisfiable with rows the mirror
// could not serve.
func TestReconcileCertifiesEmptyProjections(t *testing.T) {
	db := openScopeDBNamed(t, "mirror_empty_reconcile")
	defer db.Close()
	ctx := context.Background()

	// A disabled row with no payload: no leaves to project, but a decision.
	if err := db.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindSetting, UserID: "u1", Name: "quiet", Enabled: false,
		Data: map[string]interface{}{},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	// A normal row alongside it, so the pass is exercised on both shapes.
	if err := SaveSetting(ctx, db, "u1", "", "prefs",
		map[string]interface{}{"timezone": "UTC"}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	rep, err := db.ReconcileConfigMirrors(ctx, false)
	if err != nil {
		t.Fatalf("ReconcileConfigMirrors: %v", err)
	}
	if len(rep.Gaps) != 0 || rep.Certified != rep.Examined || rep.Examined != 2 {
		t.Fatalf("reconcile = %+v, want 2/2 certified and no gaps", rep)
	}
	m, ok, err := db.GetConfigMirror(ctx, store.KindSetting, User, "u1", "quiet")
	if err != nil || !ok {
		t.Fatalf("empty projection was not certified: ok=%v err=%v", ok, err)
	}
	if m.KeyCount != 0 || m.Enabled == nil || *m.Enabled {
		t.Fatalf("empty marker = %+v, want 0 keys and a recorded false", m)
	}
	if !store.VerifyConfigMirror(m, false, map[string]store.ConfigValue{}) {
		t.Fatalf("empty marker does not verify: %+v", m)
	}
}
