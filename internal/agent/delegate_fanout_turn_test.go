package agent

// One turn, several delegate_task calls. Two outcomes are acceptable — every
// sub-agent runs (one at a time), or the turn runs out of clock and the rest say
// so — and one is not: a tool row that stays pending because a call is waiting
// on another one's slot while the turn's clock runs out. That is the shape of
// the turn on 2026-09-14 whose delegate_task sat at "Queued (waiting on prior
// sub-agent)…" and never came back.
//
// The parent loop and the sub-agent loop use different provider entry points
// (ChatStream vs Chat), which is also how the fake below tells them apart — the
// sub-agent's system prompt carries the marker.

import (
	"context"
	"fmt"
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
	// tasks is one delegate_task call per entry, in the order the model emits them.
	tasks []string
	// blockFirstRun parks the first sub-agent round until its own budget ends,
	// so a test can hand the turn's clock to one call and watch what the others
	// do with what is left.
	blockFirstRun bool
	// holdFirstRun parks the first sub-agent round until the test releases it —
	// an external "this sub-agent is still working" for cross-turn cases.
	holdFirstRun chan struct{}

	mu       sync.Mutex
	emitted  map[string]bool // parent turns that already got their calls
	subCalls int
	liveSub  int
	peakSub  int
	started  []string
}

