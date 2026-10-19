package agent

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func toolCall(id, name string) provider.ToolCall {
	return provider.ToolCall{
		ID:       id,
		Function: provider.FunctionCall{Name: name, Arguments: `{}`},
	}
}

func TestSplitToolCallsForParallelCap(t *testing.T) {
	calls := []provider.ToolCall{toolCall("1", "a"), toolCall("2", "b"), toolCall("3", "c")}
	execute, deferred := splitToolCallsForParallelCap(calls, 2)
	if len(execute) != 2 || len(deferred) != 1 || deferred[0].ID != "3" {
		t.Fatalf("split = %d execute / %d deferred; want 2 / 1 with id 3", len(execute), len(deferred))
	}
	execute, deferred = splitToolCallsForParallelCap(calls, 0)
	if len(execute) != 3 || len(deferred) != 0 {
		t.Fatalf("zero cap must execute all calls; got %d / %d", len(execute), len(deferred))
	}
}

func TestDeferredToolResultsPairEveryCall(t *testing.T) {
	deferred := []provider.ToolCall{toolCall("3", "c")}
	results := deferredToolResults(deferred, 2, 3)
	if len(results) != 1 || results[0].toolCallID != "3" {
		t.Fatalf("deferred results = %+v; want one result for call 3", results)
	}
	if results[0].err != nil {
		t.Fatal("a deferred call is not an execution error")
	}
}

func TestPadToolResultsFillsMissingCalls(t *testing.T) {
	calls := []provider.ToolCall{toolCall("1", "a"), toolCall("2", "b")}
	results := []toolCallResult{{toolCallID: "2", toolName: "b", result: "ok"}}
	padded := padToolResults(calls, results)
	if len(padded) != 2 {
		t.Fatalf("padded length = %d; want 2", len(padded))
	}
	if padded[0].toolCallID != "1" || padded[0].err == nil {
		t.Fatalf("missing call 1 = %+v; want a synthetic error result", padded[0])
	}
	if padded[1].toolCallID != "2" || padded[1].result != "ok" {
		t.Fatalf("existing call 2 = %+v; want the original result", padded[1])
	}
}
