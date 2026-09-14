package setup

// End-to-end for the auto-continuation change, driven through the real chat
// endpoint: agent runtime + tool registry + session + SSE, with only the model
// faked. The unit tests in internal/agent pin the loop's decision; this pins
// what the user actually sees — the answer, and whether the "partial answer"
// badge shows up on a turn that ran out of tool-call rounds.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/api"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// capScriptProvider keeps asking for a tool until it has used up `toolRounds`,
// then answers. The synthesis call that ends a capped turn carries no tools, so
// "no tools available" is also a final answer — which keeps both the continued
// and the capped path deterministic without counting calls from the loop side.
type capScriptProvider struct {
	toolName   string
	toolRounds int
	rounds     atomic.Int64
}

func (p *capScriptProvider) response(msgs []provider.Message, tools []provider.Tool) *provider.Response {
	if len(tools) == 0 {
		return &provider.Response{Content: "final answer without tools"}
	}
	n := p.rounds.Load()
	if p.toolRounds > 0 && int(n) >= p.toolRounds {
		return &provider.Response{Content: "delivered after the extension"}
	}
	p.rounds.Add(1)
	return &provider.Response{ToolCalls: []provider.ToolCall{{
		ID:   fmt.Sprintf("call_%d", n+1),
		Type: "function",
		Function: provider.FunctionCall{
			Name:      p.toolName,
			Arguments: fmt.Sprintf(`{"n":"%d"}`, n+1),
		},
	}}}
}

func (p *capScriptProvider) Chat(_ context.Context, msgs []provider.Message, tools []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	return p.response(msgs, tools), nil
}

func (p *capScriptProvider) ChatStream(_ context.Context, msgs []provider.Message, tools []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	resp := p.response(msgs, tools)
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: resp.Content, ToolCalls: resp.ToolCalls, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

// newContinuesHarness builds the server + agent pair with an explicit
// continuation budget, which the shared newChatHarness helper does not expose.
func newContinuesHarness(t *testing.T, prov provider.Provider, iterations, continues int) (*Server, *agent.Agent) {
	t.Helper()
	home := t.TempDir()
	rc := config.ResolvedAgent{
		ID: "agt_e2e", UserID: "u_1", Home: home,
		Workspace: filepath.Join(home, "workspace"), Model: "fake-model",
		MaxTokens: 128, Temperature: 0.7,
		MaxToolIterations:         iterations,
		MaxToolIterationContinues: continues,
	}
	mgr, err := agent.NewManager([]config.ResolvedAgent{rc}, prov, bus.New(), agent.WithUserID("u_1"))
	if err != nil {
		t.Fatalf("agent manager: %v", err)
	}
	ag := mgr.AgentByID("agt_e2e")
	if ag == nil {
		t.Fatal("agent not registered")
	}
	s := &Server{userResolver: e2eResolver{space: &api.UserSpaceView{UserID: "u_1", Agents: mgr}, agents: mgr}}
	return s, ag
}

// countProbe registers a tool that always succeeds and counts its invocations —
// the evidence that a segment really did produce results.
func countProbe(t *testing.T, ag *agent.Agent, name string) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	ag.ToolRegistry().Register(name, "counting probe", nil, func(context.Context, json.RawMessage) (string, error) {
		calls.Add(1)
		return "probe ok", nil
	})
	return &calls
}

func capBadgeInHistory(msgs []provider.Message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		v, ok := msgs[i].Metadata["iterationCapReached"].(bool)
		return ok && v
	}
	return false
}

func runSingleChatStream(t *testing.T, s *Server, chatID, message string) string {
	t.Helper()
	rec := newSSERecorder()
	req := chatStreamRequest(t, chatRequest{
		AgentID: "agt_e2e", SessionID: chatID, Message: message, TurnID: "turn-" + chatID,
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleChatStream(rec, req)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("chat stream never finished; body=%q", rec.body.String())
	}
	return rec.body.String()
}

func TestIterationCapAutoContinuesAcrossTheRealChatPath(t *testing.T) {
	// Two rounds per segment: the first segment burns them both and earns one
	// extension, which the model then uses to answer.
	prov := &capScriptProvider{toolName: "count_probe", toolRounds: 2}
	s, ag := newContinuesHarness(t, prov, 2, 1)
	calls := countProbe(t, ag, "count_probe")

	body := runSingleChatStream(t, s, "chat-continue", "do the long thing")

	if !strings.Contains(body, "delivered after the extension") {
		t.Fatalf("the turn did not deliver the extension's answer; stream=%q", body)
	}
	if strings.Contains(body, "iterationCapReached") {
		t.Fatalf("a turn that finished after its extension was still badged as partial; stream=%q", body)
	}
	sess := ag.Sessions().Get("web", "", "chat-continue", "")
	if sess == nil {
		t.Fatal("session not found")
	}
	if capBadgeInHistory(sess.GetMessages()) {
		t.Fatal("history carries the partial-answer badge after a successful continuation")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("tool ran %d times, want 2 (one per segment)", got)
	}
}

func TestIterationCapStillBadgesWhenContinuationIsOff(t *testing.T) {
	prov := &capScriptProvider{toolName: "count_probe", toolRounds: 5}
	s, ag := newContinuesHarness(t, prov, 1, 0)
	calls := countProbe(t, ag, "count_probe")

	body := runSingleChatStream(t, s, "chat-capped", "do the long thing")

	// With continuations off, the turn stops after its one round and answers
	// with the synthesis call (which sees no tools).
	if !strings.Contains(body, "final answer without tools") {
		t.Fatalf("capped turn did not deliver the synthesis answer; stream=%q", body)
	}
	if !strings.Contains(body, "iterationCapReached") {
		t.Fatalf("capped turn must keep the partial-answer badge; stream=%q", body)
	}
	sess := ag.Sessions().Get("web", "", "chat-capped", "")
	if sess == nil {
		t.Fatal("session not found")
	}
	if !capBadgeInHistory(sess.GetMessages()) {
		t.Fatal("history lost the badge the UI re-renders from")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("tool ran %d times, want 1 — no extension may happen with continues=0", got)
	}
}

func TestIterationCapDoesNotExtendAStuckTurnE2E(t *testing.T) {
	// The provider asks for a tool that does not exist: every round fails, so
	// the turn must not buy itself another segment.
	prov := &capScriptProvider{toolName: "no_such_tool", toolRounds: 5}
	s, _ := newContinuesHarness(t, prov, 1, 2)

	body := runSingleChatStream(t, s, "chat-stuck", "keep trying")

	if !strings.Contains(body, "final answer without tools") {
		t.Fatalf("stuck turn did not reach synthesis; stream=%q", body)
	}
	if !strings.Contains(body, "iterationCapReached") {
		t.Fatalf("stuck turn must be badged; stream=%q", body)
	}
	// One round only: two continuations were available, neither was earned.
	if got := prov.rounds.Load(); got != 1 {
		t.Fatalf("provider tool rounds = %d, want 1 (no extension for a failing segment)", got)
	}
}
