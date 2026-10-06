package gateway

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agentconfig"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// TestResolvedAgentReadFeedsTheCacheStats walks the whole path once: the
// gateway adapter reads one agent through the versioned cache, a config write
// bumps the counter, a second read sees it, and the stats the ops endpoint
// serves report both reads. The unit tests in internal/agentconfig cover the
// arithmetic; this one covers the wiring, which is what the endpoint actually
// reports on a running pod.
func TestResolvedAgentReadFeedsTheCacheStats(t *testing.T) {
	db := openEpochStore(t)
	ctx := context.Background()

	agentID, userID := "agt_stats", "u_stats"
	if err := db.SaveAgent(ctx, &store.AgentRecord{ID: agentID, UserID: userID, Name: "stats"}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	g := &Gateway{store: db}
	scope := agentconfig.Scope{UserID: userID, AgentID: agentID}

	first, err := g.resolvedAgentFor(ctx, scope)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// A real config write. The write itself stamps now (P0b): the row and the
	// counter commit together, so no caller has to remember the bump.
	if err := db.SaveAgent(ctx, &store.AgentRecord{ID: agentID, UserID: userID, Name: "stats-renamed"}); err != nil {
		t.Fatalf("write agent row: %v", err)
	}

	second, err := g.resolvedAgentFor(ctx, scope)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("scope drifted: %q then %q", first.ID, second.ID)
	}

	snapshot, ok := g.AgentConfigCacheSnapshot(ctx)
	if !ok {
		t.Fatal("AgentConfigCacheSnapshot reported no cache after two reads")
	}
	stats := snapshot.Stats
	if stats.Checks < 2 {
		t.Fatalf("checks = %d; want one per read", stats.Checks)
	}
	if stats.Rebuilds < 2 {
		t.Fatalf("rebuilds = %d; want the first read plus the read after the bump", stats.Rebuilds)
	}
	if stats.StaleHits < 1 {
		t.Fatalf("staleHits = %d; want the read that saw the bump", stats.StaleHits)
	}
	if stats.CheckSeconds.Samples < 2 || stats.RebuildSeconds.Samples < 2 {
		t.Fatalf("latency windows empty: check=%+v rebuild=%+v", stats.CheckSeconds, stats.RebuildSeconds)
	}
}
