package agent

// piiScrubbing: the switch, and every delivery point that has to read it.
//
// Cloud zero-impact rationale: nothing about an endpoint, an auth rule or a
// wire shape changes. The setting `privacy.piiScrubbing.enabled` already exists
// as an admin row; this file pins that turning it on actually redacts what the
// agent sends a model provider — on the web chat turn, on the OpenAI-compatible
// /v1 turn with `stream: true`, inside delegate_task's own ReAct loop, in
// compaction's summarizer and in the auto-persist extractor.
//
// The measurement that opened this (2026-09-22, before the change, with the
// switch forced on at the call site): HandleMessage redacted, while
// HandleMessageStream handed the provider "my card is 4111 1111 1111 1111 and
// mail casey.rivera@example.com" verbatim, and RunSubagent did the same. The
// switch's only reader was NewAgentWithFullCfg — a constructor with no callers,
// so in production it was off no matter what the row said.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/privacy"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

const piiEmail = "casey.rivera@example.com"
const piiCard = "4111 1111 1111 1111"

// piiProbeProvider is the delivery point: it records every field of every
// message the agent hands it, so "did this leave the process raw?" is answered
// by what the provider saw rather than by reading the loop.
type piiProbeProvider struct {
	mu   sync.Mutex
	seen []string
}

func (p *piiProbeProvider) record(msgs []provider.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range msgs {
		p.seen = append(p.seen, m.Role+"|"+m.Content)
		if m.Thinking != "" {
			p.seen = append(p.seen, "thinking|"+m.Thinking)
		}
		for _, tc := range m.ToolCalls {
			p.seen = append(p.seen, "toolargs|"+tc.Function.Arguments)
		}
		for _, part := range m.ContentParts {
			p.seen = append(p.seen, "part|"+part.Text)
		}
	}
}

func (p *piiProbeProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	p.record(msgs)
	return &provider.Response{Content: "ok"}, nil
}

func (p *piiProbeProvider) ChatStream(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	p.record(msgs)
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: "ok", Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

// raw finds what leaked. "Raw" is the two planted values, not a pattern: the
// redacted form must be present somewhere, so a test that never reached a
// provider cannot pass by silence.
func (p *piiProbeProvider) raw() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, s := range p.seen {
		if strings.Contains(s, piiEmail) || strings.Contains(s, piiCard) {
			out = append(out, s)
		}
	}
	return out
}

func (p *piiProbeProvider) sawEmailPlaceholder() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.seen {
		if strings.Contains(s, "[EMAIL]") {
			return true
		}
	}
	return false
}

func (p *piiProbeProvider) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = nil
}

func piiManager(t *testing.T, prov provider.Provider, enabled bool) *Manager {
	t.Helper()
	home := t.TempDir()
	rc := config.ResolvedAgent{
		ID: "agt_pii", UserID: "u_1", Home: home,
		Workspace: home + "/workspace", Model: "fake-model",
		MaxTokens: 128, Temperature: 0.7, MaxToolIterations: 2,
	}
	mgr, err := NewManager([]config.ResolvedAgent{rc}, prov, bus.New(),
		WithUserID("u_1"),
		WithPrivacy(config.PrivacyCfg{PIIScrubbing: config.PIIScrubCfg{Enabled: enabled}}),
	)
	if err != nil {
		t.Fatalf("agent manager: %v", err)
	}
	return mgr
}

func drainStream(sr *provider.StreamReader) {
	for {
		if _, more := sr.Next(); !more {
			return
		}
	}
}

func TestTheSwitchRedactsEveryModelCallTheTurnMakes(t *testing.T) {
	prov := &piiProbeProvider{}
	mgr := piiManager(t, prov, true)
	ag := mgr.AgentByID("agt_pii")
	ctx := context.Background()
	text := "my card is " + piiCard + " and mail " + piiEmail

	// 1. The web chat turn (/api/chat/stream → HandleMessage).
	ag.HandleMessage(ctx, bus.InboundMessage{Channel: "web", ChatID: "c1", UserID: "u_1", Text: text})
	if got := prov.raw(); len(got) > 0 {
		t.Errorf("the web turn handed the provider raw text: %q", got[0])
	}
	if !prov.sawEmailPlaceholder() {
		t.Error("the web turn reached the provider with no [EMAIL] anywhere — the assertion above is vacuous")
	}

	// 2. The OpenAI-compatible turn with stream:true (/v1 → HandleMessageStream).
	prov.reset()
	drainStream(ag.HandleMessageStream(ctx, bus.InboundMessage{Channel: "api", ChatID: "c2", UserID: "api-user", Text: text}))
	if got := prov.raw(); len(got) > 0 {
		t.Errorf("/v1 with stream:true handed the provider raw text: %q", got[0])
	}
	if !prov.sawEmailPlaceholder() {
		t.Error("/v1 with stream:true reached the provider with no [EMAIL] anywhere — the assertion above is vacuous")
	}

	// 3. delegate_task's own ReAct loop.
	prov.reset()
	if _, err := ag.RunSubagent(ctx, tools.SubagentRequest{Task: text, MaxIterations: 1}); err != nil {
		t.Fatalf("run subagent: %v", err)
	}
	if got := prov.raw(); len(got) > 0 {
		t.Errorf("the sub-agent loop handed the provider raw text: %q", got[0])
	}

	// 4. Compaction's summarizer. Threshold is token-counted (chars/4), so this
	// is a history that is long as well as >PruneTurnAge messages deep —
	// compressOlderMessages returns early on a short history.
	prov.reset()
	history := []provider.Message{{Role: "user", Content: text}}
	for i := 0; i < 25; i++ {
		history = append(history, provider.Message{Role: "assistant", Content: strings.Repeat("x", 15000)})
	}
	if _, err := CompactMessages(ctx, history, t.TempDir(), ag.provider, ag.model); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if got := prov.raw(); len(got) > 0 {
		t.Errorf("compaction's summarizer handed the provider raw text: %q", got[0])
	}

	// 5. The auto-persist extractor.
	prov.reset()
	mem := NewMemory(t.TempDir())
	AutoPersistMemory(ctx, mem, ag.provider, ag.model, []provider.Message{{Role: "user", Content: text}})
	if got := prov.raw(); len(got) > 0 {
		t.Errorf("the memory extractor handed the provider raw text: %q", got[0])
	}
}

