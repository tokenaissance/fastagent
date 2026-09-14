package agent

// A sub-agent's wall budget must not be the one place that kills work in flight.
//
// The main loop has given in-flight tools a grace window since 22d361d
// (loop.go wraps its tool rounds in toolGraceContext), and finalizeSubagent
// exists to salvage an in-flight MODEL round for the same reason. The sub-agent's
// tool round called executeToolsConcurrently with the raw budget ctx, so it was
// the single path where an expiry cut a tool off with nothing to show: the tool
// saw a cancelled ctx, the round recorded an error, and the parent's only clue
// was "context canceled" far downstream (a sandbox exec reporting
// "e2b exec body read: context canceled" is exactly this shape).

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func TestSubagentBudgetExpiryLetsInFlightToolFinish(t *testing.T) {
	var toolFinished atomic.Bool
	prov := &scriptedProvider{respond: func(_ context.Context, call int, _ []provider.Tool) (*provider.Response, error) {
		switch call {
		case 1:
			return &provider.Response{ToolCalls: []provider.ToolCall{{
				ID: "t1", Function: provider.FunctionCall{Name: "slow_tool", Arguments: "{}"},
			}}}, nil
		default:
			return nil, context.DeadlineExceeded
		}
	}}
	a := newSubagentTestAgent(t, prov)
	// Budget expires quickly, the tool finishes well inside a generous grace:
	// the contract is what is under test, so the margins absorb a loaded machine.
	a.toolGrace = 30 * time.Second
	a.registry.Register("slow_tool", "test tool", nil, func(ctx context.Context, _ json.RawMessage) (string, error) {
		select {
		case <-time.After(800 * time.Millisecond):
			toolFinished.Store(true)
			return "real tool result", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})

	_, err := a.RunSubagent(context.Background(), tools.SubagentRequest{
		Task: "run the slow tool", MaxIterations: 3, WallTimeout: 200 * time.Millisecond,
	})

	// The budget still expires — the sub-agent reports it, and the grace does not
	// turn the expiry into a success.
	if err == nil || !strings.Contains(err.Error(), "wall-time budget") {
		t.Fatalf("err = %v; want the budget expiry reported", err)
	}
	if !toolFinished.Load() {
		t.Fatal("the in-flight tool was cancelled with the budget; it should have had the grace window")
	}
}
