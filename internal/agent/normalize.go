package agent

import "github.com/fastclaw-ai/fastclaw/internal/provider"

// normalizeForPrompt returns a copy of msgs that satisfies the history
// contract every provider enforces for tool calls:
//
//   - every tool call has exactly one reply, emitted immediately after the
//     call that declared it;
//   - a call with no reply anywhere gets a synthetic "interrupted" reply
//     (provider.StoppedToolResult) in that same position;
//   - replies whose call id is unknown, second and later replies for one call
//     id, and calls re-declared after they were already answered are dropped.
//
// The stored session is left untouched: it is the record of what happened,
// this is the projection the model is allowed to see
// (docs/session-turn-integrity.md, clauses P and O). The function is pure and
// idempotent — normalising an already-normalised history returns it byte for
// byte, which matters because a changing projection would invalidate prompt
// caches on every turn.
func normalizeForPrompt(msgs []provider.Message) []provider.Message {
	if len(msgs) == 0 {
		return nil
	}

	// First reply wins for a given call id: it is the one that sits next to
	// the declaring assistant, and it is the same choice the wire builder
	// makes when it collapses a duplicated reply.
	replyFor := make(map[string]provider.Message, len(msgs))
	for _, m := range msgs {
		if m.Role != "tool" || m.ToolCallID == "" {
			continue
		}
		if _, seen := replyFor[m.ToolCallID]; !seen {
			replyFor[m.ToolCallID] = m
		}
	}

	out := make([]provider.Message, 0, len(msgs))
	answered := make(map[string]bool, len(replyFor))
	for _, m := range msgs {
		if m.Role == "tool" {
			// Re-emitted next to its declaring call below, or dropped when
			// no call ever declared it.
			continue
		}
		if m.Role != "assistant" {
			out = append(out, m)
			continue
		}

		calls := m.EffectiveToolCalls()
		kept := make([]provider.ToolCall, 0, len(calls))
		for _, tc := range calls {
			if tc.ID == "" || answered[tc.ID] {
				continue
			}
			kept = append(kept, tc)
		}
		if len(kept) != len(calls) {
			// A duplicated declaration (the same assistant append landing
			// twice) loses its call, exactly as the wire builder treats an
			// unanswered call. RawAssistant is cleared because serialisers
			// prefer it and it would re-introduce the dropped calls.
			m.ToolCalls = kept
			m.RawAssistant = nil
			calls = kept
		}
		out = append(out, m)

		for _, tc := range calls {
			answered[tc.ID] = true
			if reply, ok := replyFor[tc.ID]; ok {
				out = append(out, reply)
				continue
			}
			out = append(out, provider.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    provider.StoppedToolResult,
			})
		}
	}
	return out
}
