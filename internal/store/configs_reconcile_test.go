package store

import (
	"context"
	"testing"
)

// ReconcileConfigMirrors certifies the rows whose mirror matches the blob,
// retags a row that matches on text but predates value_kind, and reports the
// rows that genuinely diverge (dropping any marker that would vouch for one).
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

	seed := func(name string, data map[string]interface{}) {
		t.Helper()
		if err := db.SaveConfig(ctx, &ConfigRecord{Kind: KindProvider, UserID: "u1", Name: name,
			Enabled: true, Data: data}); err != nil {
			t.Fatalf("SaveConfig %s: %v", name, err)
		}
	}
	mirror := func(leaves map[string]ConfigValue) {
		t.Helper()
		for k, v := range leaves {
			if err := db.SetConfigValue(ctx, KindProvider, "user", "u1", k, v); err != nil {
				t.Fatalf("seed mirror %s: %v", k, err)
			}
		}
	}

	// openai: a complete, correctly tagged mirror.
	seed("openai", map[string]interface{}{"api_key": "sk-1", "api_base": "https://x"})
	mirror(map[string]ConfigValue{
		"openai.api_key":  StringValue("sk-1"),
		"openai.api_base": StringValue("https://x"),
	})

	// legacy: text matches, but the rows predate value_kind (untagged).
	seed("legacy", map[string]interface{}{"api_key": "sk-9"})
	mirror(map[string]ConfigValue{"legacy.api_key": {Value: "sk-9"}})

	// skew: untagged and the text disagrees — a real divergence, not a retag.
	seed("skew", map[string]interface{}{"api_key": "sk-new"})
	mirror(map[string]ConfigValue{"skew.api_key": {Value: "sk-old"}})

	// prefs: a tagged subset (locale missing) plus a stale marker.
	if err := db.SaveConfig(ctx, &ConfigRecord{Kind: KindSetting, UserID: "u1", Name: "prefs",
		Enabled: true, Data: map[string]interface{}{"timezone": "UTC", "locale": "zh"}}); err != nil {
		t.Fatalf("SaveConfig prefs: %v", err)
	}
	if err := db.SetConfigValue(ctx, KindSetting, "user", "u1", "prefs.timezone", StringValue("UTC")); err != nil {
		t.Fatalf("seed prefs mirror: %v", err)
	}
	if err := db.SaveConfigMirror(ctx, KindSetting, "user", "u1", "prefs",
		NewConfigMirror("prefs.", map[string]ConfigValue{"prefs.timezone": StringValue("UTC")})); err != nil {
		t.Fatalf("seed stale marker: %v", err)
	}

	rep, err := db.ReconcileConfigMirrors(ctx, false)
	if err != nil {
		t.Fatalf("ReconcileConfigMirrors: %v", err)
	}
	if rep.Examined != 4 || rep.Certified != 2 || rep.Untyped != 1 || rep.Retagged != 1 || len(rep.Gaps) != 2 {
		t.Fatalf("reconcile = %+v, want examined=4 certified=2 untyped=1 retagged=1 gaps=2", rep)
	}

	// openai certified with a verifying marker.
	openaiLeaves := map[string]ConfigValue{"openai.api_key": StringValue("sk-1"), "openai.api_base": StringValue("https://x")}
	if m, ok, _ := db.GetConfigMirror(ctx, KindProvider, "user", "u1", "openai"); !ok || !VerifyConfigMirror(m, openaiLeaves) {
		t.Fatalf("openai marker = %+v ok=%v", m, ok)
	}
	// legacy retagged: the stored kind is now filled, and its marker verifies.
	got, err := db.GetConfigValue(ctx, KindProvider, "user", "u1", "legacy.api_key")
	if err != nil {
		t.Fatalf("GetConfigValue legacy: %v", err)
	}
	if got.Kind != ValueKindString || got.Value != "sk-9" {
		t.Fatalf("legacy row = %+v, want value sk-9 tagged string", got)
	}
	legacyLeaves := map[string]ConfigValue{"legacy.api_key": StringValue("sk-9")}
	if m, ok, _ := db.GetConfigMirror(ctx, KindProvider, "user", "u1", "legacy"); !ok || !VerifyConfigMirror(m, legacyLeaves) {
		t.Fatalf("legacy marker = %+v ok=%v", m, ok)
	}

	// The two gaps are skew (value disagrees) and prefs (a missing leaf).
	gaps := map[string]ConfigMirrorGap{}
	for _, g := range rep.Gaps {
		gaps[g.Name] = g
	}
	if g, ok := gaps["skew"]; !ok || len(g.Changed) != 1 || g.Changed[0] != "skew.api_key" {
		t.Fatalf("skew gap = %+v, want changed=[skew.api_key]", g)
	}
	if g, ok := gaps["prefs"]; !ok || len(g.Missing) != 1 || g.Missing[0] != "prefs.locale" {
		t.Fatalf("prefs gap = %+v, want missing=[prefs.locale]", g)
	}
	// prefs' stale marker is gone.
	if _, ok, _ := db.GetConfigMirror(ctx, KindSetting, "user", "u1", "prefs"); ok {
		t.Fatal("stale marker survived a mismatched reconcile")
	}
	// skew never got a marker.
	if _, ok, _ := db.GetConfigMirror(ctx, KindProvider, "user", "u1", "skew"); ok {
		t.Fatal("diverged row was certified")
	}

	// Re-running is idempotent: the same verdict, but nothing left to retag.
	rep2, err := db.ReconcileConfigMirrors(ctx, false)
	if err != nil {
		t.Fatalf("second ReconcileConfigMirrors: %v", err)
	}
	if rep2.Examined != 4 || rep2.Certified != 2 || len(rep2.Gaps) != 2 || rep2.Untyped != 0 || rep2.Retagged != 0 {
		t.Fatalf("second reconcile = %+v, want the same verdict with no retagging", rep2)
	}
}