// conversationKey identifies one turn: its first user message. Two turns of the
// same agent (different sessions) must each get the fan-out exactly once, so the
// provider cannot key on a global call counter.
func conversationKey(msgs []provider.Message) string {
	for _, m := range msgs {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

// taskOf returns the task text a sub-agent was handed — the sub-agent's own
// message list is [system, user] and carries nothing else.
func taskOf(msgs []provider.Message) string {
	for _, m := range msgs {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

func isSubagentCall(msgs []provider.Message) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, subagentPromptMarker) {
			return true
		}
	}
	return false
}

func (p *fanOutProvider) respond(ctx context.Context, msgs []provider.Message) (*provider.Response, error) {
	if isSubagentCall(msgs) {
		p.mu.Lock()
		p.subCalls++
		call := p.subCalls
		p.liveSub++
		if p.liveSub > p.peakSub {
			p.peakSub = p.liveSub
		}
		p.started = append(p.started, taskOf(msgs))
		p.mu.Unlock()

		if call == 1 {
			if p.holdFirstRun != nil {
				select {
				case <-p.holdFirstRun:
				case <-ctx.Done():
					p.mu.Lock()
					p.liveSub--
					p.mu.Unlock()
					return nil, ctx.Err()
				}
			}
			if p.blockFirstRun {
				// The budget this sub-agent was given is the turn's leftover plus
				// the margin the turn kept for itself; spending it here is what
				// makes the next call's remaining-time decision observable.
				<-ctx.Done()
				p.mu.Lock()
				p.liveSub--
				p.mu.Unlock()
				return nil, ctx.Err()
			}
		}

		time.Sleep(20 * time.Millisecond)
		p.mu.Lock()
		p.liveSub--
		p.mu.Unlock()
		return &provider.Response{Content: "sub-agent brief"}, nil
	}

	key := conversationKey(msgs)
	p.mu.Lock()
	if p.emitted == nil {
		p.emitted = map[string]bool{}
	}
	first := !p.emitted[key]
	p.emitted[key] = true
	p.mu.Unlock()
	if first {
		calls := make([]provider.ToolCall, 0, len(p.tasks))
		for i, task := range p.tasks {
			calls = append(calls, provider.ToolCall{
				ID:   fmt.Sprintf("call_%d", i),
				Type: "function",
				Function: provider.FunctionCall{
					Name:      "delegate_task",
					Arguments: fmt.Sprintf(`{"task":%q}`, task),
				},
			})
		}
		return &provider.Response{ToolCalls: calls}, nil
	}
	return &provider.Response{Content: "final answer"}, nil
}

func (p *fanOutProvider) Chat(ctx context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	return p.respond(ctx, msgs)
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
func newFanOutAgent(t *testing.T, tasks ...string) (*Agent, *fanOutProvider) {
	t.Helper()
	a, _ := newGateAgent(t)
	prov := &fanOutProvider{tasks: tasks}
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

// Every call is either executed or answered — never left pending — and at most
// one sub-agent runs at a time. The table walks the fan-out width 1→3 because
// the failure the serial slot creates is arithmetic: the more calls, the more
// wall time the turn has to have, and the sooner the leftovers must say so.
func TestTurnFanOutOfDelegateTasksIsScheduledAndAlwaysReturns(t *testing.T) {
	tasks := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("task-%d", i+1)
		}
		return out
	}
	runTurn := func(t *testing.T, a *Agent, ctx context.Context, msg bus.InboundMessage) time.Duration {
		t.Helper()
		start := time.Now()
		done := make(chan struct{})
		go func() { defer close(done); a.HandleMessage(ctx, msg) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("the turn never returned — a delegate_task call is still waiting on another")
		}
		return time.Since(start)
	}

	for _, n := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("%d calls run one at a time", n), func(t *testing.T) {
			a, prov := newFanOutAgent(t, tasks(n)...)
			msg := bus.InboundMessage{Channel: "web", UserID: "u_owner",
				ChatID: fmt.Sprintf("chat-fanout-%d", n), Text: "go"}

			elapsed := runTurn(t, a, context.Background(), msg)

			results := delegateResults(t, a, msg)
			if len(results) != n {
				t.Fatalf("%d of %d delegate_task calls came back: %v", len(results), n, results)
			}
			for i, r := range results {
				if !strings.Contains(r, "sub-agent brief") {
					t.Errorf("call %d = %q, want the sub-agent's text", i, r)
				}
			}
			if peak := prov.peakSubagents(); peak != 1 {
				t.Fatalf("%d sub-agents ran at once; delegate_task is registered serial", peak)
			}
			if got := prov.subagentRounds(); got != n {
				t.Fatalf("%d sub-agent rounds, want one per call", got)
			}
			if len(prov.started) != n {
				t.Fatalf("%d sub-agents started, want %d", len(prov.started), n)
			}
			// In emission order: the SDK executor keeps non-concurrency-safe tools
			// in a sequential group and runs that group in the order the model
			// emitted it. The dashboard's "the first unresolved delegate_task is
			// the live one" reads exactly this.
			for i, task := range tasks(n) {
				if prov.started[i] != task {
					t.Fatalf("sub-agent %d ran %q, want %q (order: %v)", i, prov.started[i], task, prov.started)
				}
			}
			if elapsed > 15*time.Second {
				t.Fatalf("%d sub-agents took %s — nothing here is allowed to wait on a clock", n, elapsed)
			}
		})

		t.Run(fmt.Sprintf("%d calls: all answered when the turn cannot afford them", n), func(t *testing.T) {
			a, prov := newFanOutAgent(t, tasks(n)...)
			msg := bus.InboundMessage{Channel: "web", UserID: "u_owner",
				ChatID: fmt.Sprintf("chat-fanout-short-%d", n), Text: "go"}

			// Less left than the margin a sub-agent needs to start and still leave
			// the parent room to answer: the honest answer is a refusal, not a
			// sub-agent cut down at the end of the turn.
			ctx, cancel := context.WithTimeout(context.Background(), subagentTurnMargin-time.Second)
			defer cancel()

			elapsed := runTurn(t, a, ctx, msg)

			results := delegateResults(t, a, msg)
			if len(results) != n {
				t.Fatalf("%d of %d delegate_task calls came back: %v", len(results), n, results)
			}
			for i, r := range results {
				// The refusal is the tool's own marker for "never started"; the
				// "stopped early" marker (a sub-agent that ran and was cut off)
				// would satisfy a looser search for the same advice text.
				if !strings.Contains(r, "[subagent failed") {
					t.Errorf("call %d = %q, want the refusal that names the next step", i, r)
				}
			}
			if got := prov.subagentRounds(); got != 0 {
				t.Fatalf("%d sub-agent rounds ran for a turn that cannot afford one", got)
			}
			if elapsed > 15*time.Second {
				t.Fatalf("refusing took %s", elapsed)
			}
		})
	}

	// The middle case: the turn can pay for one sub-agent, and the first call to
	// reach the slot spends that budget. The rest must report, not queue forever —
	// and the one that ran must still hand back what it gathered.
	t.Run("three calls: the leftovers report once the clock is spent", func(t *testing.T) {
		a, prov := newFanOutAgent(t, tasks(3)...)
		prov.blockFirstRun = true
		msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-fanout-spent", Text: "go"}

		ctx, cancel := context.WithTimeout(context.Background(), subagentTurnMargin+time.Second)
		defer cancel()

		elapsed := runTurn(t, a, ctx, msg)

		results := delegateResults(t, a, msg)
		if len(results) != 3 {
			t.Fatalf("%d of 3 delegate_task calls came back: %v", len(results), results)
		}
		var ran, refused int
		for _, r := range results {
			switch {
			case strings.Contains(r, "[subagent stopped early"):
				ran++
			case strings.Contains(r, "[subagent failed"):
				refused++
			}
		}
		if ran != 1 || refused != 2 {
			t.Fatalf("ran=%d refused=%d (want 1 and 2): %v", ran, refused, results)
		}
		if peak := prov.peakSubagents(); peak != 1 {
			t.Fatalf("%d sub-agents ran at once", peak)
		}
		if elapsed > 10*time.Second {
			t.Fatalf("the turn took %s for one clamped sub-agent", elapsed)
		}
	})
}

