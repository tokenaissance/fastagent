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
		NewConfigMirror("openai.", true, leaves)); err != nil {
		t.Fatalf("SaveConfigMirror: %v", err)
	}
	got, ok, err := db.GetConfigMirror(ctx, KindProvider, "user", "u1", "openai")
	if err != nil || !ok {
		t.Fatalf("GetConfigMirror = %+v, ok=%v, err=%v", got, ok, err)
	}
	if !VerifyConfigMirror(got, true, leaves) {
		t.Fatalf("round-tripped marker does not verify: %+v", got)
	}

	// Upsert replaces the fingerprint: the old leaf set must stop verifying.
	updated := map[string]ConfigValue{"openai.api_key": StringValue("sk-2")}
	if err := db.SaveConfigMirror(ctx, KindProvider, "user", "u1", "openai",
		NewConfigMirror("openai.", true, updated)); err != nil {
		t.Fatalf("SaveConfigMirror upsert: %v", err)
	}
	got, _, _ = db.GetConfigMirror(ctx, KindProvider, "user", "u1", "openai")
	if VerifyConfigMirror(got, true, leaves) {
		t.Fatal("stale marker still verifies after the mirror changed")
	}
	if !VerifyConfigMirror(got, true, updated) {
		t.Fatal("updated marker does not verify the new leaves")
	}

	// The enabled flag round-trips too, and flipping it invalidates the marker
	// exactly like a changed leaf does — a decision recorded in the marker is a
	// decision a later reader compares against, not a label it copies.
	if err := db.SaveConfigMirror(ctx, KindProvider, "user", "u1", "openai",
		NewConfigMirror("openai.", false, updated)); err != nil {
		t.Fatalf("SaveConfigMirror disable: %v", err)
	}
	got, _, _ = db.GetConfigMirror(ctx, KindProvider, "user", "u1", "openai")
	if got.Enabled == nil || *got.Enabled {
		t.Fatalf("marker enabled = %v, want a recorded false", got.Enabled)
	}
	if VerifyConfigMirror(got, true, updated) {
		t.Fatal("a marker recording disabled verified an enabled row")
	}
	if !VerifyConfigMirror(got, false, updated) {
		t.Fatal("a marker recording disabled did not verify a disabled row")
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

// The backfill mirrors a whole blob row, so it certifies its own output: a
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
		t.Fatal("backfill did not certify the row it mirrored")
	}
	if !VerifyConfigMirror(m, true, leaves) {
		t.Fatalf("backfill marker does not verify its leaves: %+v", m)
	}
}

// A configs_mirror table created before the enabled column existed (CREATE
// TABLE IF NOT EXISTS leaves an existing table alone) gets the column added,
// and its existing markers keep NULL — "no decision was recorded" — rather than
// being handed a default that would read as a decision nobody made.
func TestMigrateConfigsMirrorEnabledRetrofitsLegacyMarkers(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Rebuild the table in the shape a build that predates the column left.
	if _, err := db.handle().ExecContext(ctx, `DROP TABLE configs_mirror`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.handle().ExecContext(ctx, `CREATE TABLE configs_mirror (
		kind TEXT NOT NULL,
		scope TEXT NOT NULL,
		scope_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		prefix TEXT NOT NULL DEFAULT '',
		key_count INTEGER NOT NULL DEFAULT 0,
		fingerprint TEXT NOT NULL DEFAULT '',
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (kind, scope, scope_id, name)
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	leaves := map[string]ConfigValue{"openai.api_key": StringValue("sk-1")}
	if _, err := db.handle().ExecContext(ctx,
		`INSERT INTO configs_mirror (kind, scope, scope_id, name, prefix, key_count, fingerprint)
		 VALUES ('provider', 'user', 'u1', 'openai', 'openai.', 1, ?)`,
		ConfigsKVFingerprint(leaves)); err != nil {
		t.Fatalf("seed legacy marker: %v", err)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	has, err := db.tableHasColumn(ctx, "configs_mirror", "enabled")
	if err != nil || !has {
		t.Fatalf("enabled column missing after migration: has=%v err=%v", has, err)
	}

	m, ok, err := db.GetConfigMirror(ctx, KindProvider, "user", "u1", "openai")
	if err != nil || !ok {
		t.Fatalf("legacy marker unreadable: ok=%v err=%v", ok, err)
	}
	if m.Enabled != nil {
		t.Fatalf("legacy marker enabled = %v, want no recorded decision", *m.Enabled)
	}
	// An unrecorded decision is not a default: the marker certifies nothing
	// until a write (or a reconcile pass) records the decision for real.
	if VerifyConfigMirror(m, true, leaves) || VerifyConfigMirror(m, false, leaves) {
		t.Fatal("legacy marker certified a decision it never recorded")
	}
}

// ListConfigMirrors is the batched form of GetConfigMirror: every marker at one
// (kind, scope, scope_id), keyed by row name, and nothing from other scopes.
func TestListConfigMirrors(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	openaiLeaves := map[string]ConfigValue{"openai.api_key": StringValue("sk-1")}
	anthropicLeaves := map[string]ConfigValue{"anthropic.api_key": StringValue("sk-ant")}
	if err := db.SaveConfigMirror(ctx, KindProvider, "user", "u1", "openai",
		NewConfigMirror("openai.", true, openaiLeaves)); err != nil {
		t.Fatalf("SaveConfigMirror openai: %v", err)
	}
	if err := db.SaveConfigMirror(ctx, KindProvider, "user", "u1", "anthropic",
		NewConfigMirror("anthropic.", false, anthropicLeaves)); err != nil {
		t.Fatalf("SaveConfigMirror anthropic: %v", err)
	}
	// A different scope and a different kind must not leak in.
	if err := db.SaveConfigMirror(ctx, KindProvider, "user", "u2", "openai",
		NewConfigMirror("openai.", true, openaiLeaves)); err != nil {
		t.Fatalf("SaveConfigMirror other scope: %v", err)
	}
	if err := db.SaveConfigMirror(ctx, KindSetting, "user", "u1", "prefs",
		NewConfigMirror("prefs.", true, map[string]ConfigValue{})); err != nil {
		t.Fatalf("SaveConfigMirror other kind: %v", err)
	}

	got, err := db.ListConfigMirrors(ctx, KindProvider, "user", "u1")
	if err != nil {
		t.Fatalf("ListConfigMirrors: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListConfigMirrors returned %d markers, want 2: %#v", len(got), got)
	}
	if !VerifyConfigMirror(got["openai"], true, openaiLeaves) {
		t.Fatalf("openai marker = %+v, want a certified enabled marker", got["openai"])
	}
	if !VerifyConfigMirror(got["anthropic"], false, anthropicLeaves) {
		t.Fatalf("anthropic marker = %+v, want a certified disabled marker", got["anthropic"])
	}

	empty, err := db.ListConfigMirrors(ctx, KindProvider, "user", "nobody")
	if err != nil {
		t.Fatalf("ListConfigMirrors(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ListConfigMirrors(empty) = %#v, want empty map", empty)
	}
}
