package scope

import (
	"context"
	"errors"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// The writers own the "both tables or neither" promise. These tests inject a
// mirror failure and assert the authoritative blob row is not left behind —
// the state that used to be reachable, and the reason the readers carry a
// fallback at all.

var errMirrorDown = errors.New("configs_kv is down")

// failingKVStore fails the mirror write. WithTx re-wraps the transaction
// handle so the injected failure applies to the store the writer actually
// uses, whatever handle it was handed.
type failingKVStore struct {
	*store.DBStore
	failFrom int // fail on the Nth SetConfigValue call, 1-based
	calls    int
}

func (f *failingKVStore) SetConfigValue(ctx context.Context, kind, scope, scopeID, name string, value store.ConfigValue) error {
	f.calls++
	if f.calls >= f.failFrom {
		return errMirrorDown
	}
	return f.DBStore.SetConfigValue(ctx, kind, scope, scopeID, name, value)
}

func (f *failingKVStore) WithTx(ctx context.Context, fn func(store.Store) error) error {
	return f.DBStore.WithTx(ctx, func(tx store.Store) error {
		inner, ok := tx.(*store.DBStore)
		if !ok {
			return fn(tx)
		}
		return fn(&failingKVStore{DBStore: inner, failFrom: f.failFrom, calls: f.calls})
	})
}

func noRows(t *testing.T, db *store.DBStore, kind, scope, scopeID string) {
	t.Helper()
	rows, err := db.ListConfigValues(context.Background(), kind, scope, scopeID, "")
	if err != nil {
		t.Fatalf("ListConfigValues: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("partial mirror rows survived: %#v", rows)
	}
}

func noBlob(t *testing.T, db *store.DBStore, kind, uid, aid, name string) {
	t.Helper()
	rec, err := db.GetConfigByName(context.Background(), kind, uid, aid, name)
	if err == nil && rec != nil {
		t.Fatalf("blob row survived a failed mirror write: %+v", rec)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetConfigByName: %v", err)
	}
}

// A settings namespace flattens to several rows; failing on the second one
// proves the transaction covers the whole namespace, not just one statement.
func TestSaveSettingRollsBackBlobWhenMirrorFailsMidway(t *testing.T) {
	db := openScopeDBNamed(t, "tx_setting_midway")
	defer db.Close()
	ctx := context.Background()
	failing := &failingKVStore{DBStore: db, failFrom: 2}

	err := SaveSetting(ctx, failing, "u1", "", "objectstore", map[string]interface{}{
		"provider": "s3", "bucket": "b", "region": "us-east-1",
	})
	if !errors.Is(err, errMirrorDown) {
		t.Fatalf("SaveSetting = %v, want the mirror error", err)
	}
	noBlob(t, db, store.KindSetting, "u1", "", "objectstore")
	noRows(t, db, store.KindSetting, User, "u1")

	// Control: with a healthy store the same call writes both sides.
	if err := SaveSetting(ctx, db, "u1", "", "objectstore", map[string]interface{}{
		"provider": "s3", "bucket": "b", "region": "us-east-1",
	}); err != nil {
		t.Fatalf("SaveSetting(control): %v", err)
	}
	noBlobCheck, err := db.GetConfigByName(ctx, store.KindSetting, "u1", "", "objectstore")
	if err != nil || noBlobCheck == nil {
		t.Fatalf("control run did not write the blob row: %+v %v", noBlobCheck, err)
	}
	rows, err := db.ListConfigValues(ctx, store.KindSetting, User, "u1", "objectstore.")
	if err != nil || len(rows) != 3 {
		t.Fatalf("control run mirror = %#v (%v), want 3 rows", rows, err)
	}
}

func TestSaveProviderRollsBackBlobWhenMirrorFails(t *testing.T) {
	db := openScopeDBNamed(t, "tx_provider")
	defer db.Close()
	ctx := context.Background()
	failing := &failingKVStore{DBStore: db, failFrom: 1}

	err := SaveProviderState(ctx, failing, "u1", "", "openai",
		config.ProviderConfig{APIKey: "sk-x"}, true)
	if !errors.Is(err, errMirrorDown) {
		t.Fatalf("SaveProviderState = %v, want the mirror error", err)
	}
	noBlob(t, db, store.KindProvider, "u1", "", "openai")
	noRows(t, db, store.KindProvider, User, "u1")
}

func TestSaveAgentPluginEnabledRollsBackBlobWhenMirrorFails(t *testing.T) {
	db := openScopeDBNamed(t, "tx_plugin_enabled")
	defer db.Close()
	ctx := context.Background()
	failing := &failingKVStore{DBStore: db, failFrom: 1}

	err := SaveAgentPluginEnabled(ctx, failing, "a1", map[string]bool{"webSearch": true})
	if !errors.Is(err, errMirrorDown) {
		t.Fatalf("SaveAgentPluginEnabled = %v, want the mirror error", err)
	}
	noBlob(t, db, store.KindPluginEnabled, "", "a1", PluginEnabledNamespace)
	noRows(t, db, store.KindPluginEnabled, Agent, "a1")
}

// The delete half of the pair is transactional too: clearing a namespace
// removes the mirror prefix and the blob row together.
func TestSaveSettingDeleteIsPaired(t *testing.T) {
	db := openScopeDBNamed(t, "tx_delete_pair")
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "u1", "", "prefs", map[string]interface{}{"timezone": "Asia/Shanghai"}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	if err := SaveSetting(ctx, db, "u1", "", "prefs", nil); err != nil {
		t.Fatalf("SaveSetting(delete): %v", err)
	}
	noBlob(t, db, store.KindSetting, "u1", "", "prefs")
	noRows(t, db, store.KindSetting, User, "u1")
}
