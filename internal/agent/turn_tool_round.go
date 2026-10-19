package agent

import (
	"context"
	"fmt"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// toolRoundContext bounds one tool round: the turn deadline travels as a
// value, the grace window lets an in-flight call finish, and the lease watcher
// keeps a stop request reachable while a tool is parked.
func (a *Agent) toolRoundContext(
	ctx context.Context,
	lease *turnLeaseGuard,
) (context.Context, func()) {
	toolCtx, endGrace := toolGraceContext(ctx, a.graceWindow())
	toolCtx = withTurnDeadline(toolCtx, ctx)
	toolCtx, endWatch := lease.watchCancel(toolCtx, a.turnLeaseWatchInterval())
	return toolCtx, func() {
		endWatch()
		endGrace()
	}
}

// splitToolCallsForParallelCap returns the calls to execute this round and the
// calls deferred to the next one. A zero cap means unlimited.
func splitToolCallsForParallelCap(
	calls []provider.ToolCall,
	cap int,
) ([]provider.ToolCall, []provider.ToolCall) {
	if cap <= 0 || len(calls) <= cap {
		return calls, nil
	}
	return calls[:cap], calls[cap:]
}

// deferredToolResults pairs every deferred call with a synthetic result so the
// model never sees an orphan tool_use id.
func deferredToolResults(
	deferred []provider.ToolCall,
	cap, emitted int,
) []toolCallResult {
	out := make([]toolCallResult, 0, len(deferred))
	for _, tc := range deferred {
		out = append(out, toolCallResult{
			toolCallID: tc.ID,
			toolName:   tc.Function.Name,
			result: fmt.Sprintf(
				"Deferred — this turn's parallel-tool cap is %d, and you emitted %d. Re-issue this exact call next round if you still need it; you'll have the other tools' results to inform the decision then.",
				cap, emitted,
			),
		})
	}
	return out
}

// padToolResults is the defensive backstop for an executor that returned fewer
// results than calls. Every tool_use id must have a paired tool_result.
func padToolResults(calls []provider.ToolCall, results []toolCallResult) []toolCallResult {
	if len(results) >= len(calls) {
		return results
	}
	byID := make(map[string]toolCallResult, len(results))
	for _, r := range results {
		byID[r.toolCallID] = r
	}
	padded := make([]toolCallResult, len(calls))
	for i, tc := range calls {
		if r, ok := byID[tc.ID]; ok {
			padded[i] = r
			continue
		}
		padded[i] = toolCallResult{
			toolCallID: tc.ID,
			toolName:   tc.Function.Name,
			result:     "tool execution did not return a result",
			err:        fmt.Errorf("missing executor response for %s", tc.ID),
		}
	}
	return padded
}
