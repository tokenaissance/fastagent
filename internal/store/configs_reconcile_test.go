package store

import (
	"context"
	"testing"
)

// ReconcileConfigMirrors certifies the rows whose mirror matches the blob and
// reports the ones that do not, dropping any marker that would otherwise vouch
// for a wrong projection.
func TestReconcileConfigMirrors(t *testing.T) {
	db, err := NewDBStore("sqlite", "file:reconcile?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Two blob rows. Their mirrors are seeded by hand to look like state left
	// by an older build: one complete, one a subset.
	if err := db.SaveConfig(ctx, &ConfigRecord{Kind: KindProvider, UserID: "u1", Name: "openai",
		Enabled: true, Data: map[string]interface{}{"api_key": "sk-1", "api_base": "https://x"}}); err != nil {
		t.Fatalf("SaveConfig provider: %v", err)
	}
	if err := db.SaveConfig(ctx, &ConfigRecord{Kind: KindSetting, UserID: "u1", Name: "prefs",
		Enabled: true, Data: map[string]interface{}{"timezone": "UTC", "locale": "zh"}}); err != nil {
		t.Fatalf("SaveConfig setting: %v", err)
	}

	// openai: a complete, matching mirror.
	for name, val := range map[string]string{"openai.api_key": "sk-1", "openai.api_base": "https://x"} {
		if err := db.SetConfigValue(ctx, KindProvider, "user", "u1", name, StringValue(val)); err != nil {
			t.Fatalf("seed provider mirror %s: %v", name, err)
		}
	}
	// prefs: a subset (locale missing) plus a stale marker that must not
	// survive — it would certify the wrong projection.
	if err := db.SetConfigValue(ctx, KindSetting, "user", "u1", "prefs.timezone", StringValue("UTC")); err != nil {
		t.Fatalf("seed prefs mirror: %v", err)
	}
	stale := map[string]ConfigValue{"prefs.timezone": StringValue("UTC")}
	if err := db.SaveConfigMirror(ctx, KindSetting, "user", "u1", "prefs", NewConfigMirror("prefs.", stale)); err != nil {
		t.Fatalf("seed stale marker: %v", err)
	}

	rep, err := db.ReconcileConfigMirrors(ctx)
	if err != nil {
		t.Fatalf("ReconcileConfigMirrors: %v", err)
	}
	if rep.Examined != 2 || rep.Certified != 1 || len(rep.Gaps) != 1 {
		t.Fatalf("reconcile = %+v, want examined=2 certified=1 gaps=1", rep)
	}
	g := rep.Gaps[0]
	if g.Kind != KindSetting || g.Name != "prefs" || g.ScopeID != "u1" {
		t.Fatalf("gap identity = %+v", g)
	}
	if len(g.Missing) != 1 || g.Missing[0] != "prefs.locale" {
		t.Fatalf("gap.Missing = %v, want [prefs.locale]", g.Missing)
	}

	// openai is certified and verifies.
	want := map[string]ConfigValue{"openai.api_key": StringValue("sk-1"), "openai.api_base": StringValue("https://x")}
	m, ok, err := db.GetConfigMirror(ctx, KindProvider, "user", "u1", "openai")
	if err != nil || !ok {
		t.Fatalf("openai not certified: ok=%v err=%v", ok, err)
	}
	if !VerifyConfigMirror(m, want) {
		t.Fatalf("certified marker does not verify: %+v", m)
	}
	// prefs' stale marker is gone.
	if _, ok, _ := db.GetConfigMirror(ctx, KindSetting, "user", "u1", "prefs"); ok {
		t.Fatal("stale marker survived a mismatched reconcile")
	}

	// Re-running is idempotent: same verdict, no drift.
	rep2, err := db.ReconcileConfigMirrors(ctx)
	if err != nil {
		t.Fatalf("second ReconcileConfigMirrors: %v", err)
	}
	if rep2.Examined != 2 || rep2.Certified != 1 || len(rep2.Gaps) != 1 {
		t.Fatalf("second reconcile = %+v, want the same verdict", rep2)
	}
}
