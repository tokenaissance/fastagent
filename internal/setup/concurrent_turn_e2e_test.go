package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/doctor"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// toolScriptProvider asks for one tool call per turn and answers in text once
// the prompt carries a tool result. Reading the prompt instead of counting calls
// keeps both turns of this test deterministic on one provider: the cron turn
// asks for the tool, the web turn (whose prompt already contains the cron
// turn's result) answers.
type toolScriptProvider struct {
	toolName string
	rounds   atomic.Int64
	// calls counts *every* consultation, not just the rounds that ask for a
	// tool. `rounds` alone cannot see "one more model call": the forced final
	// delivery asks for no tool, so it leaves `rounds` where it was — which is
	// what made the cancel test's "must not spend another round" check a belt
	// no falsification could redden.
	calls atomic.Int64
}

func (p *toolScriptProvider) response(msgs []provider.Message) *provider.Response {
	p.calls.Add(1)
	for _, m := range msgs {
		if m.Role == "tool" {
			return &provider.Response{Content: "done"}
		}
	}
	return &provider.Response{ToolCalls: []provider.ToolCall{{
		ID:   fmt.Sprintf("call_%s_%d", p.toolName, p.rounds.Add(1)),
		Type: "function",
		Function: provider.FunctionCall{
			Name:      p.toolName,
			Arguments: "{}",
		},
	}}}
}

func (p *toolScriptProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	return p.response(msgs), nil
}

func (p *toolScriptProvider) ChatStream(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	resp := p.response(msgs)
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: resp.Content, ToolCalls: resp.ToolCalls, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

// registerBlockingTool adds a tool that signals when it starts and returns only
// once the test releases it, so the first turn can be parked inside a tool while
// the second one arrives.
func registerBlockingTool(t *testing.T, ag *agent.Agent, name string) (started <-chan struct{}, release func()) {
	t.Helper()
	startedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	ag.ToolRegistry().Register(name, "test tool that blocks until the test releases it", nil,
		func(ctx context.Context, _ json.RawMessage) (string, error) {
			once.Do(func() { close(startedCh) })
			select {
			case <-releaseCh:
				return "released", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
	var releaseOnce sync.Once
	return startedCh, func() { releaseOnce.Do(func() { close(releaseCh) }) }
}

// The 2026-09-13 incident's writer pair, end to end through the adapter the
// dashboard actually uses: a cron tick is inside a long tool while a web POST
// arrives for the same session. The POST must be announced as queued (the
// dashboard's P1b queue block), must not touch the session while the tick holds
// it, and the history both turns leave behind must satisfy the provider
// contract — one reply per call, in order (docs/session-turn-integrity.md,
// clauses W and O).
func TestConcurrentWebAndCronTurnSerialize(t *testing.T) {
	s, ag := newChatHarness(t, &toolScriptProvider{toolName: "slow_probe"}, 4)
	started, release := registerBlockingTool(t, ag, "slow_probe")

	// The automatic writer: a cron tick, published the way cron.Scheduler does
	// (bus.InboundMessage with Source=cron), routed to the session the web POST
	// below also uses.
	cronTick := bus.InboundMessage{
		Channel: "web", ChatID: "chat-concurrent", UserID: "cron", OwnerUserID: "u_1",
		AgentID: "agt_e2e", Text: "[Cron Job: probe] scheduled tick",
		Source: bus.SourceCron,
	}
	cronDone := make(chan string, 1)
	go func() { cronDone <- ag.HandleMessage(context.Background(), cronTick) }()

	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("cron turn never reached the slow tool")
	}

	// The user writer: a real dashboard POST, streaming.
	rec := newSSERecorder()
	req := chatStreamRequest(t, chatRequest{
		AgentID: "agt_e2e", SessionID: "chat-concurrent", Message: "are you there?", TurnID: "turn-concurrent",
	})
	webDone := make(chan struct{})
	go func() {
		defer close(webDone)
		s.handleChatStream(rec, req)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for !rec.seen("queued") {
		if time.Now().After(deadline) {
			t.Fatalf("web POST never reported that it was queued; body=%q", rec.body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	sess := ag.Sessions().Get("web", "", "chat-concurrent", "")
	if sess == nil {
		t.Fatal("session not found")
	}
	for _, m := range sess.GetMessages() {
		if m.Role == "user" && m.Content == "are you there?" {
			t.Fatalf("queued web turn wrote into the session while the cron turn held it: %v",
				rolesOf(sess.GetMessages()))
		}
	}

	release()
	select {
	case <-cronDone:
	case <-time.After(15 * time.Second):
		t.Fatal("cron turn never finished after the tool was released")
	}
	select {
	case <-webDone:
	case <-time.After(15 * time.Second):
		t.Fatal("queued web turn never finished after the cron turn released the slot")
	}

	msgs := sess.GetMessages()
	if got, want := rolesOf(msgs), []string{"user", "assistant", "tool", "assistant", "user", "assistant"}; !sameStrings(got, want) {
		t.Fatalf("history roles = %v; want %v (the two writers interleaved)", got, want)
	}
	if msgs[0].Content != cronTick.Text || msgs[4].Content != "are you there?" {
		t.Fatalf("user messages = %q, %q; want the cron tick first, then the web message",
			msgs[0].Content, msgs[4].Content)
	}
	replies := map[string]int{}
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			replies[m.ToolCallID]++
		}
	}
	for id, n := range replies {
		if n != 1 {
			t.Fatalf("tool_call_id %s answered %d times; the second answer is what 400s a session (%v)",
				id, n, rolesOf(msgs))
		}
	}
	if findings := doctor.Scan(msgs); len(findings) != 0 {
		t.Fatalf("history violates the pairing contract: %+v", findings)
	}
}

func rolesOf(msgs []provider.Message) []string {
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	return roles
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
