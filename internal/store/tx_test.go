package store

import (
	"context"
	"errors"
	"testing"
)

// The configs / configs_kv pair is written from two tables, so "both or
// neither" only holds if the writes share a transaction. These tests pin the
// transaction plumbing itself; the scope-level tests pin that the writers
// actually use it.

func openTxStore(t *testing.T, name string) *DBStore {
	t.Helper()
	db, err := NewDBStore("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store %s: %v", name, err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate %s: %v", name, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func kvRow(t *testing.T, db *DBStore, scope, scopeID, name string) (ConfigValue, bool) {
	t.Helper()
	v, err := db.GetConfigValue(context.Background(), KindSetting, scope, scopeID, name)
	if errors.Is(err, ErrNotFound) {
		return ConfigValue{}, false
	}
	if err != nil {
		t.Fatalf("GetConfigValue(%s): %v", name, err)
	}
	return v, true
}

func TestWithTxCommitAndRollback(t *testing.T) {
	db := openTxStore(t, "tx_commit_rollback")
	ctx := context.Background()

	write := func(tx Store, tz string) error {
		if err := tx.SetConfigValue(ctx, KindSetting, "user", "u1", "prefs.timezone",
			EncodeConfigValue(tz)); err != nil {
			return err
		}
		return tx.SaveConfig(ctx, &ConfigRecord{
			Kind: KindSetting, UserID: "u1", Name: "prefs", Enabled: true,
			Data: map[string]interface{}{"timezone": tz},
		})
	}

	// Commit path: both tables land.
	if err := db.WithTx(ctx, func(tx Store) error { return write(tx, "Asia/Shanghai") }); err != nil {
		t.Fatalf("WithTx(commit): %v", err)
	}
	if v, ok := kvRow(t, db, "user", "u1", "prefs.timezone"); !ok || v.Value != "Asia/Shanghai" {
		t.Fatalf("mirror row missing after commit: %+v ok=%v", v, ok)
	}
	if rec, err := db.GetConfigByName(ctx, KindSetting, "u1", "", "prefs"); err != nil || rec == nil {
		t.Fatalf("blob row missing after commit: %+v %v", rec, err)
	}

	// Rollback path: fn's error comes back and neither table moves.
	boom := errors.New("boom")
	err := db.WithTx(ctx, func(tx Store) error {
		if err := write(tx, "Europe/Berlin"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx = %v, want the callback's error", err)
	}
	if v, ok := kvRow(t, db, "user", "u1", "prefs.timezone"); !ok || v.Value != "Asia/Shanghai" {
		t.Fatalf("rollback lost the committed mirror row: %+v ok=%v", v, ok)
	}
	rec, err := db.GetConfigByName(ctx, KindSetting, "u1", "", "prefs")
	if err != nil || rec == nil {
		t.Fatalf("rollback lost the committed blob row: %+v %v", rec, err)
	}
	if rec.Data["timezone"] != "Asia/Shanghai" {
		t.Fatalf("blob row kept the rolled-back value: %#v", rec.Data)
	}
}

// A statement failure in the middle of the transaction must undo the writes
// that already succeeded — the trigger stands in for a constraint violation
// or a dropped connection at the worst possible moment.
func TestWithTxRollsBackOnStatementFailure(t *testing.T) {
	db := openTxStore(t, "tx_statement_failure")
	ctx := context.Background()

	if _, err := db.db.Exec(
		`CREATE TRIGGER block_configs BEFORE INSERT ON configs
		 BEGIN SELECT RAISE(ABORT, 'configs write blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	err := db.WithTx(ctx, func(tx Store) error {
		if err := tx.SetConfigValue(ctx, KindSetting, "user", "u1", "prefs.timezone",
			EncodeConfigValue("Asia/Shanghai")); err != nil {
			return err
		}
		return tx.SaveConfig(ctx, &ConfigRecord{
			Kind: KindSetting, UserID: "u1", Name: "prefs", Enabled: true,
			Data: map[string]interface{}{"timezone": "Asia/Shanghai"},
		})
	})
	if err == nil {
		t.Fatal("WithTx returned nil although the blob write was rejected")
	}
	if v, ok := kvRow(t, db, "user", "u1", "prefs.timezone"); ok {
		t.Fatalf("mirror row survived a failed transaction: %+v", v)
	}
}

// A nested WithTx joins the running transaction instead of opening a second
// one, so an outer rollback undoes the inner write too.
func TestWithTxNestedJoinsOuter(t *testing.T) {
	db := openTxStore(t, "tx_nested")
	ctx := context.Background()

	boom := errors.New("outer boom")
	err := db.WithTx(ctx, func(tx Store) error {
		txer, ok := tx.(Txer)
		if !ok {
			t.Fatal("the transaction handle should still be a Txer")
		}
		if err := txer.WithTx(ctx, func(inner Store) error {
			return inner.SetConfigValue(ctx, KindSetting, "user", "u1", "prefs.timezone",
				EncodeConfigValue("Asia/Shanghai"))
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx = %v, want the outer error", err)
	}
	if v, ok := kvRow(t, db, "user", "u1", "prefs.timezone"); ok {
		t.Fatalf("inner write survived the outer rollback: %+v", v)
	}
}

// Stores without transaction support still work through the WithTx helper —
// they just get no atomicity, which is the honest contract for a test double.
type plainStore struct{ Store }

func TestWithTxFallsBackForStoresWithoutSupport(t *testing.T) {
	db := openTxStore(t, "tx_fallback")
	var got Store
	err := WithTx(context.Background(), plainStore{Store: db}, func(st Store) error {
		got = st
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
	if got == nil {
		t.Fatal("fn was not called for a store without transaction support")
	}
}
