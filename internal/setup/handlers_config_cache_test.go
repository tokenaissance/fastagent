package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agentconfig"
)

// TestAdminConfigCacheReportsStats is the witness for P2 item 4 in
// docs/fastagent/design/15-agent-config-consistency.md §6: the read cache's
// counters reach the ops surface, and an unwired reader answers 503 instead of
// reporting zeros.
func TestAdminConfigCacheReportsStats(t *testing.T) {
	s := NewServer(0)
	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.handleAdminConfigCache(w, httptest.NewRequest(http.MethodGet, "/api/admin/config-cache", nil))
		return w
	}

	if w := get(); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired status = %d; want 503 so a missing measurement cannot read as healthy", w.Code)
	}

	s.SetConfigCacheStats(func(context.Context) (agentconfig.Snapshot, bool) {
		return agentconfig.Snapshot{Counter: 42, Stats: agentconfig.Stats{
			Checks:            1000,
			Hits:              990,
			Rebuilds:          10,
			StaleHits:         10,
			RebuildRate:       0.01,
			CheckSeconds:      agentconfig.LatencyStats{P50: 0.0011, P99: 0.0029, Samples: 512},
			RebuildSeconds:    agentconfig.LatencyStats{P50: 0.02, P99: 0.08, Samples: 10},
			VersionLagSeconds: 0.4,
		}}, true
	})

	w := get()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	var body struct {
		Built   bool              `json:"built"`
		Counter int64             `json:"counter"`
		Stats   agentconfig.Stats `json:"stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Built {
		t.Fatal("built = false; want true when the reader answers")
	}
	if body.Counter != 42 {
		t.Fatalf("counter = %d; want the live counter the snapshot carries", body.Counter)
	}
	if body.Stats.Checks != 1000 || body.Stats.Rebuilds != 10 {
		t.Fatalf("counters lost: %+v", body.Stats)
	}
	if body.Stats.RebuildRate != 0.01 {
		t.Fatalf("rebuildRate = %v; want 0.01", body.Stats.RebuildRate)
	}
	if body.Stats.CheckSeconds.P99 != 0.0029 {
		t.Fatalf("check p99 = %v; want the value the use case reported", body.Stats.CheckSeconds.P99)
	}
	if body.Stats.VersionLagSeconds != 0.4 {
		t.Fatalf("versionLagSeconds = %v; want 0.4", body.Stats.VersionLagSeconds)
	}
}