// A provider swap is a second way into the agent. If it bypassed the same
// decision, one settings reload would unship the redaction with every test
// above still green.
func TestTheSwitchSurvivesAProviderHotReload(t *testing.T) {
	prov := &piiProbeProvider{}
	mgr := piiManager(t, &piiProbeProvider{}, true)
	ag := mgr.AgentByID("agt_pii")
	text := "mail " + piiEmail

	mgr.UpdateProvider(prov)
	ag.HandleMessage(context.Background(), bus.InboundMessage{Channel: "web", ChatID: "c1", UserID: "u_1", Text: text})
	if got := prov.raw(); len(got) > 0 {
		t.Errorf("after UpdateProvider the agent handed the provider raw text: %q", got[0])
	}

	prov.reset()
	mgr.UpdateProviderResolved(prov, []config.ResolvedAgent{{ID: "agt_pii", UserID: "u_1", Home: ag.homePath, Model: "fake-model"}})
	ag.HandleMessage(context.Background(), bus.InboundMessage{Channel: "web", ChatID: "c2", UserID: "u_1", Text: text})
	if got := prov.raw(); len(got) > 0 {
		t.Errorf("after UpdateProviderResolved the agent handed the provider raw text: %q", got[0])
	}
}

// The other direction, pinned so the witness above cannot be satisfied by
// redacting unconditionally: with the switch off the model gets what the user
// wrote, including the parts that look like PII. That is the documented meaning
// of an off switch, and a test that only checked the on direction would let a
// "always scrub" regression through.
func TestWithTheSwitchOffTheModelStillGetsTheUsersWords(t *testing.T) {
	prov := &piiProbeProvider{}
	mgr := piiManager(t, prov, false)
	ag := mgr.AgentByID("agt_pii")
	text := "mail " + piiEmail

	ag.HandleMessage(context.Background(), bus.InboundMessage{Channel: "web", ChatID: "c1", UserID: "u_1", Text: text})
	if got := prov.raw(); len(got) == 0 {
		t.Error("with piiScrubbing off the provider should have seen the user's own words, but nothing raw arrived")
	}
}

// The field list is the wire's, not the struct's: these are the places
// provider/openai.go's toAPIMessages copies text out of a Message. RawAssistant
// is the deliberate exception (verbatim replay for the prompt cache and
// DeepSeek thinking mode), pinned here so the limit stays a decision instead of
// drifting into an oversight.
func TestScrubMessagesCoversEveryFieldThatLeavesForTheProvider(t *testing.T) {
	in := provider.Message{
		Role:         "assistant",
		Content:      "mail " + piiEmail,
		Thinking:     "the user's address is " + piiEmail,
		ContentParts: []provider.ContentPart{{Type: "text", Text: "card " + piiCard}},
		ToolCalls: []provider.ToolCall{{
			ID: "call_1", Type: "function",
			Function: provider.FunctionCall{Name: "send_mail", Arguments: `{"to":"` + piiEmail + `"}`},
		}},
		RawAssistant: []byte(`{"role":"assistant","content":"mail ` + piiEmail + `"}`),
	}

	out := privacy.ScrubMessages([]provider.Message{in})[0]

	for _, tc := range []struct {
		field string
		got   string
		want  string
	}{
		{"Content", out.Content, "[EMAIL]"},
		{"Thinking", out.Thinking, "[EMAIL]"},
		{"ContentParts[].Text", out.ContentParts[0].Text, "[CARD]"},
		{"ToolCalls[].Arguments", out.ToolCalls[0].Function.Arguments, "[EMAIL]"},
	} {
		if !strings.Contains(tc.got, tc.want) {
			t.Errorf("%s = %q, want it to contain %s", tc.field, tc.got, tc.want)
		}
		if strings.Contains(tc.got, piiEmail) || strings.Contains(tc.got, piiCard) {
			t.Errorf("%s = %q, still carries the raw value", tc.field, tc.got)
		}
	}

	// The tool's name is not user text; rewriting it would make the replayed
	// call unresolvable at the provider.
	if out.ToolCalls[0].Function.Name != "send_mail" {
		t.Errorf("the tool name was rewritten to %q", out.ToolCalls[0].Function.Name)
	}

	// The documented exception.
	if string(out.RawAssistant) != string(in.RawAssistant) {
		t.Errorf("RawAssistant was rewritten: %q", string(out.RawAssistant))
	}

	// And the session keeps the user's own words: the input slice is not
	// mutated for the caller.
	if in.Content != "mail "+piiEmail {
		t.Errorf("ScrubMessages mutated its input: %q", in.Content)
	}
}
