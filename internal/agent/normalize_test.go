package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func nUser(s string) provider.Message { return provider.Message{Role: "user", Content: s} }

func nCall(ids ...string) provider.Message {
	m := provider.Message{Role: "assistant"}
	for _, id := range ids {
		m.ToolCalls = append(m.ToolCalls, provider.ToolCall{
			ID:       id,
			Type:     "function",
			Function: provider.FunctionCall{Name: "tool_" + id},
		})
	}
	return m
}

func nReply(id, content string) provider.Message {
	return provider.Message{Role: "tool", ToolCallID: id, Name: "tool_" + id, Content: content}
}

// normalizeForPrompt is the single place that guarantees the history
// contract every provider enforces: one reply per call, every reply
// belonging to a call (docs/session-turn-integrity.md, clause P). The
// stored record stays truthful; only the projection is repaired — including
// the synthetic reply for a call an interrupted turn left open (clause T/Q4).
//
// Duplicates are therefore a legacy shape: only histories written before
// Q4 can carry a synthetic reply in storage next to a real one.

// TestProjectionDoesNotClaimInterruptedWithoutEvidence pins A2.2: with no
// lease wired the projection has no evidence that the owning turn died, so the
// word "interrupted" must not appear — the old wording asserted it for every
// open call, and on 09-18 the model read that claim for two sub-tasks that were
// still running on a peer and re-issued them.
//
// Falsification: put provider.StoppedToolResult back into normalizeForPrompt
// and this test goes red on both assertions.
func TestProjectionDoesNotClaimInterruptedWithoutEvidence(t *testing.T) {
	projected := normalizeForPrompt([]provider.Message{nCall("A")})
	if len(projected) != 2 {
		t.Fatalf("projection length = %d, want 2 (the call plus its synthetic reply)", len(projected))
	}
	got := projected[1].Content
	if strings.Contains(strings.ToLower(got), "interrupt") {
		t.Fatalf("the projection asserts an interruption without evidence: %q", got)
	}
	if got != provider.NoReplyUnknownResult {
		t.Fatalf("projection sentence = %q; want the no-fact sentence %q", got, provider.NoReplyUnknownResult)
	}
}

func TestNormalizeForPromptShapes(t *testing.T) {
	stopped := provider.StoppedToolResult
	noReply := provider.NoReplyUnknownResult

	cases := []struct {
		name string
		in   []provider.Message
		want []provider.Message
	}{
		{
			name: "well-formed history is unchanged",
			in:   []provider.Message{nUser("go"), nCall("A"), nReply("A", "done"), nUser("next"), {Role: "assistant", Content: "ok"}},
			want: []provider.Message{nUser("go"), nCall("A"), nReply("A", "done"), nUser("next"), {Role: "assistant", Content: "ok"}},
		},
		{
			name: "legacy synthetic reply plus real result collapses to one",
			in:   []provider.Message{nUser("go"), nCall("A"), nReply("A", stopped), nReply("A", "real")},
			want: []provider.Message{nUser("go"), nCall("A"), nReply("A", stopped)},
		},
		{
			name: "incident shape: two calls answered twice each",
			in: []provider.Message{
				nUser("resume"), nCall("A", "B"),
				nReply("A", stopped), nReply("B", stopped), nReply("A", "real"), nReply("B", "real"),
			},
			want: []provider.Message{nUser("resume"), nCall("A", "B"), nReply("A", stopped), nReply("B", stopped)},
		},
		{
			name: "reply with no declaring call is dropped",
			in:   []provider.Message{nUser("go"), nReply("ghost", "late")},
			want: []provider.Message{nUser("go")},
		},
		{
			name: "unanswered call gets a synthetic reply next to it",
			in:   []provider.Message{nCall("A"), nUser("next")},
			want: []provider.Message{nCall("A"), {Role: "tool", ToolCallID: "A", Name: "tool_A", Content: noReply}, nUser("next")},
		},
		{
			name: "late reply is pulled next to its call",
			in:   []provider.Message{nCall("A"), nUser("while you were out"), nReply("A", "real")},
			want: []provider.Message{nCall("A"), nReply("A", "real"), nUser("while you were out")},
		},
		{
			name: "empty input stays empty",
			in:   nil,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := deepCopyMessages(tc.in)
			got := normalizeForPrompt(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("normalizeForPrompt:\n got %s\nwant %s", describeMessages(got), describeMessages(tc.want))
			}
			if !reflect.DeepEqual(tc.in, before) {
				t.Fatalf("normalizeForPrompt mutated its input:\n got %s\nwant %s",
					describeMessages(tc.in), describeMessages(before))
			}
			// Idempotence: a projection that changes on a second pass would
			// rewrite prompts (and bust prompt caches) for no reason.
			if again := normalizeForPrompt(got); !reflect.DeepEqual(again, got) {
				t.Fatalf("normalizeForPrompt is not idempotent:\n once %s\ntwice %s",
					describeMessages(got), describeMessages(again))
			}
		})
	}
}

// The declaring call may live only in RawAssistant (sessions written by older
// builds streamed the message and never parsed ToolCalls), so normalisation
// must read both. Otherwise a valid pair looks like an orphan reply.
func TestNormalizeForPromptReadsRawAssistantCalls(t *testing.T) {
	raw := provider.Message{
		Role:         "assistant",
		RawAssistant: []byte(`{"role":"assistant","content":"","tool_calls":[{"id":"raw1","type":"function","function":{"name":"exec","arguments":"{}"}}]}`),
	}
	in := []provider.Message{nUser("run"), raw, nUser("meanwhile"), nReply("raw1", "real")}

	got := normalizeForPrompt(in)
	want := []provider.Message{nUser("run"), raw, nReply("raw1", "real"), nUser("meanwhile")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeForPrompt:\n got %s\nwant %s", describeMessages(got), describeMessages(want))
	}
}

// A call id declared twice (a duplicated assistant append) must not be
// answered twice; the later declaration loses its call, matching what the
// wire builder does with an unanswered call.
func TestNormalizeForPromptStripsDuplicateCallDeclaration(t *testing.T) {
	in := []provider.Message{nCall("A"), nReply("A", "done"), nCall("A"), nUser("again")}

	got := normalizeForPrompt(in)
	if len(got) != 4 {
		t.Fatalf("got %s; want 4 messages", describeMessages(got))
	}
	if len(got[2].ToolCalls) != 0 {
		t.Fatalf("second declaration kept its duplicate call: %s", describeMessages(got))
	}
	replies := 0
	for _, m := range got {
		if m.Role == "tool" && m.ToolCallID == "A" {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("call A answered %d times; want 1 (%s)", replies, describeMessages(got))
	}
}

func deepCopyMessages(msgs []provider.Message) []provider.Message {
	if msgs == nil {
		return nil
	}
	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	return out
}

func describeMessages(msgs []provider.Message) string {
	var b []byte
	b = append(b, '[')
	for i, m := range msgs {
		if i > 0 {
			b = append(b, ' ')
		}
		b = append(b, m.Role...)
		if m.ToolCallID != "" {
			b = append(b, '/')
			b = append(b, m.ToolCallID...)
		}
		for _, tc := range m.ToolCalls {
			b = append(b, '+')
			b = append(b, tc.ID...)
		}
		if m.Content != "" {
			b = append(b, ':')
			b = append(b, m.Content...)
		}
	}
	return string(append(b, ']'))
}
