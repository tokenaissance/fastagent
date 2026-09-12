package store

import (
	"context"
	"testing"
)

func TestConfigMirrorRoundTrip(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	leaves := map[string]ConfigValue{"openai.api_key": StringValue("sk-1")}
	if err := db.SaveConfigMirror(ctx, KindProvider, "user", "u1", "openai",
		NewConfigMirror("openai.", leaves)); err != nil {
		t.Fatalf("SaveConfigMirror: %v", err)
	}
	got, ok, err := db.GetConfigMirror(ctx, KindProvider, "user", "u1", "openai")
	if err != nil || !ok {
		t.Fatalf("GetConfigMirror = %+v, ok=%v, err=%v", got, ok, err)
	}
	if !VerifyConfigMirror(got, leaves) {
		t.Fatalf("round-tripped marker does not verify: %+v", got)
	}

	// Upsert replaces the fingerprint: the old leaf set must stop verifying.
	updated := map[string]ConfigValue{"openai.api_key": StringValue("sk-2")}
	if err := db.SaveConfigMirror(ctx, KindProvider, "user", "u1", "openai",
		NewConfigMirror("openai.", updated)); err != nil {
		t.Fatalf("SaveConfigMirror upsert: %v", err)
	}
	got, _, _ = db.GetConfigMirror(ctx, KindProvider, "user", "u1", "openai")
	if VerifyConfigMirror(got, leaves) {
		t.Fatal("stale marker still verifies after the projection changed")
	}
	if !VerifyConfigMirror(got, updated) {
		t.Fatal("updated marker does not verify the new leaves")
	}

	// A row with no marker is reported as absent, not as an error.
	if _, ok, err := db.GetConfigMirror(ctx, KindProvider, "user", "u1", "nope"); err != nil || ok {
		t.Fatalf("missing marker = ok=%v err=%v, want false/nil", ok, err)
	}

	if err := db.DeleteConfigMirror(ctx, KindProvider, "user", "u1", "openai"); err != nil {
		t.Fatalf("DeleteConfigMirror: %v", err)
	}
	if _, ok, _ := db.GetConfigMirror(ctx, KindProvider, "user", "u1", "openai"); ok {
		t.Fatal("marker survived DeleteConfigMirror")
	}
}

// The backfill projects a whole blob row, so it certifies its own output: a
// backfilled row must carry a marker that verifies against the rows it wrote.
func TestMigrateConfigsToKVWritesMirrorMarkers(t *testing.T) {
	db, err := NewDBStore("sqlite", "file:mirror_migrate?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if err := db.SaveConfig(ctx, &ConfigRecord{
		Kind:    KindProvider,
		UserID:  "u1",
		Name:    "openai",
		Enabled: true,
		Data:    map[string]interface{}{"api_key": "sk-1", "api_base": "https://api.openai.com"},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	// A fresh DB leaves configs_kv empty, so the backfill still runs.
	if err := db.migrateConfigsToKV(ctx); err != nil {
		t.Fatalf("migrateConfigsToKV: %v", err)
	}

	leaves, err := db.ListConfigValues(ctx, KindProvider, "user", "u1", "openai.")
	if err != nil {
		t.Fatalf("ListConfigValues: %v", err)
	}
	if len(leaves) == 0 {
		t.Fatal("backfill wrote no leaves")
	}
	m, ok, err := db.GetConfigMirror(ctx, KindProvider, "user", "u1", "openai")
	if err != nil {
		t.Fatalf("GetConfigMirror: %v", err)
	}
	if !ok {
		t.Fatal("backfill did not certify the row it projected")
	}
	if !VerifyConfigMirror(m, leaves) {
		t.Fatalf("backfill marker does not verify its leaves: %+v", m)
	}
}
