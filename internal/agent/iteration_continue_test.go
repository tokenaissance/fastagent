package agent

// The iteration cap used to end a turn outright: the model got one synthesizing
// call with tools disabled and the chat UI badged the answer as partial. A turn
// that ran out of rounds *while making progress* now gets an extra segment
// instead — bounded by maxToolContinues, and never granted to a segment that
// only produced failures (another budget would just burn on the same wall).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// iterationScriptedRound is one model response: either a tool call (with distinct
// arguments, so the loop detector stays out of the way) or a final answer.
type iterationScriptedRound struct {
	tool  string
	final string
}

// iterationScriptedProvider plays a fixed list of rounds, counting how many the turn
// asked for. Past the end of the script it answers with the last entry, so an
// unexpected extra call shows up as a wrong call count rather than a hang.
type iterationScriptedProvider struct {
	mu    sync.Mutex
	steps []iterationScriptedRound
	calls int
}

func (p *iterationScriptedProvider) next() iterationScriptedRound {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if len(p.steps) == 0 {
		return iterationScriptedRound{final: "(script exhausted)"}
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	return step
}

func (p *iterationScriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *iterationScriptedProvider) reply(round iterationScriptedRound, callID string) provider.StreamChunk {
	if round.final != "" {
		return provider.StreamChunk{Content: round.final, Done: true}
	}
	return provider.StreamChunk{ToolCalls: []provider.ToolCall{{
		ID: callID, Type: "function",
		Function: provider.FunctionCall{Name: round.tool, Arguments: `{"n":"` + callID + `"}`},
	}}, Done: true}
}

func (p *iterationScriptedProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	round := p.next()
	resp := &provider.Response{Content: round.final}
	if round.final == "" {
		resp.ToolCalls = []provider.ToolCall{{
			ID: fmt.Sprintf("call_%d", p.callCount()), Type: "function",
			Function: provider.FunctionCall{Name: round.tool, Arguments: `{"n":"` + fmt.Sprint(p.callCount()) + `"}`},
		}}
	}
	return resp, nil
}

func (p *iterationScriptedProvider) ChatStream(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	round := p.next()
	ch := make(chan provider.StreamChunk, 2)
	ch <- p.reply(round, fmt.Sprintf("call_%d", p.callCount()))
	close(ch)
	return provider.NewStreamReader(ch), nil
}

// newScriptedTurnAgent wires the scripted provider into a working agent with a
// registry whose "probe" tool succeeds.
func newScriptedTurnAgent(t *testing.T, prov *iterationScriptedProvider, iterations, continues int) *Agent {
	t.Helper()
	a, _ := newGateAgent(t)
	a.provider = prov
	a.maxToolIterations = iterations
	a.maxToolContinues = continues
	a.registry.Register("probe", "test tool", nil, func(context.Context, json.RawMessage) (string, error) {
		return "probe result", nil
	})
	return a
}

func runTurn(t *testing.T, a *Agent, chatID string) []provider.Message {
	t.Helper()
	msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: chatID, Text: "go"}
	a.HandleMessage(context.Background(), msg)
	return a.sessions.Get(sessionTriple(msg, msg.ProjectID)).GetMessages()
}

// iterationCapValue digs the badge's number out of the last assistant message.
// A missing badge returns 0, which is what "the turn finished on its own" looks
// like.
func iterationCapValue(msgs []provider.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		if msgs[i].Metadata == nil {
			return 0
		}
		if v, ok := msgs[i].Metadata["iterationCapReached"].(bool); ok && v {
			switch n := msgs[i].Metadata["iterationCapValue"].(type) {
			case int:
				return n
			case float64:
				return int(n)
			}
			return -1
		}
		return 0
	}
	return 0
}

func TestIterationCapExtendsTheTurnWhenTheRoundMadeProgress(t *testing.T) {
	prov := &iterationScriptedProvider{steps: []iterationScriptedRound{
		{tool: "probe"}, {tool: "probe"}, // segment 1 burns its two rounds
		{tool: "probe"}, // segment 2 starts; the model is still working
		{final: "delivered after the extension"},
	}}
	a := newScriptedTurnAgent(t, prov, 2, 1)

	msgs := runTurn(t, a, "chat-extend")

	if got := prov.callCount(); got != 4 {
		t.Fatalf("provider calls = %d, want 4 (2 rounds + 1 extended round + final): the turn did not continue", got)
	}
	if got := iterationCapValue(msgs); got != 0 {
		t.Fatalf("the answer was badged as partial (budget %d) even though the extension finished the work", got)
	}
	if last := msgs[len(msgs)-1]; !strings.Contains(last.Content, "delivered after the extension") {
		t.Fatalf("last message = %q", last.Content)
	}
	// The continuation nudge is prompt scaffolding for the extended segment —
	// it must not become part of the session's history.
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(m.Content, "the turn continues") {
			t.Fatal("the continuation nudge leaked into history")
		}
	}
}

func TestIterationCapDoesNotExtendAFailingTurn(t *testing.T) {
	prov := &iterationScriptedProvider{steps: []iterationScriptedRound{
		{tool: "no_such_tool"}, {tool: "no_such_tool"}, // both rounds fail
		{final: "synthesized from what I have"},
	}}
	a := newScriptedTurnAgent(t, prov, 2, 1)

	msgs := runTurn(t, a, "chat-nofail-extend")

	if got := prov.callCount(); got != 3 {
		t.Fatalf("provider calls = %d, want 3 (2 failing rounds + synthesis, no extension)", got)
	}
	if got := iterationCapValue(msgs); got != 2 {
		t.Fatalf("badge budget = %d, want 2 — a failing turn keeps the original budget and the badge", got)
	}
}

func TestIterationCapBadgeCountsEverySegmentItGranted(t *testing.T) {
	// One round per segment, two extensions: three segments of work, all of it
	// productive, and the turn still runs out — the badge must report the
	// budget the turn actually had (3), not the per-segment 1.
	prov := &iterationScriptedProvider{steps: []iterationScriptedRound{
		{tool: "probe"}, {tool: "probe"}, {tool: "probe"},
		{final: "still partial"},
	}}
	a := newScriptedTurnAgent(t, prov, 1, 2)

	msgs := runTurn(t, a, "chat-badge")

	if got := prov.callCount(); got != 4 {
		t.Fatalf("provider calls = %d, want 4 (3 segments + the synthesis call)", got)
	}
	if got := iterationCapValue(msgs); got != 3 {
		t.Fatalf("badge budget = %d, want 3 (segments × rounds)", got)
	}
}

func TestIterationContinuesOffKeepsTheOldBehavior(t *testing.T) {
	prov := &iterationScriptedProvider{steps: []iterationScriptedRound{
		{tool: "probe"}, {tool: "probe"},
		{final: "synthesized"},
	}}
	a := newScriptedTurnAgent(t, prov, 2, 0)

	msgs := runTurn(t, a, "chat-off")

	if got := prov.callCount(); got != 3 {
		t.Fatalf("provider calls = %d, want 3 — with continuations off nothing may change", got)
	}
	if got := iterationCapValue(msgs); got != 2 {
		t.Fatalf("badge budget = %d, want 2", got)
	}
}
