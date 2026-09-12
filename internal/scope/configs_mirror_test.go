package scope

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// mirrorCertified asserts that the row named name has a completeness marker and
// that the marker verifies against the leaves actually stored under prefix.
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
	if !store.VerifyConfigMirror(m, leaves) {
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
	if store.VerifyConfigMirror(m, leaves) {
		t.Fatal("marker verified a projection a manual delete had truncated")
	}
}
