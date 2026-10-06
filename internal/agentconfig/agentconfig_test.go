package agentconfig

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// fakeStore is the port's test adapter: it answers a counter and a resolved
// agent, and it counts resolves so a test can tell a cache hit from a rebuild.
type fakeStore struct {
	mu       sync.Mutex
	version  Version
	servers  map[string]config.MCPServerConfig
	resolves int
	// versionReads lets a test make the counter move between the two reads of
	// one read-repair attempt (the torn-pair case).
	versionReads []Version
	// resolveDelay makes a rebuild slow, so a test can tell the check metric
	// from the rebuild metric.
	resolveDelay time.Duration
}

func (f *fakeStore) CurrentVersion(context.Context) (Version, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.versionReads) > 0 {
		v := f.versionReads[0]
		f.versionReads = f.versionReads[1:]
		return v, nil
	}
	return f.version, nil
}

func (f *fakeStore) Resolve(context.Context, Scope) (config.ResolvedAgent, error) {
	f.mu.Lock()
	delay := f.resolveDelay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolves++
	servers := map[string]config.MCPServerConfig{}
	for k, v := range f.servers {
		servers[k] = v
	}
	return config.ResolvedAgent{ID: "agt_1", UserID: "u_1", MCPServers: servers}, nil
}

func (f *fakeStore) write(version Version, servers map[string]config.MCPServerConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version = version
	f.servers = servers
}

func (f *fakeStore) resolveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolves
}

// I2 (read-your-writes): after the counter moves, the next read returns the new
// content. This is the incident's shape — a write and a read in the same session.
//
// Falsification: change the memo comparison in For from `version == before` to
// `true` (always trust the memo) and this test goes red on the second read.
func TestResolveServesFreshConfigAfterVersionBump(t *testing.T) {
	store := &fakeStore{}
	store.write(1, map[string]config.MCPServerConfig{})
	uc, err := New(store, NewMemoryMemo(0))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	scope := Scope{UserID: "u_1", AgentID: "agt_1"}

	cfg, err := uc.For(ctx, scope)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if len(cfg.MCPServers) != 0 {
		t.Fatalf("first read sees %d servers; want 0", len(cfg.MCPServers))
	}

	// The writer adds a server, then bumps the counter (one logical write).
	store.write(2, map[string]config.MCPServerConfig{
		"notion": {Type: "http", URL: "https://mcp.notion.example/mcp", OAuthResource: "https://mcp.notion.example/mcp"},
	})

	cfg, err = uc.For(ctx, scope)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if _, ok := cfg.MCPServers["notion"]; !ok {
		t.Fatalf("second read still misses the server added before it: %+v", cfg.MCPServers)
	}
	stats := uc.Stats()
	if stats.Checks != 2 || stats.Rebuilds != 2 || stats.Hits != 0 {
		t.Fatalf("stats = %+v; want 2 checks, 2 rebuilds, 0 hits", stats)
	}
}

// A read that finds an unchanged counter serves the memo and does not resolve.
func TestResolveServesFromMemoWithoutRebuild(t *testing.T) {
	store := &fakeStore{}
	store.write(7, map[string]config.MCPServerConfig{})
	uc, _ := New(store, NewMemoryMemo(0))
	ctx := context.Background()
	scope := Scope{UserID: "u_1", AgentID: "agt_1"}

	if _, err := uc.For(ctx, scope); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if _, err := uc.For(ctx, scope); err != nil {
		t.Fatalf("second read: %v", err)
	}
	if got := store.resolveCount(); got != 1 {
		t.Fatalf("resolves = %d; want 1 (the second read must hit the memo)", got)
	}
	if stats := uc.Stats(); stats.Hits != 1 {
		t.Fatalf("stats = %+v; want 1 hit", stats)
	}
}

// I4 (no torn pair): the counter moves between the two reads of one attempt, so
// the content may mix two versions. The use case must not cache it; it retries
// and caches the pair it can prove.
func TestResolveDoesNotCacheATornPair(t *testing.T) {
	store := &fakeStore{}
	store.write(2, map[string]config.MCPServerConfig{"notion": {Type: "http", URL: "https://mcp.notion.example/mcp"}})
	// First attempt: before = 1, resolve returns the CURRENT (v2) content,
	// after = 2 ⇒ mismatch ⇒ retry. Second attempt: 2, 2 ⇒ cached.
	store.versionReads = []Version{1, 2, 2, 2}
	memo := NewMemoryMemo(0)
	uc, _ := New(store, memo)
	ctx := context.Background()
	scope := Scope{UserID: "u_1", AgentID: "agt_1"}

	cfg, err := uc.For(ctx, scope)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, ok := cfg.MCPServers["notion"]; !ok {
		t.Fatalf("read did not return the server: %+v", cfg.MCPServers)
	}
	if _, version, ok := memo.Get(scope); !ok || version != 2 {
		t.Fatalf("memo entry = (ok=%v, version=%d); want (true, 2)", ok, version)
	}
	if stats := uc.Stats(); stats.Checks != 2 || stats.Rebuilds != 1 {
		t.Fatalf("stats = %+v; want 2 checks and 1 rebuild (the first attempt was discarded)", stats)
	}
}

// A counter that never settles returns ErrConfigChurn, not a value of unknown
// age. I3: no undeclared stale serve.
func TestResolveRefusesToServeWhenTheCounterNeverSettles(t *testing.T) {
	store := &fakeStore{}
	store.versionReads = []Version{1, 2, 3, 4, 5, 6}
	uc, _ := New(store, NewMemoryMemo(0))
	if _, err := uc.For(context.Background(), Scope{UserID: "u_1", AgentID: "agt_1"}); !errors.Is(err, ErrConfigChurn) {
		t.Fatalf("err = %v; want ErrConfigChurn", err)
	}
	if stats := uc.Stats(); stats.Churn != 1 {
		t.Fatalf("stats = %+v; want 1 churn", stats)
	}
}

// Concurrent misses on one scope resolve once (single flight), and the second
// caller reads the entry the first filled.
func TestResolveSingleFlightsOneRebuildPerScope(t *testing.T) {
	store := &fakeStore{}
	store.write(3, map[string]config.MCPServerConfig{})
	uc, _ := New(store, NewMemoryMemo(0))
	ctx := context.Background()
	scope := Scope{UserID: "u_1", AgentID: "agt_1"}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := uc.For(ctx, scope); err != nil {
				t.Errorf("concurrent read: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := store.resolveCount(); got != 1 {
		t.Fatalf("resolves = %d; want 1 for 8 concurrent reads", got)
	}
}
