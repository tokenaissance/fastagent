package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// threeCallProvider emits three tool calls on the first round and records the
// messages the model sees on the second round. It is the fixture for the
// per-round parallel cap, which both the streaming and non-streaming loops must
// apply identically.
type threeCallProvider struct {
	mu     sync.Mutex
	round  int
	second []provider.Message
}

func (p *threeCallProvider) advance() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.round++
	return p.round
}

func (p *threeCallProvider) record(messages []provider.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.second = append([]provider.Message(nil), messages...)
}

func (p *threeCallProvider) calls() []provider.ToolCall {
	names := []string{"tool_a", "tool_b", "tool_c"}
	calls := make([]provider.ToolCall, 0, len(names))
	for _, name := range names {
		calls = append(calls, provider.ToolCall{
			ID:       "call_" + name,
			Type:     "function",
			Function: provider.FunctionCall{Name: name, Arguments: "{}"},
		})
	}
	return calls
}

func (p *threeCallProvider) Chat(_ context.Context, messages []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	if p.advance() == 1 {
		return &provider.Response{ToolCalls: p.calls()}, nil
	}
	p.record(messages)
	return &provider.Response{Content: "done"}, nil
}

func (p *threeCallProvider) ChatStream(_ context.Context, messages []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	ch := make(chan provider.StreamChunk, 2)
	if p.advance() == 1 {
		ch <- provider.StreamChunk{ToolCalls: p.calls(), Done: true}
	} else {
		p.record(messages)
		ch <- provider.StreamChunk{Content: "done", Done: true}
	}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func TestToolRoundParallelCapIsTheSameForStreamingAndNonStreaming(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Agent, bus.InboundMessage)
	}{
		{"HandleMessage", func(a *Agent, msg bus.InboundMessage) {
			_ = a.HandleMessage(context.Background(), msg)
		}},
		{"HandleMessageStream", func(a *Agent, msg bus.InboundMessage) {
			drainStream(a.HandleMessageStream(context.Background(), msg))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newGateAgent(t)
			var executed int32
			for _, name := range []string{"tool_a", "tool_b", "tool_c"} {
				a.registry.Register(name, "test tool", nil, func(context.Context, json.RawMessage) (string, error) {
					atomic.AddInt32(&executed, 1)
					return "real result", nil
				})
			}
			prov := &threeCallProvider{}
			a.provider = prov
			a.publishRuntimeFromFields()
			a.mutateRuntime(func(rt *agentRuntime) { rt.maxParallelToolCalls = 1 })
			tc.run(a, bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-cap", Text: "go"})

			if got := atomic.LoadInt32(&executed); got != 1 {
				t.Fatalf("executed tools = %d, want 1 (the parallel cap must be applied before execution)", got)
			}

			prov.mu.Lock()
			messages := append([]provider.Message(nil), prov.second...)
			prov.mu.Unlock()

			var toolMessages []provider.Message
			for _, m := range messages {
				if m.Role == "tool" {
					toolMessages = append(toolMessages, m)
				}
			}
			if len(toolMessages) != 3 {
				t.Fatalf("tool messages = %d, want 3 (every call needs a paired result)", len(toolMessages))
			}
			deferred := 0
			for _, m := range toolMessages {
				if strings.Contains(m.Content, "Deferred —") {
					deferred++
				}
			}
			if deferred != 2 {
				t.Fatalf("deferred results = %d, want 2 (cap 1 of 3 calls)", deferred)
			}
		})
	}
}
