package gateway

// The delivery-point half of the `memory` namespace.
//
// internal/agent/memory_autopersist_row_test.go pins the precedence (the row is
// the default layer, the per-agent flag wins over it, and the pass the switch
// turns on really writes MEMORY.md / USER.md).
//
// This one pins the link above it: that the row an operator writes — namespace
// "memory", the only surface for it (no dashboard renders autoPersist's cadence
// or model) — reaches the option, so a real turn's post-turn pass actually
// fires. The negative half (no row, no firing) is what makes the positive half
// mean something rather than pass on some other default.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// memoryDistillProvider counts the auto-persist extraction calls: recognisable
// by the prompt's closing instruction, which no ordinary turn carries.
type memoryDistillProvider struct {
	mu       sync.Mutex
	distills int
}

func (p *memoryDistillProvider) respond(msgs []provider.Message) *provider.Response {
	for _, m := range msgs {
		if strings.Contains(m.Content, "Output JSON only") {
			p.mu.Lock()
			p.distills++
			p.mu.Unlock()
			return &provider.Response{Content: `{"memory_facts":["the release goes out on fridays"],"user_notes":[]}`}
		}
	}
	return &provider.Response{Content: "done"}
}

func (p *memoryDistillProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	return p.respond(msgs), nil
}

func (p *memoryDistillProvider) ChatStream(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	resp := p.respond(msgs)
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: resp.Content, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func (p *memoryDistillProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.distills
}

// distillsAfterOneTurn builds a fresh user space, optionally seeds the row, runs
// one real turn and reports how many distill passes fired. everyNTurns=1 is what
// makes a single turn enough to reach the cadence.
func distillsAfterOneTurn(t *testing.T, rowOn bool) (int, config.MemoryCfg) {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	t.Setenv("FASTAGENT_HOME", base)

	// A file-backed store per case: the shared in-memory DSN would let the
	// positive case's row leak into the negative one.
	db, err := store.NewDBStore("sqlite", filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if rowOn {
		if err := scope.SaveSetting(ctx, db, "u_owner", "", "memory", map[string]interface{}{
			"autoPersist": map[string]interface{}{"enabled": true, "everyNTurns": 1},
		}); err != nil {
			t.Fatalf("save settings row: %v", err)
		}
	}

	cfg, err := assembleConfig(ctx, db, "u_owner", "")
	if err != nil {
		t.Fatalf("assemble config: %v", err)
	}

	home := filepath.Join(base, "agents", "agt_ap", "agent")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	rc := config.ResolvedAgent{
		ID: "agt_ap", UserID: "u_owner", Home: home,
		Workspace: filepath.Join(home, "workspace"), Model: "fake-model",
		MaxTokens: 128, Temperature: 0.7, MaxToolIterations: 2,
	}
	prov := &memoryDistillProvider{}
	mgr, err := agent.NewManager([]config.ResolvedAgent{rc}, prov, bus.New(),
		managerOptions(cfg, "u_owner", db, nil, nil, nil, nil)...)
	if err != nil {
		t.Fatalf("agent manager: %v", err)
	}
	ag := mgr.AgentByID(rc.ID)
	if ag == nil {
		t.Fatal("agent not built")
	}

	ag.HandleMessage(ctx, bus.InboundMessage{
		Channel: "web", ChatID: "c1", UserID: "u_owner",
		Text: "remember that we ship on fridays",
	})

	if !rowOn {
		// The pass is asynchronous, so give it a window before concluding it
		// never ran (the gate already decided synchronously inside the turn).
		time.Sleep(500 * time.Millisecond)
		return prov.count(), cfg.Memory
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && prov.count() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	return prov.count(), cfg.Memory
}

func TestTheMemoryRowIsWhatTurnsAutoPersistOn(t *testing.T) {
	fired, cfg := distillsAfterOneTurn(t, true)
	if !cfg.AutoPersist.Enabled {
		t.Fatalf("assembleConfig did not read memory.autoPersist.enabled back into cfg.Memory")
	}
	if fired == 0 {
		t.Fatalf("the memory row reached no reader: a real turn fired no auto-persist pass (cfg.EveryNTurns=%d)", cfg.AutoPersist.EveryNTurns)
	}

	if fired, _ := distillsAfterOneTurn(t, false); fired != 0 {
		t.Errorf("with no memory row the pass fired %d time(s) — the row is supposed to be what turns it on", fired)
	}
}
