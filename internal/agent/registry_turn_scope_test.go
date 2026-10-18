package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// scopeLeakProvider parks chat A inside its first model call, then answers
// chat B completely, then lets A continue. That ordering makes the shared
// registry's state change between A's bindSession and A's tool execution.
type scopeLeakProvider struct {
	chatAAtModel chan struct{}
	releaseA     chan struct{}
	onceA        sync.Once

	mu    sync.Mutex
	calls map[string]int
}

func (p *scopeLeakProvider) respond(msgs []provider.Message) (*provider.Response, error) {
	chat := ""
	for _, m := range msgs {
		if strings.Contains(m.Content, "chat-a") {
			chat = "chat-a"
			break
		}
	}
	p.mu.Lock()
	p.calls[chat]++
	call := p.calls[chat]
	p.mu.Unlock()

	if chat == "chat-a" && call == 1 {
		p.onceA.Do(func() { close(p.chatAAtModel) })
		<-p.releaseA
		return &provider.Response{ToolCalls: []provider.ToolCall{{
			ID:       "call-scope-a",
			Type:     "function",
			Function: provider.FunctionCall{Name: "scope_probe", Arguments: "{}"},
		}}}, nil
	}
	return &provider.Response{Content: "ok"}, nil
}

func (p *scopeLeakProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	return p.respond(msgs)
}

func (p *scopeLeakProvider) ChatStream(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	resp, err := p.respond(msgs)
	if err != nil {
		return nil, err
	}
	ch := make(chan provider.StreamChunk, 2)
	ch <- provider.StreamChunk{Content: resp.Content, ToolCalls: resp.ToolCalls, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

// TestToolRegistryTurnScopeIsNotSharedAcrossConcurrentChats pins the scope a
// tool sees to the turn that invoked it. The registry is one per agent, so two
// chats running at the same time must not overwrite each other's session and
// project binding.
func TestToolRegistryTurnScopeIsNotSharedAcrossConcurrentChats(t *testing.T) {
	a, _ := newGateAgent(t)
	prov := &scopeLeakProvider{
		chatAAtModel: make(chan struct{}),
		releaseA:     make(chan struct{}),
		calls:        make(map[string]int),
	}
	a.provider = prov

	type scope struct {
		session string
		project string
	}
	probe := make(chan scope, 1)
	a.ToolRegistry().Register("scope_probe", "records the registry scope visible to the tool", map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}, func(ctx context.Context, _ json.RawMessage) (string, error) {
		ts := tools.TurnScopeFrom(ctx)
		probe <- scope{session: ts.SessionID, project: ts.ProjectID}
		return "observed", nil
	})

	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		a.HandleMessage(context.Background(), bus.InboundMessage{
			Channel: "web", UserID: "u_owner", ChatID: "chat-a", ProjectID: "project-a", Text: "chat-a",
		})
	}()

	select {
	case <-prov.chatAAtModel:
	case <-time.After(5 * time.Second):
		t.Fatal("chat A never reached the model")
	}

	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		a.HandleMessage(context.Background(), bus.InboundMessage{
			Channel: "web", UserID: "u_owner", ChatID: "chat-b", ProjectID: "project-b", Text: "chat-b",
		})
	}()
	select {
	case <-doneB:
	case <-time.After(5 * time.Second):
		t.Fatal("chat B never finished")
	}

	close(prov.releaseA)
	select {
	case <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatal("chat A never finished after release")
	}

	select {
	case got := <-probe:
		if got.session != "chat-a" || got.project != "project-a" {
			t.Fatalf("chat A's tool saw session=%q project=%q; want chat-a/project-a", got.session, got.project)
		}
	case <-time.After(time.Second):
		t.Fatal("chat A's tool never ran")
	}
}
