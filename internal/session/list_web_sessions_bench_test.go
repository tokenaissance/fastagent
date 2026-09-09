package session

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// benchSeed builds a database with n sessions (each with one append-only
// user opening turn) and a StoreAdapter owned by ownerID.
func benchSeed(b *testing.B, ownerID, agentID string, n int) (*store.DBStore, *StoreAdapter) {
	b.Helper()
	db, err := store.NewDBStore("sqlite", "file:"+filepath.Join(b.TempDir(), "sessions.db")+"?cache=shared")
	if err != nil {
		b.Fatalf("NewDBStore: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		b.Fatalf("Migrate: %v", err)
	}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("sbk_bench_%d", i)
		if err := db.SaveSession(ctx, ownerID, agentID, key, &store.SessionRecord{
			Channel: "web",
			Messages: []store.SessionMessage{
				{Role: "user", Content: fmt.Sprintf("opening %d", i), Timestamp: time.Now().UTC()},
			},
		}); err != nil {
			b.Fatalf("SaveSession %d: %v", i, err)
		}
		if err := db.AppendSessionMessage(ctx, ownerID, agentID, key, store.SessionMessage{
			Role: "user", Content: fmt.Sprintf("opening %d", i), Timestamp: time.Now().UTC(),
		}); err != nil {
			b.Fatalf("AppendSessionMessage %d: %v", i, err)
		}
	}
	return db, NewStoreAdapter(db, ownerID)
}

// BenchmarkListWebSessions_PerSessionOld reproduces the pre-optimization
// path: for every session meta, BuildWebSession issues its own message
// query (1 + N queries).
func BenchmarkListWebSessions_PerSessionOld(b *testing.B) {
	const (
		ownerID = "bench_owner"
		agentID = "bench_agent"
		n       = 200
	)
	db, adapter := benchSeed(b, ownerID, agentID, n)
	_ = db
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		metas, err := adapter.st.ListSessions(ctx, adapter.userID, agentID)
		if err != nil {
			b.Fatalf("ListSessions: %v", err)
		}
		for _, m := range metas {
			_ = adapter.BuildWebSession(ctx, m)
		}
	}
}

// BenchmarkListWebSessions_BatchNew measures the optimized batch path
// (1 meta query + 1 preview query per distinct owner).
func BenchmarkListWebSessions_BatchNew(b *testing.B) {
	const (
		ownerID = "bench_owner"
		agentID = "bench_agent"
		n       = 200
	)
	_, adapter := benchSeed(b, ownerID, agentID, n)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := adapter.ListWebSessions(ctx, agentID); err != nil {
			b.Fatalf("ListWebSessions: %v", err)
		}
	}
}
