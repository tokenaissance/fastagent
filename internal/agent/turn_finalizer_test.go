package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// finalizerProbe counts PostTurn effects and records the context each one saw.
type finalizerProbe struct {
	mu        sync.Mutex
	calls     int
	toolCalls []int
	ctxErrs   []error
	onCall    func()
}

// cancelThenFailProvider cancels the turn context from inside the model call
// and then reports the cancellation. Admission and prompt assembly therefore
// succeed, while the finalizer sees exactly the state production sees when a
// turn deadline expires mid-call.
type cancelThenFailProvider struct {
	cancel context.CancelFunc
	err    error
}

func (p *cancelThenFailProvider) Chat(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.Response, error) {
	p.cancel()
	return nil, p.err
}

func (p *cancelThenFailProvider) ChatStream(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.StreamReader, error) {
	p.cancel()
	return nil, p.err
}

func (p *finalizerProbe) hook(ctx context.Context, hc *HookContext) {
	p.mu.Lock()
	p.calls++
	p.toolCalls = append(p.toolCalls, hc.ToolCallCount)
	p.ctxErrs = append(p.ctxErrs, ctx.Err())
	onCall := p.onCall
	p.mu.Unlock()
	if onCall != nil {
		onCall()
	}
}

func (p *finalizerProbe) snapshot() (int, []int, []error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, append([]int(nil), p.toolCalls...), append([]error(nil), p.ctxErrs...)
}

func mustFinalizerProbe(t *testing.T, a *Agent) *finalizerProbe {
	t.Helper()
	p := &finalizerProbe{}
	a.hooks.Register(PostTurn, p.hook)
	return p
}

// F1: the finalizer is a one-shot contract, including under concurrent callers.
func TestTurnFinalizerRunsAtMostOnce(t *testing.T) {
	a, _ := newGateAgent(t)
	probe := mustFinalizerProbe(t, a)
	f := newTurnFinalizer(a)
	in := turnFinalizeInput{msg: bus.InboundMessage{Channel: "web", ChatID: "c"}, toolCallCount: 2}

	if !f.Finalize(context.Background(), in) {
		t.Fatal("first Finalize reported no effect")
	}
	if f.Finalize(context.Background(), in) {
		t.Fatal("second Finalize reported an effect; F1 is broken")
	}
	if !f.Finalized() {
		t.Fatal("Finalized() = false after a finalize call")
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Finalize(context.Background(), in)
		}()
	}
	wg.Wait()

	if calls, _, _ := probe.snapshot(); calls != 1 {
		t.Fatalf("PostTurn fired %d times; want exactly 1", calls)
	}
}

