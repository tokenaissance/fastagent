package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/doctor"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// toolRoundProvider asks for one tool call per turn and answers in text once
// the prompt carries a tool result — round 2 of its own turn, or a later turn
// that sees the earlier turn's result. Deciding from the prompt instead of from
// a call counter keeps the fixture honest when two turns share one provider.
type toolRoundProvider struct {
	toolName string
	rounds   atomic.Int64
}

func (p *toolRoundProvider) response(msgs []provider.Message) *provider.Response {
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

func (p *toolRoundProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	return p.response(msgs), nil
}

func (p *toolRoundProvider) ChatStream(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	resp := p.response(msgs)
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: resp.Content, ToolCalls: resp.ToolCalls, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

// blockingTool registers a tool that signals when it starts and returns only
// once the test releases it — the "long tool" a turn can be sitting inside when
// the next message for the session arrives.
func blockingTool(t *testing.T, a *Agent, name string) (started <-chan struct{}, release func()) {
	t.Helper()
	startedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	a.registry.Register(name, "test tool that blocks until the test releases it", nil,
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

func messageRoles(msgs []provider.Message) []string {
	roles := make([]string, 0, len(msgs))
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	return roles
}

func userTexts(msgs []provider.Message) []string {
	var texts []string
	for _, m := range msgs {
		if m.Role == "user" {
			texts = append(texts, m.Content)
		}
	}
	return texts
}

// A turn that is inside a slow tool keeps the session slot for as long as the
// tool runs: the next message for that session waits, and it must not append
// anything — not even its own user message — until the holder released.
// (docs/session-turn-integrity.md, clause O)
func TestQueuedTurnRunsAfterLongTool(t *testing.T) {
	a, _ := newGateAgent(t)
	a.maxToolIterations = 4
	a.provider = &toolRoundProvider{toolName: "slow_probe"}
	started, release := blockingTool(t, a, "slow_probe")

	first := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-long-tool", Text: "first"}
	second := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-long-tool", Text: "second"}
	sess := a.sessions.Get(sessionTriple(first, first.ProjectID))

	done := make(chan struct{}, 2)
	go func() { defer func() { done <- struct{}{} }(); a.HandleMessage(context.Background(), first) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first turn never reached the slow tool")
	}

	go func() { defer func() { done <- struct{}{} }(); a.HandleMessage(context.Background(), second) }()
	waitFor(t, "second turn queued", func() bool { return sess.TurnWaiters() == 1 })

	// The holder is mid-tool: the queued turn must not have written its user
	// message (or anything else) yet.
	for _, m := range sess.GetMessages() {
		if m.Role == "user" && m.Content == "second" {
			t.Fatalf("queued turn wrote its user message while the holder was inside a tool: %v",
				messageRoles(sess.GetMessages()))
		}
	}

	release()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("turn %d never finished after the tool was released", i+1)
		}
	}

	msgs := sess.GetMessages()
	if got, want := messageRoles(msgs), []string{"user", "assistant", "tool", "assistant", "user", "assistant"}; !equalStrings(got, want) {
		t.Fatalf("history roles = %v; want %v (turns interleaved)", got, want)
	}
	if got, want := userTexts(msgs), []string{"first", "second"}; !equalStrings(got, want) {
		t.Fatalf("user messages = %v; want %v in arrival order", got, want)
	}
}

// The incident's exact timing: a cron tick is inside a long tool when a user
// message lands on the same session (docs/session-turn-integrity.md, clause W +
// the 2026-09-13 appendix). Serialization must leave a history the providers
// accept — the doctor scanner is the oracle for "no double answers, nothing
// dangling".
func TestCronTickDoesNotInterleaveWithWebTurn(t *testing.T) {
	a, _ := newGateAgent(t)
	a.maxToolIterations = 4
	a.provider = &toolRoundProvider{toolName: "slow_probe"}
	started, release := blockingTool(t, a, "slow_probe")

	cronTick := bus.InboundMessage{
		Channel: "web", UserID: "cron", OwnerUserID: "u_owner", ChatID: "chat-cron-web",
		Text: "[Cron Job: probe] scheduled tick", Source: bus.SourceCron,
	}
	webMsg := bus.InboundMessage{
		Channel: "web", UserID: "u_owner", ChatID: "chat-cron-web",
		Text: "are you there?", Source: bus.SourceUser,
	}
	sess := a.sessions.Get(sessionTriple(cronTick, cronTick.ProjectID))

	done := make(chan struct{}, 2)
	go func() { defer func() { done <- struct{}{} }(); a.HandleMessage(context.Background(), cronTick) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("cron turn never reached the slow tool")
	}

	go func() { defer func() { done <- struct{}{} }(); a.HandleMessage(context.Background(), webMsg) }()
	waitFor(t, "web turn queued behind the cron tick", func() bool { return sess.TurnWaiters() == 1 })
	release()

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("turn %d never finished after the tool was released", i+1)
		}
	}

	msgs := sess.GetMessages()
	if got, want := messageRoles(msgs), []string{"user", "assistant", "tool", "assistant", "user", "assistant"}; !equalStrings(got, want) {
		t.Fatalf("history roles = %v; want %v (the cron tick and the web turn interleaved)", got, want)
	}
	replies := map[string]int{}
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			replies[m.ToolCallID]++
		}
	}
	for id, n := range replies {
		if n != 1 {
			t.Fatalf("tool_call_id %s answered %d times; providers reject the second answer (%v)",
				id, n, messageRoles(msgs))
		}
	}
	if findings := doctor.Scan(msgs); len(findings) != 0 {
		t.Fatalf("history violates the pairing contract: %+v", findings)
	}
}

func equalStrings(got, want []string) bool {
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
