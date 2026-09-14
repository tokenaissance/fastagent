package agent

// The wall budget used to end a sub-agent with ("", err): everything it had
// fetched was discarded and the parent received only "ran out of budget". These
// tests pin the two halves of the fix — the budget is resolvable, and an expiry
// now hands back what the sub-agent had already produced.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// scriptedProvider plays a fixed sequence of Chat answers so a sub-agent can be
// walked into its budget expiry deterministically. ChatStream satisfies the
// interface and is never used by the sub-agent loop.
type scriptedProvider struct {
	mu      sync.Mutex
	calls   int
	toolLen []int
	respond func(ctx context.Context, call int, tools []provider.Tool) (*provider.Response, error)
}

func (p *scriptedProvider) Chat(ctx context.Context, _ []provider.Message, toolDefs []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.toolLen = append(p.toolLen, len(toolDefs))
	p.mu.Unlock()
	return p.respond(ctx, n, toolDefs)
}

func (p *scriptedProvider) ChatStream(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// sawToolsFreeRound reports whether the n-th call ran with tools disabled —
// i.e. whether it was the salvage round.
func (p *scriptedProvider) sawToolsFreeRound(n int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return n <= len(p.toolLen) && p.toolLen[n-1] == 0
}

func newSubagentTestAgent(t *testing.T, p provider.Provider) *Agent {
	t.Helper()
	mem := NewMemory(t.TempDir())
	return &Agent{
		name:              "sub-agent",
		ownerUserID:       "u_owner",
		provider:          p,
		registry:          tools.NewRegistry("", ""),
		memory:            mem,
		ctxBuilder:        NewContextBuilder(t.TempDir(), mem, ""),
		hooks:             NewHookRegistry(),
		messageBus:        bus.New(),
		model:             "fake-model",
		maxTokens:         256,
		temperature:       0.7,
		maxToolIterations: 3,
		engine:            newSDKEngine("sess-subagent-test"),
	}
}

// The budget is configuration, so resolution is: the caller's request, then
// what the agent was configured with, then the built-in. Where the configured
// value comes from (system → user → agent scope) is the config layer's job and
// is covered by the merge tests there.
//
// A caller with no deadline has no turn clock to clamp against, which is what
// these three cases exercise — the ceiling itself is pinned by the turn-budget
// tests.
func TestSubagentWallBudgetResolution(t *testing.T) {
	t.Run("an explicit request wins", func(t *testing.T) {
		a := newSubagentTestAgent(t, &scriptedProvider{})
		a.subagentTimeout = 20 * time.Minute
		got, _, err := a.subagentWallBudget(context.Background(), 45*time.Second)
		if err != nil {
			t.Fatalf("no deadline must not refuse: %v", err)
		}
		if got != 45*time.Second {
			t.Fatalf("budget = %s, want the requested 45s", got)
		}
	})
	t.Run("the agent's configured default is used", func(t *testing.T) {
		a := newSubagentTestAgent(t, &scriptedProvider{})
		a.subagentTimeout = 30 * time.Minute
		got, _, err := a.subagentWallBudget(context.Background(), 0)
		if err != nil {
			t.Fatalf("no deadline must not refuse: %v", err)
		}
		if got != 30*time.Minute {
			t.Fatalf("budget = %s, want the configured 30m", got)
		}
	})
	t.Run("an unconfigured agent falls back to the built-in", func(t *testing.T) {
		a := newSubagentTestAgent(t, &scriptedProvider{})
		got, _, err := a.subagentWallBudget(context.Background(), 0)
		if err != nil {
			t.Fatalf("no deadline must not refuse: %v", err)
		}
		if got != subagentDefaultTimeout {
			t.Fatalf("budget = %s, want %s", got, subagentDefaultTimeout)
		}
	})
}

// The budget expires with material already gathered: the sub-agent gets one
// tools-free round to write it down, and that text comes back WITH the reason
// it is partial.
func TestRunSubagentSalvagesWhatItGathered(t *testing.T) {
	prov := &scriptedProvider{respond: func(_ context.Context, call int, _ []provider.Tool) (*provider.Response, error) {
		switch call {
		case 1:
			// A round that both writes prose and reaches for a tool — the shape
			// that used to lose everything at expiry.
			return &provider.Response{
				Content:   "DRAFT: pricing page says 60 QCC/hour",
				ToolCalls: []provider.ToolCall{{ID: "t1", Function: provider.FunctionCall{Name: "no_such_tool", Arguments: "{}"}}},
			}, nil
		case 2:
			return nil, context.DeadlineExceeded
		default:
			return &provider.Response{Content: "PARTIAL BRIEF: 1) plans … 2) compute …"}, nil
		}
	}}
	a := newSubagentTestAgent(t, prov)

	out, err := a.RunSubagent(context.Background(), tools.SubagentRequest{Task: "research QuantConnect", MaxIterations: 3, WallTimeout: time.Minute})

	if err == nil {
		t.Fatal("an expired budget must still surface as an error")
	}
	if !strings.Contains(err.Error(), "wall-time budget") || !strings.Contains(err.Error(), "1m0s") {
		t.Fatalf("error should name the budget that expired, got %q", err)
	}
	if !strings.Contains(out, "PARTIAL BRIEF") {
		t.Fatalf("salvaged output = %q, want the final tools-free round's text", out)
	}
	if !prov.sawToolsFreeRound(3) {
		t.Fatalf("the salvage round must run with tools disabled, tool-counts=%v", prov.toolLen)
	}
}

// If the salvage round itself produces nothing, the prose from earlier rounds
// is still better than an empty result.
func TestRunSubagentSalvageFallsBackToEarlierProse(t *testing.T) {
	prov := &scriptedProvider{respond: func(_ context.Context, call int, _ []provider.Tool) (*provider.Response, error) {
		switch call {
		case 1:
			return &provider.Response{
				Content:   "DRAFT: tiers table started",
				ToolCalls: []provider.ToolCall{{ID: "t1", Function: provider.FunctionCall{Name: "no_such_tool", Arguments: "{}"}}},
			}, nil
		case 2:
			return nil, context.DeadlineExceeded
		default:
			return nil, errors.New("salvage round failed too")
		}
	}}
	a := newSubagentTestAgent(t, prov)

	out, err := a.RunSubagent(context.Background(), tools.SubagentRequest{Task: "research QuantConnect", MaxIterations: 3, WallTimeout: time.Minute})

	if err == nil {
		t.Fatal("want the budget error")
	}
	if !strings.Contains(out, "DRAFT: tiers table started") {
		t.Fatalf("output = %q, want the earlier round's prose", out)
	}
}

// A cancelled parent means nobody is waiting: do not spend another round.
func TestRunSubagentParentCancelSkipsSalvage(t *testing.T) {
	prov := &scriptedProvider{respond: func(ctx context.Context, _ int, _ []provider.Tool) (*provider.Response, error) {
		return nil, ctx.Err()
	}}
	a := newSubagentTestAgent(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out, err := a.RunSubagent(ctx, tools.SubagentRequest{Task: "research QuantConnect", MaxIterations: 3, WallTimeout: time.Minute})

	if err == nil || !strings.Contains(err.Error(), "cancelled with its parent") {
		t.Fatalf("err = %v, want the parent-cancel framing", err)
	}
	if out != "" {
		t.Fatalf("out = %q, want empty", out)
	}
	if got := prov.callCount(); got != 1 {
		t.Fatalf("Chat calls = %d, want 1: a cancelled parent must not trigger a salvage round", got)
	}
}
