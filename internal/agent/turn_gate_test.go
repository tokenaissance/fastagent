package agent

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/session"
)

// gateProvider records when the model was actually consulted, so a test can
// tell "the turn ran" from "the turn waited".
type gateProvider struct {
	called chan string
	reply  string
}

func (p *gateProvider) note(text string) {
	select {
	case p.called <- text:
	default:
	}
}

func (p *gateProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	p.note("chat")
	return &provider.Response{Content: p.reply}, nil
}

func (p *gateProvider) ChatStream(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	p.note("stream")
	ch := make(chan provider.StreamChunk, 2)
	ch <- provider.StreamChunk{Content: p.reply, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func newGateAgent(t *testing.T) (*Agent, *gateProvider) {
	t.Helper()
	mem := NewMemory(t.TempDir())
	prov := &gateProvider{called: make(chan string, 8), reply: "ok"}
	a := &Agent{
		name:              "gate-agent",
		ownerUserID:       "u_owner",
		provider:          prov,
		registry:          tools.NewRegistry("", ""),
		sessions:          session.NewManager(t.TempDir()),
		memory:            mem,
		ctxBuilder:        NewContextBuilder(t.TempDir(), mem, ""),
		hooks:             NewHookRegistry(),
		messageBus:        bus.New(),
		model:             "fake-model",
		maxTokens:         256,
		temperature:       0.7,
		maxToolIterations: 2,
	}
	return a, prov
}

// A turn-start request that finds the session busy must wait for the slot
// instead of writing history concurrently — the defect behind the 2026-09-13
// production incident (docs/session-turn-integrity.md, clauses W and O).
func TestHandleMessageWaitsForInFlightTurn(t *testing.T) {
	a, prov := newGateAgent(t)
	msg := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-1", Text: "hello"}

	sess := a.sessions.Get(sessionTriple(msg, msg.ProjectID))
	if !sess.AcquireTurn(context.Background()) {
		t.Fatal("could not take the turn slot for the test")
	}

	done := make(chan string, 1)
	go func() { done <- a.HandleMessage(context.Background(), msg) }()

	// While the slot is held the queued turn must not consult the model or
	// touch the history.
	time.Sleep(200 * time.Millisecond)
	select {
	case what := <-prov.called:
		t.Fatalf("queued turn called the provider (%s) while the slot was held", what)
	default:
	}
	if got := sess.GetMessages(); len(got) != 0 {
		t.Fatalf("queued turn wrote %d messages while the slot was held", len(got))
	}
	select {
	case reply := <-done:
		t.Fatalf("HandleMessage returned %q while the slot was held", reply)
	default:
	}

	sess.ReleaseTurn()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queued turn never ran after the slot was released")
	}
	msgs := sess.GetMessages()
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("history after the queued turn = %v; want user, assistant", msgs)
	}
}

// Two queued turns run one after the other, and their messages land as
// whole turns (user, assistant, user, assistant) — never interleaved.
func TestHandleMessageSerializesQueuedTurns(t *testing.T) {
	a, _ := newGateAgent(t)
	first := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-2", Text: "first"}
	second := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-2", Text: "second"}

	sess := a.sessions.Get(sessionTriple(first, first.ProjectID))
	if !sess.AcquireTurn(context.Background()) {
		t.Fatal("could not take the turn slot for the test")
	}

	done := make(chan struct{}, 2)
	go func() { a.HandleMessage(context.Background(), first); done <- struct{}{} }()
	waitFor(t, "first turn queued", func() bool { return sess.TurnWaiters() == 1 })
	go func() { a.HandleMessage(context.Background(), second); done <- struct{}{} }()
	waitFor(t, "second turn queued", func() bool { return sess.TurnWaiters() == 2 })

	sess.ReleaseTurn()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("queued turn %d never finished", i+1)
		}
	}

	var roles []string
	var contents []string
	for _, m := range sess.GetMessages() {
		roles = append(roles, m.Role)
		if m.Role == "user" {
			contents = append(contents, m.Content)
		}
	}
	if got := len(roles); got != 4 {
		t.Fatalf("history length = %d (%v); want 4", got, roles)
	}
	want := []string{"user", "assistant", "user", "assistant"}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("history roles = %v; want %v (turns interleaved)", roles, want)
		}
	}
	if len(contents) != 2 || contents[0] != "first" || contents[1] != "second" {
		t.Fatalf("user messages = %v; want first, second in order", contents)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
