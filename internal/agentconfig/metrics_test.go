package agentconfig

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// TestStatsCountStaleHitsAndVersionLag is the witness for the two metrics the
// ops surface reports: a read that finds the cache behind the counter raises
// staleHits, and it records how old the cached entry was.
func TestStatsCountStaleHitsAndVersionLag(t *testing.T) {
	store := &fakeStore{version: 1, servers: map[string]config.MCPServerConfig{}}
	uc, err := New(store, NewMemoryMemo(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scope := Scope{UserID: "u_1", AgentID: "agt_1"}

	if _, err := uc.For(context.Background(), scope); err != nil {
		t.Fatalf("first read: %v", err)
	}
	// Hold the entry long enough that the lag is measurable.
	time.Sleep(20 * time.Millisecond)
	store.write(2, map[string]config.MCPServerConfig{"quandora": {URL: "https://x"}})
	if _, err := uc.For(context.Background(), scope); err != nil {
		t.Fatalf("second read: %v", err)
	}

	stats := uc.Stats()
	if stats.StaleHits != 1 {
		t.Fatalf("staleHits = %d; want 1 (the version check fired once)", stats.StaleHits)
	}
	if stats.VersionLagSeconds < 0.02 {
		t.Fatalf("versionLagSeconds = %v; want at least the 20ms the entry was cached", stats.VersionLagSeconds)
	}
	if stats.Checks != 2 || stats.Rebuilds != 2 {
		t.Fatalf("checks/rebuilds = %d/%d; want 2/2", stats.Checks, stats.Rebuilds)
	}
	if stats.RebuildRate != 1 {
		t.Fatalf("rebuildRate = %v; want 1 for two reads that both rebuilt", stats.RebuildRate)
	}
}

// TestStatsKeepTheRecentShape covers the latency window and the steady-state
// rule: a read that hits the memo adds no rebuild, so the rebuild rate falls,
// and the window reports both percentiles once a sample exists.
func TestStatsKeepTheRecentShape(t *testing.T) {
	store := &fakeStore{version: 1, servers: map[string]config.MCPServerConfig{}}
	uc, err := New(store, NewMemoryMemo(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scope := Scope{UserID: "u_1", AgentID: "agt_1"}

	for i := 0; i < 4; i++ {
		if _, err := uc.For(context.Background(), scope); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}

	stats := uc.Stats()
	if stats.Hits != 3 {
		t.Fatalf("hits = %d; want 3 of 4 reads served from the memo", stats.Hits)
	}
	if stats.Rebuilds != 1 {
		t.Fatalf("rebuilds = %d; want 1", stats.Rebuilds)
	}
	if stats.RebuildRate != 0.25 {
		t.Fatalf("rebuildRate = %v; want 0.25", stats.RebuildRate)
	}
	if stats.StaleHits != 0 || stats.VersionLagSeconds != 0 {
		t.Fatalf("steady state reported movement: staleHits=%d lag=%v", stats.StaleHits, stats.VersionLagSeconds)
	}
	if stats.CheckSeconds.Samples != 4 {
		t.Fatalf("check samples = %d; want one per read", stats.CheckSeconds.Samples)
	}
	if stats.CheckSeconds.P99 < stats.CheckSeconds.P50 {
		t.Fatalf("p99 %v < p50 %v", stats.CheckSeconds.P99, stats.CheckSeconds.P50)
	}
	if stats.RebuildSeconds.Samples != 1 {
		t.Fatalf("rebuild samples = %d; want one per rebuild", stats.RebuildSeconds.Samples)
	}
}