// A stored tag that contradicts the blob is a divergence, not a retag.
func TestReconcileRejectsContradictingKind(t *testing.T) {
	db, err := NewDBStore("sqlite", "file:reconcile_kind?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.SaveConfig(ctx, &ConfigRecord{Kind: KindProvider, UserID: "u1", Name: "typed",
		Enabled: true, Data: map[string]interface{}{"api_key": "1234"}}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	// Same text, but stored as a number while the blob says string.
	if err := db.SetConfigValue(ctx, KindProvider, "user", "u1", "typed.api_key",
		ConfigValue{Value: "1234", Kind: ValueKindNumber}); err != nil {
		t.Fatalf("seed mirror: %v", err)
	}
	rep, err := db.ReconcileConfigMirrors(ctx, false)
	if err != nil {
		t.Fatalf("ReconcileConfigMirrors: %v", err)
	}
	if rep.Certified != 0 || len(rep.Gaps) != 1 {
		t.Fatalf("reconcile = %+v, want 0 certified and 1 gap", rep)
	}
}

// --repair re-projects a diverged namespace from the blob: the stale key is
// cleared, the canonical projection is written, and the row is certified. The
// two seeds are the real dev/prod shapes — a nested map collapsed to one
// object leaf, and an ALL_CAPS data key folded to snake_case.
func TestReconcileRepairReprojectsDiverged(t *testing.T) {
	db, err := NewDBStore("sqlite", "file:reconcile_repair?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// dev: tools.providers is a nested map, the mirror collapsed it.
	if err := db.SaveConfig(ctx, &ConfigRecord{Kind: KindSetting, Name: "tools.providers",
		Enabled: true, Data: map[string]interface{}{
			"searxng": map[string]interface{}{"endpoint": "https://searxng.example"},
		}}); err != nil {
		t.Fatalf("SaveConfig tools.providers: %v", err)
	}
	if err := db.SetConfigValue(ctx, KindSetting, "system", "", "tools.providers.searxng",
		ConfigValue{Value: `{"endpoint": "https://searxng.example"}`}); err != nil {
		t.Fatalf("seed collapsed row: %v", err)
	}

	// prod: skills.entries.<id>.env holds map keys, an older build snake_cased
	// AppID on the way in.
	if err := db.SaveConfig(ctx, &ConfigRecord{Kind: KindSetting, AgentID: "agt1", Name: "skills.entries",
		Enabled: true, Data: map[string]interface{}{
			"ai-berkshire": map[string]interface{}{"env": map[string]interface{}{"AppID": "wx1"}},
		}}); err != nil {
		t.Fatalf("SaveConfig skills.entries: %v", err)
	}
	if err := db.SetConfigValue(ctx, KindSetting, "agent", "agt1", "skills.entries.ai-berkshire.env.app_i_d",
		StringValue("wx1")); err != nil {
		t.Fatalf("seed folded row: %v", err)
	}

	// Default pass: report both, touch neither.
	rep, err := db.ReconcileConfigMirrors(ctx, false)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rep.Repaired != 0 || len(rep.Gaps) != 2 {
		t.Fatalf("default pass = %+v, want 2 gaps and no repair", rep)
	}
	if _, err := db.GetConfigValue(ctx, KindSetting, "system", "", "tools.providers.searxng"); err != nil {
		t.Fatal("default pass modified the mirror")
	}

	// --repair: re-project both from the blob.
	rep, err = db.ReconcileConfigMirrors(ctx, true)
	if err != nil {
		t.Fatalf("reconcile --repair: %v", err)
	}
	if rep.Repaired != 2 || len(rep.Gaps) != 0 || rep.Certified != 2 {
		t.Fatalf("repair pass = %+v, want 2 repaired, 2 certified, 0 gaps", rep)
	}

	// The collapsed ancestor is gone, the canonical leaf is there and tagged.
	if _, err := db.GetConfigValue(ctx, KindSetting, "system", "", "tools.providers.searxng"); err == nil {
		t.Fatal("collapsed key survived the repair")
	}
	got, err := db.GetConfigValue(ctx, KindSetting, "system", "", "tools.providers.searxng.endpoint")
	if err != nil || got.Value != "https://searxng.example" || got.Kind != ValueKindString {
		t.Fatalf("repaired leaf = %+v err=%v", got, err)
	}

	// The folded data key came back verbatim: env keys are data keys, not
	// struct fields.
	if _, err := db.GetConfigValue(ctx, KindSetting, "agent", "agt1", "skills.entries.ai-berkshire.env.app_i_d"); err == nil {
		t.Fatal("folded key survived the repair")
	}
	got, err = db.GetConfigValue(ctx, KindSetting, "agent", "agt1", "skills.entries.ai-berkshire.env.AppID")
	if err != nil || got.Value != "wx1" {
		t.Fatalf("repaired env key = %+v err=%v", got, err)
	}

	// Both markers now verify against the projection.
	for _, c := range []struct{ scope, scopeID, name string }{
		{"system", "", "tools.providers"},
		{"agent", "agt1", "skills.entries"},
	} {
		m, ok, err := db.GetConfigMirror(ctx, KindSetting, c.scope, c.scopeID, c.name)
		if err != nil || !ok {
			t.Fatalf("marker %s/%s: ok=%v err=%v", c.scope, c.scopeID, ok, err)
		}
		leaves, err := db.ListConfigValues(ctx, KindSetting, c.scope, c.scopeID,
			MirrorPrefixFor(KindSetting, c.name))
		if err != nil {
			t.Fatalf("leaves %s: %v", c.name, err)
		}
		if !VerifyConfigMirror(m, leaves) {
			t.Fatalf("marker %s/%s does not verify", c.scope, c.scopeID)
		}
	}

	// Idempotent: a second repair pass has nothing left to do.
	rep2, err := db.ReconcileConfigMirrors(ctx, true)
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if rep2.Repaired != 0 || len(rep2.Gaps) != 0 || rep2.Certified != 2 {
		t.Fatalf("second repair = %+v, want nothing repaired", rep2)
	}
}
