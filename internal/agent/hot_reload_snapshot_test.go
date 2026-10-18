package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

type recordedModelCall struct {
	model     string
	maxTokens int
}

type recordingProvider struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu    sync.Mutex
	calls []recordedModelCall
}

func (p *recordingProvider) record(model string, maxTokens int) {
	p.mu.Lock()
	p.calls = append(p.calls, recordedModelCall{model: model, maxTokens: maxTokens})
	p.mu.Unlock()
}

func (p *recordingProvider) snapshot() []recordedModelCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedModelCall(nil), p.calls...)
}

func (p *recordingProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool, model string, maxTokens int, _ float64) (*provider.Response, error) {
	p.record(model, maxTokens)
	return &provider.Response{Content: "ok"}, nil
}

func (p *recordingProvider) ChatStream(_ context.Context, _ []provider.Message, _ []provider.Tool, model string, maxTokens int, _ float64) (*provider.StreamReader, error) {
	p.record(model, maxTokens)
	if p.entered != nil {
		p.once.Do(func() { close(p.entered) })
		<-p.release
	}
	ch := make(chan provider.StreamChunk, 2)
	ch <- provider.StreamChunk{Content: "ok", Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

// TestInFlightTurnKeepsOneRuntimeGeneration parks a turn inside its first
// model call, publishes a new config, then releases the turn. The in-flight
// call must still use the generation it captured at admission. The next turn
// must use the new generation.
func TestInFlightTurnKeepsOneRuntimeGeneration(t *testing.T) {
	a, _ := newGateAgent(t)
	prov := &recordingProvider{entered: make(chan struct{}), release: make(chan struct{})}
	a.provider = prov
	a.model = "model-a"
	a.maxTokens = 100

	first := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-a", Text: "first"}
	done := make(chan struct{})
	go func() {
		a.HandleMessage(context.Background(), first)
		close(done)
	}()
	<-prov.entered

	a.UpdateConfig(config.ResolvedAgent{
		Model:       "model-b",
		MaxTokens:   200,
		Temperature: 0.2,
	})
	close(prov.release)
	<-done

	calls := prov.snapshot()
	if len(calls) == 0 {
		t.Fatal("the first turn never called the provider")
	}
	if calls[0].model != "model-a" || calls[0].maxTokens != 100 {
		t.Fatalf("in-flight call used a mixed generation: %+v; want model-a/100", calls[0])
	}

	second := bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-b", Text: "second"}
	a.HandleMessage(context.Background(), second)
	calls = prov.snapshot()
	last := calls[len(calls)-1]
	if last.model != "model-b" || last.maxTokens != 200 {
		t.Fatalf("next turn did not use the new generation: %+v; want model-b/200", last)
	}
}

// TestPublishedGenerationIsImmutable pins copy-on-write for the two writers
// that used to mutate shared fields directly: UpdateConfig and SetGroupContext.
func TestPublishedGenerationIsImmutable(t *testing.T) {
	a, _ := newGateAgent(t)
	a.ctxBuilder.SetPromptMode(config.PromptModeAgent)
	a.publishRuntimeFromFields()
	old := a.currentRuntime()

	a.UpdateConfig(config.ResolvedAgent{
		Model:       "model-b",
		MaxTokens:   200,
		Temperature: 0.2,
		PromptMode:  config.PromptModeChatbot,
		Sandbox:     config.SandboxCfg{Enabled: true},
	})
	gc := &GroupContext{}
	a.SetGroupContext(gc)

	if old.model != "fake-model" || old.maxTokens != 256 {
		t.Fatalf("published generation changed in place: %+v", old)
	}
	if old.ctxBuilder.promptMode != config.PromptModeAgent || old.ctxBuilder.sandboxEnabled {
		t.Fatalf("published builder changed in place: mode=%q sandbox=%v",
			old.ctxBuilder.promptMode, old.ctxBuilder.sandboxEnabled)
	}
	if old.ctxBuilder.groupCtx != nil {
		t.Fatal("SetGroupContext mutated the published builder")
	}

	next := a.currentRuntime()
	if next.model != "model-b" || next.maxTokens != 200 {
		t.Fatalf("new generation was not published: %+v", next)
	}
	if next.ctxBuilder.promptMode != config.PromptModeChatbot || !next.ctxBuilder.sandboxEnabled {
		t.Fatalf("new builder is incomplete: mode=%q sandbox=%v",
			next.ctxBuilder.promptMode, next.ctxBuilder.sandboxEnabled)
	}
	if next.ctxBuilder.groupCtx != gc {
		t.Fatal("new builder did not receive the group context")
	}
}

// TestConcurrentTurnsAndUpdatesNeverMixGenerations runs turns while config
// updates alternate between two complete generations. Every model call must
// see one pair, never (model-a, 200) or (model-b, 100).
func TestConcurrentTurnsAndUpdatesNeverMixGenerations(t *testing.T) {
	a, _ := newGateAgent(t)
	prov := &recordingProvider{}
	a.provider = prov
	a.model = "model-a"
	a.maxTokens = 100

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := bus.InboundMessage{
				Channel: "web",
				UserID:  "u_owner",
				ChatID:  fmt.Sprintf("chat-%d", i),
				Text:    "hello",
			}
			a.HandleMessage(ctx, msg)
		}(i)
	}
	for i := 0; i < 24; i++ {
		if i%2 == 0 {
			a.UpdateConfig(config.ResolvedAgent{Model: "model-b", MaxTokens: 200, Temperature: 0.2})
		} else {
			a.UpdateConfig(config.ResolvedAgent{Model: "model-a", MaxTokens: 100, Temperature: 0.7})
		}
	}
	wg.Wait()

	calls := prov.snapshot()
	if len(calls) == 0 {
		t.Fatal("no model calls were recorded")
	}
	for _, call := range calls {
		valid := (call.model == "model-a" && call.maxTokens == 100) ||
			(call.model == "model-b" && call.maxTokens == 200)
		if !valid {
			t.Fatalf("model call mixed two generations: %+v", call)
		}
	}
}

// TestRuntimeWritersAreSerialized pins the writer boundary: concurrent
// read-copy-publish updates must all survive. Without the lock the last store
// wins on a stale copy and increments disappear.
func TestRuntimeWritersAreSerialized(t *testing.T) {
	a, _ := newGateAgent(t)
	a.publishRuntimeFromFields()
	base := a.currentRuntime().maxToolIterations
	const writers = 64
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.mutateRuntime(func(rt *agentRuntime) { rt.maxToolIterations++ })
		}()
	}
	wg.Wait()
	if got, want := a.currentRuntime().maxToolIterations, base+writers; got != want {
		t.Fatalf("lost updates: maxToolIterations=%d, want %d", got, want)
	}
}
