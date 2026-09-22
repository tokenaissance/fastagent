package gateway

// The settings row → agent provider half of piiScrubbing.
//
// The agent-side witness (internal/agent/pii_scrub_cloudpath_e2e_test.go) proves
// that a Manager built with WithPrivacy redacts every model call. This one
// proves the link above it: that the row an operator writes
// (namespace "privacy", agent/user scope) actually becomes that option. Without
// it the row is exactly what it was before this change — writable in the admin
// UI, readable back, and read by nobody, because the redaction used to live in
// agent.NewAgentWithFullCfg, a constructor no production path calls.
//
// Cloud zero-impact rationale: no endpoint, auth rule or wire shape changes.
// This is the same room the model-precedence test lives in: a settings row that
// silently does nothing is the failure this pins.

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

const piiProbeEmail = "casey.rivera@example.com"

type gatewayPIIProbeProvider struct {
	mu   sync.Mutex
	seen []string
}

func (p *gatewayPIIProbeProvider) record(msgs []provider.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range msgs {
		p.seen = append(p.seen, m.Role+"|"+m.Content)
	}
}

func (p *gatewayPIIProbeProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	p.record(msgs)
	return &provider.Response{Content: "ok"}, nil
}

func (p *gatewayPIIProbeProvider) ChatStream(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	p.record(msgs)
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: "ok", Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func (p *gatewayPIIProbeProvider) raw() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, s := range p.seen {
		if strings.Contains(s, piiProbeEmail) {
			out = append(out, s)
		}
	}
	return out
}

func (p *gatewayPIIProbeProvider) sawPlaceholder() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.seen {
		if strings.Contains(s, "[EMAIL]") {
			return true
		}
	}
	return false
}

func TestThePiiScrubbingRowReachesEveryAgentProvider(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// What the settings page writes when an operator turns PII scrubbing on.
	if err := scope.SaveSetting(ctx, db, "u_owner", "", "privacy", map[string]interface{}{
		"piiScrubbing": map[string]interface{}{"enabled": true},
	}); err != nil {
		t.Fatalf("save settings row: %v", err)
	}

	cfg, err := assembleConfig(ctx, db, "u_owner", "")
	if err != nil {
		t.Fatalf("assemble config: %v", err)
	}
	if !cfg.Privacy.PIIScrubbing.Enabled {
		t.Fatalf("assembleConfig did not read privacy.piiScrubbing.enabled back into cfg.Privacy")
	}

	home := t.TempDir()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	rc := config.ResolvedAgent{
		ID: "agt_pii", UserID: "u_owner", Home: home,
		Workspace: home + "/workspace", Model: "fake-model",
		MaxTokens: 128, Temperature: 0.7, MaxToolIterations: 2,
	}

	prov := &gatewayPIIProbeProvider{}
	mgr, err := agent.NewManager([]config.ResolvedAgent{rc}, prov, bus.New(),
		managerOptions(cfg, "u_owner", db, nil, nil, nil, nil)...)
	if err != nil {
		t.Fatalf("agent manager: %v", err)
	}
	ag := mgr.AgentByID("agt_pii")
	if ag == nil {
		t.Fatal("agent not built")
	}

	ag.HandleMessage(ctx, bus.InboundMessage{
		Channel: "web", ChatID: "c1", UserID: "u_owner",
		Text: "my mail is " + piiProbeEmail,
	})

	if got := prov.raw(); len(got) > 0 {
		t.Errorf("with the row on, the agent still handed the provider raw text: %q", got[0])
	}
	if !prov.sawPlaceholder() {
		t.Error("no [EMAIL] reached the provider — either the call never happened or the assertion above is vacuous")
	}
}
