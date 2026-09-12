package gateway

// assembleConfig reads the "bindings" setting namespace through an envelope
// (config.BindingsPayload) because the stored shape is {"list":[...]} while
// the runtime field is a bare slice. Projecting the stored map straight into
// []config.Binding fails with "cannot unmarshal object into Go value of type
// []config.Binding", and assembleConfig returns that error — fatal for the
// whole UserSpace load, not just for bindings. It never tripped in dev only
// because no bindings row has been written yet.

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func openBindingsTestDB(t *testing.T) *store.DBStore {
	t.Helper()
	db, err := store.NewDBStore("sqlite", "file:bindings_row?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestAssembleConfigReadsBindingsEnvelope(t *testing.T) {
	t.Setenv("FASTAGENT_HOME", t.TempDir())
	db := openBindingsTestDB(t)
	ctx := context.Background()

	if err := scope.SaveSetting(ctx, db, "", "", "bindings", map[string]interface{}{
		"list": []interface{}{
			map[string]interface{}{
				"agentId": "agt_bound",
				"match": map[string]interface{}{
					"channel":   "telegram",
					"accountId": "acct-1",
					"peer":      map[string]interface{}{"kind": "user", "id": "42"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("SaveSetting bindings: %v", err)
	}

	cfg, err := assembleConfig(ctx, db, "u_bound", "")
	if err != nil {
		t.Fatalf("assembleConfig: %v", err)
	}
	if len(cfg.Bindings) != 1 {
		t.Fatalf("bindings = %#v, want the single stored row", cfg.Bindings)
	}
	got := cfg.Bindings[0]
	if got.AgentID != "agt_bound" || got.Match.Channel != "telegram" || got.Match.AccountID != "acct-1" {
		t.Fatalf("binding = %#v, want the stored agent/channel/account", got)
	}
	if got.Match.Peer == nil || got.Match.Peer.ID != "42" {
		t.Fatalf("binding peer = %#v, want id 42", got.Match.Peer)
	}
}

// A missing bindings row must leave whatever the caller already synthesized
// in place, so the envelope seeding trick does not wipe preloaded bindings.
func TestAssembleConfigKeepsBindingsWhenRowAbsent(t *testing.T) {
	t.Setenv("FASTAGENT_HOME", t.TempDir())
	db := openBindingsTestDB(t)

	cfg, err := assembleConfig(context.Background(), db, "u_bound", "")
	if err != nil {
		t.Fatalf("assembleConfig: %v", err)
	}
	if len(cfg.Bindings) != 0 {
		t.Fatalf("bindings = %#v, want none", cfg.Bindings)
	}
}
