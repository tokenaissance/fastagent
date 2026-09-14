package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/taskqueue"
)

// newGatewayConfigStore is a migrated sqlite store; the reload reads the same
// system-scope namespace the boot path does, so it needs real config rows.
func newGatewayConfigStore(t *testing.T) *store.DBStore {
	t.Helper()
	db, err := store.NewDBStore("sqlite", "file:taskqueue_reload?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func saveTaskQueueSetting(t *testing.T, db *store.DBStore, data map[string]any) {
	t.Helper()
	if err := scope.SaveSetting(context.Background(), db, "", "", "taskqueue", data); err != nil {
		t.Fatalf("save taskqueue setting: %v", err)
	}
}

// ReloadTaskQueue is what makes "save the taskqueue setting" mean "applies to
// the next turn" instead of "applies at the next rollout". It reads the same
// system-scope namespace the boot path reads and pushes the values into the
// live queue + the cron policy field.
func TestReloadTaskQueueAppliesSystemConfig(t *testing.T) {
	st := newGatewayConfigStore(t)
	g := &Gateway{
		store:     st,
		taskQueue: taskqueue.NewQueue(1, 300*time.Second, nil),
	}
	t.Cleanup(func() { g.taskQueue.Stop() })

	if got := g.taskQueue.DefaultTimeout(); got != 300*time.Second {
		t.Fatalf("boot default = %s; want 300s", got)
	}
	if got := g.taskTimeoutFor(bus.InboundMessage{Source: bus.SourceCron}); got != 0 {
		t.Fatalf("cron budget before reload = %s; want 0 (queue default)", got)
	}

	saveTaskQueueSetting(t, st, map[string]any{
		"maxConcurrent":  7,
		"taskTimeoutSec": 12,
		"cronTimeoutSec": 45,
	})
	if err := g.ReloadTaskQueue(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if got := g.taskQueue.DefaultTimeout(); got != 12*time.Second {
		t.Fatalf("default timeout after reload = %s; want 12s", got)
	}
	if got := g.taskTimeoutFor(bus.InboundMessage{Source: bus.SourceCron}); got != 45*time.Second {
		t.Fatalf("cron budget after reload = %s; want 45s", got)
	}
	if got := g.taskTimeoutFor(bus.InboundMessage{Source: bus.SourceUser}); got != 0 {
		t.Fatalf("user budget after reload = %s; want 0 (queue default)", got)
	}

	// Clearing the cron knob returns cron to the default without touching the
	// rest of the namespace.
	saveTaskQueueSetting(t, st, map[string]any{"cronTimeoutSec": 0})
	if err := g.ReloadTaskQueue(); err != nil {
		t.Fatalf("reload after clear: %v", err)
	}
	if got := g.taskTimeoutFor(bus.InboundMessage{Source: bus.SourceCron}); got != 0 {
		t.Fatalf("cron budget after clearing = %s; want 0", got)
	}
}

// No store (CLI/test harness) and no queue must both be no-ops rather than
// panics: the reload hook is best-effort by contract.
func TestReloadTaskQueueDegradesQuietly(t *testing.T) {
	g := &Gateway{}
	if err := g.ReloadTaskQueue(); err != nil {
		t.Fatalf("reload with no store: %v", err)
	}
	g = &Gateway{store: newGatewayConfigStore(t)}
	if err := g.ReloadTaskQueue(); err != nil {
		t.Fatalf("reload with no queue: %v", err)
	}
}
