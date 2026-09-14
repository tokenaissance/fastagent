package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// streamProvider scripts the streaming path HandleMessage actually uses: the
// first round asks for one tool, later rounds answer in text — and, like a real
// provider, it refuses to serve a round whose context is already done.
type streamProvider struct {
	mu         sync.Mutex
	attempts   int
	successful int
}

func (p *streamProvider) script() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts++
	return p.attempts, nil
}

func (p *streamProvider) Chat(ctx context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	round, _ := p.script()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.successful++
	p.mu.Unlock()
	if round == 1 {
		return &provider.Response{ToolCalls: []provider.ToolCall{{
			ID: "call_slow", Type: "function",
			Function: provider.FunctionCall{Name: "slow_tool", Arguments: "{}"},
		}}}, nil
	}
	return &provider.Response{Content: "done"}, nil
}

func (p *streamProvider) ChatStream(ctx context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	round, _ := p.script()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.successful++
	p.mu.Unlock()

	ch := make(chan provider.StreamChunk, 2)
	if round == 1 {
		ch <- provider.StreamChunk{ToolCalls: []provider.ToolCall{{
			ID: "call_slow", Type: "function",
			Function: provider.FunctionCall{Name: "slow_tool", Arguments: "{}"},
		}}, Done: true}
	} else {
		ch <- provider.StreamChunk{Content: "done", Done: true}
	}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

// When a turn's budget expires mid-tool, the tool still gets its grace window
// to produce a real result, and the turn then stops (the next model round is
// refused because the turn's own context is done). Before the grace existed
// the tool was aborted together with the turn and the round recorded nothing,
// so the call stayed open and the projection had to answer it with a synthetic
// "stopped" reply — the same shape whose persisted form collided with a late
// real result in the 2026-09-13 incident (docs/session-turn-integrity.md, P5).
func TestTurnBudgetExpiryLetsInFlightToolRecordItsResult(t *testing.T) {
	a, _ := newGateAgent(t)
	// Budget expires quickly, the tool finishes well inside a generous grace:
	// the point is the contract, so the margins absorb a loaded machine.
	a.toolGrace = 30 * time.Second
	prov := &streamProvider{}
	a.provider = prov
	a.registry.Register("slow_tool", "test tool", nil, func(ctx context.Context, _ json.RawMessage) (string, error) {
		select {
		case <-time.After(800 * time.Millisecond):
			return "real tool result", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})

	msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-budget", Text: "go"}
	turnCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	a.HandleMessage(turnCtx, msg)

	msgs := a.sessions.Get(sessionTriple(msg, msg.ProjectID)).GetMessages()
	var toolContent string
	for _, m := range msgs {
		if m.Role == "tool" {
			toolContent = m.Content
		}
		if m.Content == provider.StoppedToolResult {
			t.Fatalf("a synthetic pad was written despite the grace window: %+v", msgs)
		}
	}
	if toolContent != "real tool result" {
		t.Fatalf("tool reply = %q; want the tool's own result (grace should have let it finish)", toolContent)
	}

	prov.mu.Lock()
	successful := prov.successful
	prov.mu.Unlock()
	if successful != 1 {
		t.Fatalf("model rounds served = %d; want 1 (the expired budget must stop the next round)", successful)
	}
}
