package agent

import (
	"context"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// agentRuntime is one immutable generation of an Agent's hot-reloadable
// state. UpdateConfig, ReloadWorkspaceFiles and the other writers build a
// complete next value and publish it with one atomic store. A running turn
// keeps the pointer it captured at admission, so no turn observes half of
// one generation and half of another.
//
// Do not mutate a published value. Copy it, change the copy, then store the
// copy.
type agentRuntime struct {
	model                string
	provider             provider.Provider
	maxTokens            int
	temperature          float64
	maxToolIterations    int
	maxToolContinues     int
	maxParallelToolCalls int
	subagentTimeout      time.Duration
	promptMode           string
	splitReplies         bool
	memory               *Memory
	ctxBuilder           *ContextBuilder
	sandboxPool          sandbox.ExecutorPool
}

type agentRuntimeKey struct{}

// withAgentRuntime attaches one runtime generation to a turn. Every helper
// that receives this context reads the same generation.
func withAgentRuntime(ctx context.Context, rt *agentRuntime) context.Context {
	return context.WithValue(ctx, agentRuntimeKey{}, rt)
}

func agentRuntimeFrom(ctx context.Context) *agentRuntime {
	rt, _ := ctx.Value(agentRuntimeKey{}).(*agentRuntime)
	return rt
}

// runtimeFor returns the turn's captured generation. Outside a turn it
// returns the current generation, which is safe for one-shot readers such as
// dashboard accessors.
func (a *Agent) runtimeFor(ctx context.Context) *agentRuntime {
	if rt := agentRuntimeFrom(ctx); rt != nil {
		return rt
	}
	return a.currentRuntime()
}

// currentRuntime returns the published generation. A zero-value or
// test-constructed Agent has no published generation yet, so the first call
// publishes one from its construction fields.
func (a *Agent) currentRuntime() *agentRuntime {
	if rt := a.runtime.Load(); rt != nil {
		return rt
	}
	rt := a.snapshotFromFields()
	if a.runtime.CompareAndSwap(nil, rt) {
		return rt
	}
	return a.runtime.Load()
}

func (a *Agent) snapshotFromFields() *agentRuntime {
	return &agentRuntime{
		model:                a.model,
		provider:             a.provider,
		maxTokens:            a.maxTokens,
		temperature:          a.temperature,
		maxToolIterations:    a.maxToolIterations,
		maxToolContinues:     a.maxToolContinues,
		maxParallelToolCalls: a.maxParallelToolCalls,
		subagentTimeout:      a.subagentTimeout,
		promptMode:           a.promptMode,
		splitReplies:         a.splitReplies,
		memory:               a.memory,
		ctxBuilder:           a.ctxBuilder,
	}
}

func (a *Agent) publishRuntimeFromFields() *agentRuntime {
	rt := a.snapshotFromFields()
	a.runtime.Store(rt)
	return rt
}

func (a *Agent) publishRuntime(next *agentRuntime) {
	a.runtime.Store(next)
}

// mutateRuntime is the one write path for hot reload. The lock serializes
// control-plane writers so none of them publishes a copy that predates a peer's
// update. The mutation runs on a private copy; readers keep the old generation
// until the store publishes the new one.
func (a *Agent) mutateRuntime(mutate func(next *agentRuntime)) {
	a.runtimeWriteMu.Lock()
	defer a.runtimeWriteMu.Unlock()
	next := *a.currentRuntime()
	mutate(&next)
	a.publishRuntime(&next)
}

// EnableChatterTimezone installs the chatter-timezone resolver on a private
// builder copy. The resolver captures the same memory object as the
// generation it belongs to.
func (a *Agent) EnableChatterTimezone() {
	a.mutateRuntime(func(next *agentRuntime) {
		if next.ctxBuilder == nil {
			return
		}
		cb := next.ctxBuilder.Clone()
		mem := next.memory
		cb.SetTimezoneResolver(chatterTimezone{agent: a, memory: mem}.Resolve)
		next.ctxBuilder = cb
	})
}

// chatterTimezone resolves a chatter's location from one memory generation.
// Both timezone writers install this named resolver instead of repeating a
// function literal, so the dependency on Agent plus Memory is explicit.
type chatterTimezone struct {
	agent  *Agent
	memory *Memory
}

// Resolve returns the chatter's effective location.
func (tz chatterTimezone) Resolve(chatterUID string) *time.Location {
	return tz.agent.chatterLocationFor(tz.memory, chatterUID)
}
