package setup

// End-to-end for the fan-out, through the real chat endpoint: real agent
// runtime, real tool registry, real sub-agent loop, real session — only the
// model is faked.
//
// The unit tests in internal/agent pin the scheduling decisions (serial, in
// emission order, clamped, refused when out of clock). This pins what the person
// on the other end gets: N delegate_task calls in one turn, every one of them
// answered, the stream carrying the sub-agent heartbeats, and a turn that ends.
// The failure this exists to make impossible is the row that never came back
// (2026-09-14, "Queued (waiting on prior sub-agent)…").

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// fanOutE2EProvider fans one turn out into `fanout` delegate_task calls, answers
// every sub-agent immediately, and answers the parent on its next round. Turns
// are told apart by their first user message: the same fake serves the parent
// and every sub-agent.
type fanOutE2EProvider struct {
	fanout int

	mu       sync.Mutex
	emitted  map[string]bool
	subCalls int
}

func e2eIsSubagent(msgs []provider.Message) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, "# Subagent mode") {
			return true
		}
	}
	return false
}

func e2eTurnKey(msgs []provider.Message) string {
	for _, m := range msgs {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

func (p *fanOutE2EProvider) respond(msgs []provider.Message) *provider.Response {
	if e2eIsSubagent(msgs) {
		p.mu.Lock()
		p.subCalls++
		p.mu.Unlock()
		// Name the work after the task it was handed: the order the answers come
		// back in is then observable end to end.
		return &provider.Response{Content: "brief for " + subagentTask(msgs)}
	}

	key := e2eTurnKey(msgs)
	p.mu.Lock()
	if p.emitted == nil {
		p.emitted = map[string]bool{}
	}
	first := !p.emitted[key]
	p.emitted[key] = true
	fanout := p.fanout
	p.mu.Unlock()

	if !first {
		return &provider.Response{Content: "final answer"}
	}
	calls := make([]provider.ToolCall, 0, fanout)
	for i := 0; i < fanout; i++ {
		calls = append(calls, provider.ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Type: "function",
			Function: provider.FunctionCall{
				Name:      "delegate_task",
				Arguments: fmt.Sprintf(`{"task":"part-%d"}`, i+1),
			},
		})
	}
	return &provider.Response{ToolCalls: calls}
}

// subagentTask is the task a sub-agent was given (its own message list is
// [system, user] and carries nothing else).
func subagentTask(msgs []provider.Message) string {
	for _, m := range msgs {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

func (p *fanOutE2EProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	return p.respond(msgs), nil
}

func (p *fanOutE2EProvider) ChatStream(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	resp := p.respond(msgs)
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: resp.Content, ToolCalls: resp.ToolCalls, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func (p *fanOutE2EProvider) subagentRounds() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.subCalls
}

func TestDelegateTaskFanOutTurnE2E(t *testing.T) {
	for _, n := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("%d calls", n), func(t *testing.T) {
			prov := &fanOutE2EProvider{fanout: n}
			// One round for the fan-out, one for the answer, and room for the
			// loop's own bookkeeping rounds.
			s, ag := newChatHarness(t, prov, n+2)
			chatID := fmt.Sprintf("chat-fanout-e2e-%d", n)

			body := runSingleChatStream(t, s, chatID, fmt.Sprintf("go-%d", n))

			if got := prov.subagentRounds(); got != n {
				t.Fatalf("%d sub-agent rounds ran, want %d; stream=%q", got, n, body)
			}
			if !strings.Contains(body, "final answer") {
				t.Fatalf("the turn never delivered its answer; stream=%q", body)
			}
			// Every sub-agent announced itself and its completion: one heartbeat
			// per call, and the dashboard's indicator has to clear (phase=done).
			if got := strings.Count(body, `"type":"subagent_progress"`); got < n {
				t.Errorf("%d subagent_progress events for %d calls; stream=%q", got, n, body)
			}
			if got := strings.Count(body, `"phase":"done"`); got != n {
				t.Errorf("%d phase=done events for %d calls (the indicator would stay on); stream=%q", got, n, body)
			}

			// The calls themselves are answered, in order, in the session the
			// dashboard reads — a call with no tool_result is the row that stays
			// pending, and out-of-order answers would break "the first unresolved
			// one is the live one".
			sess := ag.Sessions().Get("web", "", chatID, "")
			if sess == nil {
				t.Fatal("session not found")
			}
			var answered []string
			announced := 0
			for _, m := range sess.GetMessages() {
				switch m.Role {
				case "assistant":
					// The call names live on the assistant message; only the tool
					// reply carries Name.
					for _, tc := range m.ToolCalls {
						if tc.Function.Name == "delegate_task" {
							announced++
						}
					}
				case "tool":
					if m.Name == "delegate_task" {
						answered = append(answered, m.Content)
					}
				}
			}
			if announced != n {
				t.Errorf("%d delegate_task calls in history, want %d", announced, n)
			}
			if len(answered) != n {
				t.Fatalf("%d of %d delegate_task calls have a result — the rest would sit at 'Queued…' forever: %v",
					len(answered), n, answered)
			}
			for i, got := range answered {
				want := fmt.Sprintf("brief for part-%d", i+1)
				if !strings.Contains(got, want) {
					t.Errorf("answer %d = %q, want %q (sub-agents must run in emission order)", i, got, want)
				}
			}
		})
	}
}