// F4: a failed turn whose caller deadline already expired still runs PostTurn
// on a detached, bounded context. Without recordCtx the goal continuation is
// skipped, which is the production stall documented on turnFinalizer.
func TestTurnFinalizerFailedOutcomeSurvivesAnExpiredDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Agent, bus.InboundMessage)
	}{
		{"HandleMessage", func(a *Agent, msg bus.InboundMessage) { a.HandleMessage(context.Background(), msg) }},
		{"HandleMessageStream", func(a *Agent, msg bus.InboundMessage) { drainStream(a.HandleMessageStream(context.Background(), msg)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newGateAgent(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a.provider = &cancelThenFailProvider{cancel: cancel, err: context.Canceled}
			probe := mustFinalizerProbe(t, a)

			msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-" + tc.name, Text: "go"}
			if tc.name == "HandleMessage" {
				a.HandleMessage(ctx, msg)
			} else {
				drainStream(a.HandleMessageStream(ctx, msg))
			}

			calls, _, ctxErrs := probe.snapshot()
			if calls != 1 {
				t.Fatalf("PostTurn fired %d times; want exactly 1", calls)
			}
			if ctxErrs[0] != nil {
				t.Fatalf("PostTurn context error = %v; want a detached context with its own budget", ctxErrs[0])
			}
		})
	}
}

// F2: every result path finalizes exactly once. The table covers the two entry
// points and the two result shapes that used to duplicate their terminal code:
// a normal reply and the forced delivery after the iteration cap.
func TestTurnFinalizerCoversEveryResultPath(t *testing.T) {
	type testCase struct {
		name          string
		scripted      bool
		iterations    int
		wantToolCalls int
		run           func(*Agent, bus.InboundMessage)
	}
	cases := []testCase{
		{
			name: "non-stream reply", run: func(a *Agent, msg bus.InboundMessage) { a.HandleMessage(context.Background(), msg) },
		},
		{
			name: "stream reply", run: func(a *Agent, msg bus.InboundMessage) { drainStream(a.HandleMessageStream(context.Background(), msg)) },
		},
		{
			name: "non-stream iteration cap", scripted: true, iterations: 1, wantToolCalls: 1,
			run: func(a *Agent, msg bus.InboundMessage) { a.HandleMessage(context.Background(), msg) },
		},
		{
			name: "stream iteration cap", scripted: true, iterations: 1, wantToolCalls: 1,
			run: func(a *Agent, msg bus.InboundMessage) { drainStream(a.HandleMessageStream(context.Background(), msg)) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newGateAgent(t)
			if tc.scripted {
				prov := &iterationScriptedProvider{steps: []iterationScriptedRound{
					{tool: "probe"},
					{final: "synthesized"},
				}}
				a = newScriptedTurnAgent(t, prov, tc.iterations, 0)
			}
			probe := mustFinalizerProbe(t, a)
			msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-" + tc.name, Text: "go"}
			tc.run(a, msg)

			calls, toolCalls, _ := probe.snapshot()
			if calls != 1 {
				t.Fatalf("PostTurn fired %d times; want exactly 1", calls)
			}
			if toolCalls[0] != tc.wantToolCalls {
				t.Fatalf("PostTurn toolCallCount = %d; want %d", toolCalls[0], tc.wantToolCalls)
			}
		})
	}
}

// blockingStreamProvider answers Chat immediately and holds ChatStream open
// until the test releases it. It lets the test observe the session slot while
// the reply is still in flight.
type blockingStreamProvider struct {
	gate chan struct{}
}

func (p *blockingStreamProvider) Chat(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.Response, error) {
	return &provider.Response{}, nil
}

func (p *blockingStreamProvider) ChatStream(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.StreamReader, error) {
	ch := make(chan provider.StreamChunk, 1)
	go func() {
		<-p.gate
		ch <- provider.StreamChunk{Content: "done", Done: true}
		close(ch)
	}()
	return provider.NewStreamReader(ch), nil
}

// F5: the streaming path must not release the session slot until the final
// message is persisted. Before the hand-off, `defer turn.release()` in
// HandleMessageStream freed the slot while the goroutine still owned the
// session, so a second turn could write the same history concurrently.
func TestStreamingTurnHoldsTheSessionSlotUntilTheReplyLands(t *testing.T) {
	a, _ := newGateAgent(t)
	prov := &blockingStreamProvider{gate: make(chan struct{})}
	a.provider = prov
	probe := mustFinalizerProbe(t, a)

	msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-stream-lease", Text: "go"}
	sess := a.sessions.Get(sessionTriple(msg, msg.ProjectID))
	sr := a.HandleMessageStream(context.Background(), msg)

	if !sess.TurnActive() {
		t.Fatal("the session slot was released before the stream finished")
	}

	close(prov.gate)
	drainStream(sr)

	deadline := time.Now().Add(2 * time.Second)
	for sess.TurnActive() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.TurnActive() {
		t.Fatal("the session slot was not released after the stream finished")
	}
	if calls, _, _ := probe.snapshot(); calls != 1 {
		t.Fatalf("PostTurn fired %d times; want exactly 1", calls)
	}
}
