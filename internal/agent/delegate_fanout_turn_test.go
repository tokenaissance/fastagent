package agent

// One turn, two delegate_task calls. Two outcomes are acceptable — both
// sub-agents run, or the turn cannot afford them and both say so — and one is
// not: a tool row that stays pending because the second call is waiting on the
// first one's slot while the turn's clock runs out. That is the shape of the
// turn on 2026-09-14 whose delegate_task sat at "Queued (waiting on prior
// sub-agent)…" and never came back.
//
// The parent loop and the sub-agent loop use different provider entry points
// (ChatStream vs Chat), which is also how the fake below tells them apart — the
// sub-agent's system prompt carries the marker.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

const subagentPromptMarker = "# Subagent mode"

// fanOutProvider emits two delegate_task calls in the parent's first round and
// answers everything after that. Sub-agent rounds are recognised by the system
// prompt they carry, and the 20ms inside them is the window an unserialized
// sibling would overlap in.
type fanOutProvider struct {
	mu          sync.Mutex
	parentCalls int
	subCalls    int
	liveSub     int
	peakSub     int
}

func isSubagentCall(msgs []provider.Message) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, subagentPromptMarker) {
			return true
		}
	}
	return false
}

func (p *fanOutProvider) respond(msgs []provider.Message) *provider.Response {
	if isSubagentCall(msgs) {
		p.mu.Lock()
		p.subCalls++
		p.liveSub++
		if p.liveSub > p.peakSub {
			p.peakSub = p.liveSub
		}
		p.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		p.mu.Lock()
		p.liveSub--
		p.mu.Unlock()
		return &provider.Response{Content: "sub-agent brief"}
	}

	p.mu.Lock()
	p.parentCalls++
	n := p.parentCalls
	p.mu.Unlock()
	if n == 1 {
		return &provider.Response{ToolCalls: []provider.ToolCall{
			{ID: "call_a", Type: "function", Function: provider.FunctionCall{Name: "delegate_task", Arguments: `{"task":"first"}`}},
			{ID: "call_b", Type: "function", Function: provider.FunctionCall{Name: "delegate_task", Arguments: `{"task":"second"}`}},
		}}
	}
	return &provider.Response{Content: "final answer"}
}

func (p *fanOutProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	return p.respond(msgs), nil
}

func (p *fanOutProvider) ChatStream(ctx context.Context, msgs []provider.Message, tools []provider.Tool, model string, maxTokens int, temp float64) (*provider.StreamReader, error) {
	resp, err := p.Chat(ctx, msgs, tools, model, maxTokens, temp)
	if err != nil {
		return nil, err
	}
	ch := make(chan provider.StreamChunk, 2)
	ch <- provider.StreamChunk{Content: resp.Content, ToolCalls: resp.ToolCalls, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func (p *fanOutProvider) subagentRounds() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.subCalls
}

func (p *fanOutProvider) peakSubagents() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peakSub
}

// newFanOutAgent is the gate agent plus the one registration this test is about:
// delegate_task, with the agent itself as the runner (which is what loops.go
// does in production).
func newFanOutAgent(t *testing.T) (*Agent, *fanOutProvider) {
	t.Helper()
	a, _ := newGateAgent(t)
	prov := &fanOutProvider{}
	a.provider = prov
	a.maxToolIterations = 4
	tools.RegisterDelegateTask(a.registry, a)
	return a, prov
}

// delegateResults returns the tool_result text of every delegate_task call this
// turn recorded. A call with no entry is a call the UI would still be showing
// as pending.
func delegateResults(t *testing.T, a *Agent, msg bus.InboundMessage) []string {
	t.Helper()
	var out []string
	for _, m := range a.sessions.Get(sessionTriple(msg, msg.ProjectID)).GetMessages() {
		if m.Role == "tool" && m.Name == "delegate_task" {
			out = append(out, m.Content)
		}
	}
	return out
}

func TestTurnFanOutOfTwoDelegateTasksAlwaysReturns(t *testing.T) {
	t.Run("both run when the turn can afford them", func(t *testing.T) {
		a, prov := newFanOutAgent(t)
		msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-fanout", Text: "go"}

		start := time.Now()
		done := make(chan struct{})
		go func() { defer close(done); a.HandleMessage(context.Background(), msg) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("the turn never returned — a delegate_task call is still waiting on the other")
		}

		results := delegateResults(t, a, msg)
		if len(results) != 2 {
			t.Fatalf("%d of 2 delegate_task calls came back: %v", len(results), results)
		}
		for i, r := range results {
			if !strings.Contains(r, "sub-agent brief") {
				t.Errorf("call %d = %q, want the sub-agent's text", i, r)
			}
		}
		if peak := prov.peakSubagents(); peak != 1 {
			t.Fatalf("%d sub-agents ran at once; delegate_task is registered serial", peak)
		}
		if n := prov.subagentRounds(); n != 2 {
			t.Fatalf("%d sub-agent rounds, want one per call", n)
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("two sub-agents took %s — nothing here is allowed to wait on a clock", elapsed)
		}
	})

	t.Run("both report, in time, when the turn cannot afford them", func(t *testing.T) {
		a, prov := newFanOutAgent(t)
		msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-fanout-short", Text: "go"}

		// Less left than the margin a sub-agent needs to start and still leave the
		// parent room to answer: the honest answer is a refusal, not a sub-agent
		// that gets cut down at the end of the turn.
		ctx, cancel := context.WithTimeout(context.Background(), subagentTurnMargin-time.Second)
		defer cancel()

		done := make(chan struct{})
		go func() { defer close(done); a.HandleMessage(ctx, msg) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("the turn never returned — a delegate_task call is still waiting on the other")
		}

		results := delegateResults(t, a, msg)
		if len(results) != 2 {
			t.Fatalf("%d of 2 delegate_task calls came back: %v", len(results), results)
		}
		for i, r := range results {
			if !strings.Contains(r, "re-issue") {
				t.Errorf("call %d = %q, want the refusal that names the next step", i, r)
			}
		}
		if n := prov.subagentRounds(); n != 0 {
			t.Fatalf("%d sub-agent rounds ran for a turn that cannot afford one", n)
		}
	})
}
