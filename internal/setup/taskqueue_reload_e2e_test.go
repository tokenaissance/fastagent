package setup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/api"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// taskQueueReloadResolver is the Cloud admin path's resolver shape: it can
// reload the running queue (the real one forwards to Gateway.ReloadTaskQueue).
type taskQueueReloadResolver struct {
	reloads int
}

func (r *taskQueueReloadResolver) UserSpaceFor(string) (*api.UserSpaceView, error) {
	return nil, nil
}
func (r *taskQueueReloadResolver) LocalAgentManager() *agent.Manager { return nil }
func (r *taskQueueReloadResolver) IsCloudMode() bool                 { return true }
func (r *taskQueueReloadResolver) ReloadTaskQueue() error {
	r.reloads++
	return nil
}

// Saving the taskqueue namespace at system scope must reload the live queue —
// otherwise an operator's budget/concurrency change silently waits for the next
// rollout, which is exactly the trap the sandbox namespace already avoids.
// User-scope saves must not fire it (the queue is gateway-wide), and a resolver
// without the capability degrades to a no-op instead of failing the save.
func TestTaskQueue_HotReloadCloudPathE2E(t *testing.T) {
	s, uid, _ := setupFileUploadTest(t)
	resolver := &taskQueueReloadResolver{}
	s.userResolver = resolver

	// ── 1. System-scope save fires the reload once and persists the row.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config",
		strings.NewReader(`{"taskQueue":{"maxConcurrent":8,"taskTimeoutSec":420,"cronTimeoutSec":900}}`))
	req.Header.Set("Content-Type", "application/json")
	s.handleUpdateConfig(rec, stampSystemAdmin(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("system taskqueue save status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if resolver.reloads != 1 {
		t.Fatalf("ReloadTaskQueue called %d time(s); want 1", resolver.reloads)
	}
	row, err := s.dataStore.GetConfigByName(context.Background(), store.KindSetting, "", "", "taskqueue")
	if err != nil || row == nil {
		t.Fatalf("system taskqueue row missing: err=%v row=%v", err, row)
	}
	if got := fmt.Sprintf("%v", row.Data["cronTimeoutSec"]); got != "900" {
		t.Fatalf("persisted cronTimeoutSec = %s; want 900", got)
	}

	// ── 2. A user-scope save must not touch the gateway-wide queue.
	rec = httptest.NewRecorder()
	s.handleUpdateConfig(rec, cfgReq(t, http.MethodPost, uid, `{"taskQueue":{"taskTimeoutSec":60}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("user taskqueue save status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if resolver.reloads != 1 {
		t.Fatalf("user-scope save fired ReloadTaskQueue %d time(s); want still 1", resolver.reloads)
	}

	// ── 3. Resolver without the hook: save still succeeds (applies at boot).
	s.userResolver = &recordingChannelResolver{}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/config",
		strings.NewReader(`{"taskQueue":{"taskTimeoutSec":300}}`))
	req.Header.Set("Content-Type", "application/json")
	s.handleUpdateConfig(rec, stampSystemAdmin(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("no-reloader system save status = %d, body=%s", rec.Code, rec.Body.String())
	}
}
