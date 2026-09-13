package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToAPIMessagesDropsDanglingToolReplies covers the inverse orphan:
// "Messages with role 'tool' must be a response to a preceding message
// with 'tool_calls'". This happens when the assistant half of a pair is
// dropped (compaction/truncation) or a hung exec's late tool result lands
// out of order. The wire build must drop the dangling tool reply instead
// of shipping a request the provider rejects.
func TestToAPIMessagesDropsDanglingToolReplies(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "run it"},
		// No preceding assistant declared this id — dangling tool reply.
		{Role: "tool", ToolCallID: "call_dangling", Content: "late result"},
		{Role: "assistant", Content: "ok"},
	}
	wire := toAPIMessages(msgs)
	if len(wire) != 2 {
		t.Fatalf("wire len = %d; want 2 (dangling tool dropped)\n%+v", len(wire), wire)
	}
	var roles []string
	for _, raw := range wire {
		var am apiMessage
		if err := json.Unmarshal(raw, &am); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		roles = append(roles, am.Role)
	}
	if strings.Join(roles, ",") != "user,assistant" {
		t.Fatalf("roles after strip = %v; want user,assistant", roles)
	}
}

// TestToAPIMessagesKeepsAnsweredPairAndDropsStrayReply ensures the valid
// assistant(tool_calls)→tool pair survives while a stray tool reply after
// a later user turn is removed.
func TestToAPIMessagesKeepsAnsweredPairAndDropsStrayReply(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "run"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_ok", Type: "function", Function: FunctionCall{Name: "exec", Arguments: `{"command":"sleep 1"}`}}}},
		{Role: "tool", ToolCallID: "call_ok", Content: "done"},
		{Role: "user", Content: "again"},
		{Role: "tool", ToolCallID: "call_stray", Content: "too late"},
	}
	_, orphanTool := findOrphanToolCalls(msgs)
	if orphanTool[2] {
		t.Fatal("answered tool reply must not be flagged")
	}
	if !orphanTool[4] {
		t.Fatal("stray tool reply after user turn must be flagged")
	}
}

// TestToAPIMessagesDropsDuplicateToolReplies pins the production incident
// this file's sanitizer already claims to cover: a turn killed while a tool
// was in flight (the 300s task-queue timeout) writes its synthetic
// "stopped" reply via padOrphanToolResults, and the interrupted tool's real
// result is appended afterwards for the SAME tool_call_id. Both replies sit
// directly after the declaring assistant, so neither the orphan-assistant
// nor the dangling-reply scan fires and the request ships two answers for
// one tool_call_id. DeepSeek-style validators reject that with
// "Messages with role 'tool' must be a response to a preceding message with
// 'tool_calls'" — the second reply no longer answers an OPEN tool call.
// Exactly one reply per tool_call_id may leave the wire builder.
func TestToAPIMessagesDropsDuplicateToolReplies(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "resume the job"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "call_list", Type: "function", Function: FunctionCall{Name: "list_cron_jobs"}},
			{ID: "call_exec", Type: "function", Function: FunctionCall{Name: "exec"}},
		}},
		{Role: "tool", ToolCallID: "call_list", Name: "list_cron_jobs", Content: StoppedToolResult},
		{Role: "tool", ToolCallID: "call_exec", Name: "exec", Content: StoppedToolResult},
		{Role: "tool", ToolCallID: "call_list", Name: "list_cron_jobs", Content: "3 jobs"},
		{Role: "tool", ToolCallID: "call_exec", Name: "exec", Content: "batch restarted"},
	}
	wire := toAPIMessages(msgs)
	if len(wire) != 4 {
		t.Fatalf("wire len = %d; want 4 (user, assistant, one reply per tool_call_id)\n%+v", len(wire), wire)
	}
	var got []string
	for _, raw := range wire {
		var am apiMessage
		if err := json.Unmarshal(raw, &am); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if am.Role == "tool" {
			got = append(got, am.ToolCallID)
		}
	}
	if strings.Join(got, ",") != "call_list,call_exec" {
		t.Fatalf("tool replies = %v; want one reply per id in declared order", got)
	}
}

// TestToAPIMessagesDropsLateDuplicateAcrossTurns covers the same defect
// when the duplicate is separated from its (already answered) parent by
// other turns — the shape a late result lands in after the session moved
// on. The backward scan happily finds the older assistant, so without an
// explicit "answered once" rule the extra reply ships.
func TestToAPIMessagesDropsLateDuplicateAcrossTurns(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "run"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_once", Type: "function", Function: FunctionCall{Name: "exec"}}}},
		{Role: "tool", ToolCallID: "call_once", Name: "exec", Content: "done"},
		{Role: "user", Content: "and now?"},
		{Role: "assistant", Content: "here is the summary"},
		{Role: "tool", ToolCallID: "call_once", Name: "exec", Content: "late duplicate"},
	}
	wire := toAPIMessages(msgs)
	if len(wire) != 5 {
		t.Fatalf("wire len = %d; want 5 (late duplicate dropped)\n%+v", len(wire), wire)
	}
	var am apiMessage
	if err := json.Unmarshal(wire[4], &am); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if am.Role != "assistant" {
		t.Fatalf("last wire message role = %q; want assistant (duplicate tool reply dropped)", am.Role)
	}
}