// The overlap one round cannot produce: two sessions of the SAME agent, both
// delegating. Within a round the SDK executor already runs non-concurrency-safe
// tools one at a time and in emission order, so the registry's slot exists for
// this — a sub-agent still working from an earlier turn (or another session)
// while a new turn asks for one.
//
// The wait belongs to the waiting turn's clock. When that clock ends the call
// returns instead of staying pending: the shape of the row that said "Queued
// (waiting on prior sub-agent)…" and never came back (2026-09-14).
func TestDelegateTaskSlotSpansTurnsAndReleasesOnTheWaiterClock(t *testing.T) {
	startTurn := func(a *Agent, ctx context.Context, chatID string) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			// The text is the session id so the fake provider can tell the two
			// conversations apart (and give each exactly one fan-out).
			a.HandleMessage(ctx, bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: chatID, Text: chatID})
		}()
		return done
	}
	waitFor := func(t *testing.T, what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitDone := func(t *testing.T, d chan struct{}, what string) {
		t.Helper()
		select {
		case <-d:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s never returned", what)
		}
	}

	t.Run("a second session waits, then runs", func(t *testing.T) {
		a, prov := newFanOutAgent(t, "slow")
		hold := make(chan struct{})
		prov.holdFirstRun = hold

		msgA := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "session-a", Text: "go"}
		doneA := startTurn(a, context.Background(), "session-a")
		waitFor(t, "the first sub-agent to be running", func() bool { return prov.subagentRounds() == 1 })

		msgB := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "session-b", Text: "go"}
		doneB := startTurn(a, context.Background(), "session-b")

		// While the first turn holds the slot, the second must not start a second
		// sub-agent — that is the whole reason the two are serialized.
		time.Sleep(200 * time.Millisecond)
		if got := prov.subagentRounds(); got != 1 {
			t.Fatalf("%d sub-agent rounds ran while one was still working", got)
		}
		if peak := prov.peakSubagents(); peak != 1 {
			t.Fatalf("peak sub-agents = %d, want 1", peak)
		}

		close(hold)
		waitDone(t, doneA, "the first turn")
		waitDone(t, doneB, "the waiting turn")

		if got := prov.subagentRounds(); got != 2 {
			t.Fatalf("sub-agent rounds = %d, want one per turn", got)
		}
		for _, msg := range []bus.InboundMessage{msgA, msgB} {
			results := delegateResults(t, a, msg)
			if len(results) != 1 || !strings.Contains(results[0], "sub-agent brief") {
				t.Fatalf("turn %s: results = %v", msg.ChatID, results)
			}
		}
	})

	t.Run("the waiting turn's clock releases it", func(t *testing.T) {
		a, prov := newFanOutAgent(t, "slow")
		hold := make(chan struct{})
		prov.holdFirstRun = hold

		doneA := startTurn(a, context.Background(), "session-c")
		waitFor(t, "the first sub-agent to be running", func() bool { return prov.subagentRounds() == 1 })

		// One second of clock, and the slot is not coming back in time: the call
		// has to come back as a failure rather than sit on it.
		ctxB, cancelB := context.WithTimeout(context.Background(), time.Second)
		defer cancelB()
		msgB := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "session-d", Text: "go"}
		doneB := startTurn(a, ctxB, "session-d")

		waitDone(t, doneB, "the waiting turn")
		results := delegateResults(t, a, msgB)
		if len(results) != 1 {
			t.Fatalf("results = %v, want the waiting call answered", results)
		}
		// The call never reached the tool: the slot released it when the turn
		// ended, and the message says so instead of the bare provider-style
		// "context deadline exceeded".
		if !strings.Contains(results[0], "never started") {
			t.Fatalf("result = %q, want the wait's own verdict", results[0])
		}
		if got := prov.subagentRounds(); got != 1 {
			t.Fatalf("%d sub-agent rounds ran; the waiting turn must not start one", got)
		}

		close(hold)
		waitDone(t, doneA, "the first turn")
	})
}
