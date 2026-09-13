package agent

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/session"
)

func toolCallIDs(msgs []provider.Message) []string {
	var ids []string
	for _, m := range msgs {
		if m.Role == "tool" {
			ids = append(ids, m.ToolCallID)
		}
	}
	return ids
}

// padOrphanToolResults runs from a turn's defer and must only settle the
// tool_use ids THAT TURN appended. It used to scan the session for the
// last assistant carrying tool_calls and pad every unresolved id it found
// there — so when a second turn started while an earlier turn still had a
// tool in flight, the newer turn padded the older turn's tool_use ids.
// The older turn's real results then landed right after the pads, leaving
// two replies for one tool_call_id in the session (which DeepSeek rejects
// with the 400 this whole fix exists for, and which then poisons every
// later turn on that session — cron ticks included).
func TestPadOrphanToolResultsLeavesOtherTurnsToolUseAlone(t *testing.T) {
	mgr := session.NewManager(t.TempDir())
	sess := mgr.Get("web", "", "chat-1", "")

	// An earlier turn still waiting on its tools (call_other never resolved).
	sess.Append(provider.Message{Role: "user", Content: "earlier turn"})
	sess.Append(provider.Message{
		Role:      "assistant",
		ToolCalls: []provider.ToolCall{{ID: "call_other", Function: provider.FunctionCall{Name: "exec"}}},
	})

	// This turn: one call answered, one still in flight when it exited.
	sess.Append(provider.Message{
		Role: "assistant",
		ToolCalls: []provider.ToolCall{
			{ID: "call_mine_done", Function: provider.FunctionCall{Name: "write_file"}},
			{ID: "call_mine_open", Function: provider.FunctionCall{Name: "exec"}},
		},
	})
	sess.Append(provider.Message{Role: "tool", ToolCallID: "call_mine_done", Content: "ok"})

	padOrphanToolResults(sess, []string{"call_mine_done", "call_mine_open"})

	ids := toolCallIDs(sess.GetMessages())
	for _, id := range ids {
		if id == "call_other" {
			t.Fatalf("padded another turn's tool_use: %v", ids)
		}
	}
	var padded int
	for _, id := range ids {
		if id == "call_mine_open" {
			padded++
		}
	}
	if padded != 1 {
		t.Fatalf("call_mine_open padded %d times; want exactly 1 (ids=%v)", padded, ids)
	}
	if len(ids) != 2 {
		t.Fatalf("tool replies = %v; want the answered call plus one pad", ids)
	}
}

// A turn that reported no tool calls (plain reply, plan mode, a turn whose
// assistant message was never persisted) must not pad anything — pads there
// would be answers to tool calls the model never made.
func TestPadOrphanToolResultsNoTurnIDsIsNoop(t *testing.T) {
	mgr := session.NewManager(t.TempDir())
	sess := mgr.Get("web", "", "chat-2", "")

	sess.Append(provider.Message{Role: "user", Content: "hi"})
	sess.Append(provider.Message{
		Role:      "assistant",
		ToolCalls: []provider.ToolCall{{ID: "call_stale", Function: provider.FunctionCall{Name: "exec"}}},
	})

	padOrphanToolResults(sess, nil)

	if ids := toolCallIDs(sess.GetMessages()); len(ids) != 0 {
		t.Fatalf("padded with no turn tool_use ids: %v", ids)
	}
}

// Padding twice must not answer the same call twice: the second pass sees the
// reply the first pass wrote and stops. This is the property that keeps a
// retried/duplicated defer from re-creating the production incident's
// duplicate replies.
func TestPadOrphanToolResultsIsIdempotent(t *testing.T) {
	mgr := session.NewManager(t.TempDir())
	sess := mgr.Get("web", "", "chat-3", "")

	sess.Append(provider.Message{
		Role:      "assistant",
		ToolCalls: []provider.ToolCall{{ID: "call_once", Function: provider.FunctionCall{Name: "exec"}}},
	})

	padOrphanToolResults(sess, []string{"call_once"})
	padOrphanToolResults(sess, []string{"call_once"})

	ids := toolCallIDs(sess.GetMessages())
	if len(ids) != 1 || ids[0] != "call_once" {
		t.Fatalf("tool replies = %v; want exactly one pad for call_once", ids)
	}
	if got := sess.GetMessages()[1].Content; got != provider.StoppedToolResult {
		t.Fatalf("pad content = %q; want %q", got, provider.StoppedToolResult)
	}
}
