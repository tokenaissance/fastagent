package agent

// The declaration has to survive the REAL wire, not just the tool layer's own
// test doubles (docs/sandbox-scope-leak.md §9.5).
//
// In production the registry is bound to whatever the sandbox pool hands back,
// which for the lifecycle pool is a lazy proxy rather than the executor itself —
// and the agent loop strips the sandbox marker with strings.TrimPrefix only
// while it sits on line 1 of the result. Both facts are invisible to a test
// that calls the tool closure directly, and both decide whether the model ever
// sees the sentence this whole slice exists to deliver.

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// stubExecutor is a sandbox executor standing in for E2B: it reports the
// Policy C state and nothing else interesting.
type stubExecutor struct {
	unhydrated bool
}

func (s *stubExecutor) Exec(context.Context, string, time.Duration) (string, error) {
	return "", nil
}
func (s *stubExecutor) ReadFile(context.Context, string) (string, error) { return "", nil }
func (s *stubExecutor) WriteFile(context.Context, string, string) (string, error) {
	return "", nil
}
func (s *stubExecutor) ListDir(context.Context, string) (string, error) { return "", nil }
func (s *stubExecutor) Backend() string                                 { return "stub" }
func (s *stubExecutor) Close() error                                    { return nil }
func (s *stubExecutor) WorkspaceUnhydrated() bool                       { return s.unhydrated }

// stubPool is the inner pool the lifecycle pool wraps.
type stubPool struct {
	mu   sync.Mutex
	live map[string]*stubExecutor
	// unhydrated arms every instance this pool creates.
	unhydrated atomic.Bool
}

func (p *stubPool) Get(_ context.Context, agentID, projectID, sessionID string) (sandbox.Executor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.live == nil {
		p.live = map[string]*stubExecutor{}
	}
	k := agentID + "|" + projectID + "|" + sessionID
	if ex, ok := p.live[k]; ok {
		return ex, nil
	}
	ex := &stubExecutor{unhydrated: p.unhydrated.Load()}
	p.live[k] = ex
	return ex, nil
}

func (p *stubPool) Release(agentID, projectID, sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.live, agentID+"|"+projectID+"|"+sessionID)
	return nil
}

func (p *stubPool) CloseAll()       { p.mu.Lock(); p.live = nil; p.mu.Unlock() }
func (p *stubPool) Backend() string { return "stub" }

func TestWorkspaceSignalSurvivesTheLifecycleProxyAndTheMetaStrip(t *testing.T) {
	ctx := context.Background()
	inner := &stubPool{}
	inner.unhydrated.Store(true)
	lp := sandbox.NewLifecyclePool(inner, 0, 0)
	lp.Start()
	defer lp.CloseAll()

	// This is the production shape: bindSession hands the registry the pool's
	// proxy, not an executor.
	ex, err := lp.Get(ctx, "agent-signal", "", "")
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(t.TempDir(), t.TempDir())
	defer reg.Close()
	reg.SetExecutor(ex)

	out, err := reg.Execute(ctx, "exec", `{"command":"ls /workspace"}`)
	if err != nil {
		t.Fatal(err)
	}

	// What the model receives: the loop strips the marker and its metadata
	// first, then the content is what the provider sees.
	content, meta := extractToolMeta(out)
	if meta["sandbox"] != true {
		t.Fatalf("the sandbox marker was lost — the declaration was put in front of it:\n%q", out)
	}
	if !strings.HasPrefix(content, "[workspace not hydrated:") {
		t.Fatalf("the model does not see the declaration:\n%q", content)
	}
}

// The mirror image, through the same wire: a healthy scope must arrive with no
// declaration at all, or the sentence becomes wallpaper.
func TestWorkspaceSignalIsAbsentThroughTheLifecycleProxy(t *testing.T) {
	ctx := context.Background()
	inner := &stubPool{}
	lp := sandbox.NewLifecyclePool(inner, 0, 0)
	lp.Start()
	defer lp.CloseAll()

	ex, err := lp.Get(ctx, "agent-clean", "", "")
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(t.TempDir(), t.TempDir())
	defer reg.Close()
	reg.SetExecutor(ex)

	out, err := reg.Execute(ctx, "exec", `{"command":"ls /workspace"}`)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := extractToolMeta(out)
	if strings.Contains(content, "not hydrated") {
		t.Fatalf("a hydrated scope was declared unhydrated:\n%q", content)
	}
}
