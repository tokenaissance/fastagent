package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// The producers (sandbox exec, host exec, docker) each clip at the source. This
// test covers the hole that is left when a tool bypasses them — a plugin tool,
// an MCP server, a future call path — by driving a 300 KB result through the
// real ReAct loop and reading it back out of session history.
//
// The message that lands in the session is the one the model re-sends every
// later round of the turn, so that is the only place worth asserting on
// (2026-09-14 OOM: 73 MB of that text cost two pods).

// bigResultProvider emits one big_probe call, then answers. Both Chat and
// ChatStream are implemented because the non-streaming entry point is also a
// supported path.
type bigResultProvider struct {
	mu    sync.Mutex
	round int
}

func (p *bigResultProvider) call() provider.ToolCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.round++
	if p.round == 1 {
		return provider.ToolCall{
			ID: "call_big_1", Type: "function",
			Function: provider.FunctionCall{Name: "big_probe", Arguments: `{}`},
		}
	}
	return provider.ToolCall{}
}

func (p *bigResultProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	if tc := p.call(); tc.ID != "" {
		return &provider.Response{ToolCalls: []provider.ToolCall{tc}}, nil
	}
	return &provider.Response{Content: "done"}, nil
}

func (p *bigResultProvider) ChatStream(ctx context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	resp, err := p.Chat(ctx, nil, nil, "", 0, 0)
	if err != nil {
		return nil, err
	}
	ch := make(chan provider.StreamChunk, 2)
	ch <- provider.StreamChunk{Content: resp.Content, ToolCalls: resp.ToolCalls, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func TestToolResultIsClippedBeforeItReachesHistory(t *testing.T) {
	a, _ := newGateAgent(t)
	a.maxToolIterations = 4
	a.provider = &bigResultProvider{}

	// "HEAD-START" then 300 KB of filler then a tail line, so both ends are
	// recognisable in the stored message.
	payload := "HEAD-START\n" + strings.Repeat("filler line that a command printed\n", 9000) + "TAIL-END\n"
	a.registry.Register("big_probe", "test tool", nil, func(context.Context, json.RawMessage) (string, error) {
		return payload, nil
	})

	msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-bigresult", Text: "go"}
	a.HandleMessage(context.Background(), msg)

	var toolContent string
	for _, m := range a.sessions.Get(sessionTriple(msg, msg.ProjectID)).GetMessages() {
		if m.Role == "tool" && m.Name == "big_probe" {
			toolContent = m.Content
		}
	}
	if toolContent == "" {
		t.Fatal("the big_probe result never reached session history")
	}
	if !strings.Contains(toolContent, "of output omitted") {
		t.Fatalf("%d bytes of tool output were stored unclipped", len(toolContent))
	}
	if len(toolContent) > sandbox.OutputHeadCap+sandbox.OutputTailCap+400 {
		t.Fatalf("stored tool result is %d bytes — the cap must bound what the next request carries", len(toolContent))
	}
	if !strings.HasPrefix(toolContent, "HEAD-START\n") {
		t.Error("the head of the output must survive so the model knows what ran")
	}
	if !strings.HasSuffix(toolContent, "TAIL-END\n") {
		t.Error("the tail must survive — that is where the exit summary lives")
	}
}
