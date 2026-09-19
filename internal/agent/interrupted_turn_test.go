package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/doctor"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// loopingStreamProvider always asks for the same tool call (fresh id each
// round) on the STREAMING path HandleMessage uses. Same name+arguments three
// rounds in a row trips the loop detector, so the third round is never
// executed and its call stays unanswered.
type loopingStreamProvider struct {
	mu    sync.Mutex
	round int
}

func (p *loopingStreamProvider) next() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.round++
	return p.round
}

func (p *loopingStreamProvider) call() provider.ToolCall {
	return provider.ToolCall{
		ID: fmt.Sprintf("call_probe_%d", p.next()), Type: "function",
		Function: provider.FunctionCall{Name: "probe", Arguments: `{"q":"same"}`},
	}
}

func (p *loopingStreamProvider) Chat(ctx context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &provider.Response{ToolCalls: []provider.ToolCall{p.call()}}, nil
}

func (p *loopingStreamProvider) ChatStream(ctx context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ch := make(chan provider.StreamChunk, 2)
	ch <- provider.StreamChunk{ToolCalls: []provider.ToolCall{p.call()}, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

// A turn can exit with a tool_use that never got its reply: the client clicked
// Stop, the budget expired while the tool was in flight, or — as here — the
// loop detector broke out before executing the round.
//
// That call stays OPEN in the session. History records what happened; the next
// request is made valid by normalizeForPrompt, which fills exactly one
// synthetic reply at prompt-build time. Persisting that reply instead is what
// collided with a late real result in the 2026-09-13 incident, so the pad was
// removed (docs/session-turn-integrity.md, Q4): the session must contain the
// unanswered call and no synthetic reply, and the projection must be clean.
func TestInterruptedTurnLeavesNoSyntheticReplyInHistory(t *testing.T) {
	a, _ := newGateAgent(t)
	a.maxToolIterations = 5
	// The same call every round trips loop detection on the third one, so that
	// round's tool never runs and its call stays unanswered.
	a.provider = &loopingStreamProvider{}
	a.registry.Register("probe", "test tool", nil, func(context.Context, json.RawMessage) (string, error) {
		return "probe result", nil
	})

	msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-interrupted", Text: "go"}
	a.HandleMessage(context.Background(), msg)

	msgs := a.sessions.Get(sessionTriple(msg, msg.ProjectID)).GetMessages()

	// 1. No synthetic reply was written into history.
	for i, m := range msgs {
		for _, pad := range provider.SyntheticToolPads {
			if m.Content == pad {
				t.Fatalf("history carries a persisted synthetic reply at %d: %+v", i, m)
			}
		}
	}

	// 2. The interrupted call really is open (otherwise this test proves
	// nothing): at least one assistant round has no reply for its call.
	answered := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}
	openCalls := 0
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.EffectiveToolCalls() {
			if !answered[tc.ID] {
				openCalls++
			}
		}
	}
	if openCalls == 0 {
		t.Fatal("no unanswered call in history; the interrupted-turn path was not exercised")
	}

	// 3. The prompt projection is still valid — the doctor scanner is the
	// oracle, so this asserts the same contract the provider depends on.
	if findings := doctor.Scan(normalizeForPrompt(msgs)); len(findings) != 0 {
		t.Fatalf("projected prompt violates the pairing contract: %+v", findings)
	}
}
