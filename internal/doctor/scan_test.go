package doctor

import (
	"reflect"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func dCall(ids ...string) provider.Message {
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

func dReply(id, content string) provider.Message {
	return provider.Message{Role: "tool", ToolCallID: id, Name: "tool_" + id, Content: content}
}

func dUser(s string) provider.Message { return provider.Message{Role: "user", Content: s} }

// The three shapes the scan exists to catch, plus the clean case. Each is the
// production form of the same defect class: a call/reply pairing that no
// provider accepts or that silently loses context.
func TestScanShapes(t *testing.T) {
	pad := provider.StoppedToolResult

	cases := []struct {
		name string
		in   []provider.Message
		want []Finding
	}{
		{
			name: "clean history reports nothing",
			in:   []provider.Message{dUser("go"), dCall("A"), dReply("A", "done")},
			want: nil,
		},
		{
			name: "pad plus real result is a duplicate",
			in:   []provider.Message{dCall("A"), dReply("A", pad), dReply("A", "real")},
			want: []Finding{{Kind: DuplicateReply, Index: 2, ToolCallID: "A", Detail: "already answered at index 1"}},
		},
		{
			name: "the incident shape: two calls answered twice each",
			in: []provider.Message{
				dCall("A", "B"),
				dReply("A", pad), dReply("B", pad),
				dReply("A", "real"), dReply("B", "real"),
			},
			want: []Finding{
				{Kind: DuplicateReply, Index: 3, ToolCallID: "A", Detail: "already answered at index 1"},
				{Kind: DuplicateReply, Index: 4, ToolCallID: "B", Detail: "already answered at index 2"},
			},
		},
		{
			name: "reply with no declaring call is an orphan",
			in:   []provider.Message{dUser("go"), dReply("ghost", "late")},
			want: []Finding{{Kind: OrphanReply, Index: 1, ToolCallID: "ghost",
				Detail: "no assistant message declares this call"}},
		},
		{
			name: "reply without an id is an orphan",
			in:   []provider.Message{{Role: "tool", Content: "no id"}},
			want: []Finding{{Kind: OrphanReply, Index: 0, Detail: "tool reply without a tool_call_id"}},
		},
		{
			name: "declared call with no reply is unanswered",
			in:   []provider.Message{dUser("go"), dCall("A"), dUser("next")},
			want: []Finding{{Kind: UnansweredCall, Index: 1, ToolCallID: "A",
				Detail: "assistant declared this call; no reply answers it"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Scan(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Scan =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

// A call that only exists inside RawAssistant (older streamed sessions) must
// still count as declared, or every reply to it would be reported as an
// orphan — the scan would cry wolf on exactly the sessions it is meant to
// clear.
func TestScanReadsDeclarationsFromRawAssistant(t *testing.T) {
	raw := provider.Message{
		Role:         "assistant",
		RawAssistant: []byte(`{"role":"assistant","content":"","tool_calls":[{"id":"raw1","type":"function","function":{"name":"exec","arguments":"{}"}}]}`),
	}
	if got := Scan([]provider.Message{dUser("run"), raw, dReply("raw1", "real")}); len(got) != 0 {
		t.Fatalf("Scan reported %+v; want clean", got)
	}
}

// Since Q4 an unanswered call is the shape stored history is supposed to show
// for an interrupted turn — the loop leaves the call open and the projection
// answers it at request time. So it must be reported (an operator wants to
// know) but classified as expected, i.e. not gated on. Duplicates and orphans
// stay actionable: nothing in the runtime writes them any more.
func TestExpectedCoversOpenCallsOnly(t *testing.T) {
	in := []provider.Message{
		dCall("A", "B"),         // A answered twice below, B never answered
		dReply("A", "real"),     // first reply
		dReply("A", "late"),     // duplicate
		dReply("ghost", "late"), // orphan
	}
	findings := Scan(in)
	if len(findings) != 3 {
		t.Fatalf("Scan = %+v; want 3 findings", findings)
	}

	var actionable []Kind
	for _, f := range Unexpected(findings) {
		actionable = append(actionable, f.Kind)
	}
	if !reflect.DeepEqual(actionable, []Kind{DuplicateReply, OrphanReply}) {
		t.Fatalf("Unexpected = %v; want [%s %s]", actionable, DuplicateReply, OrphanReply)
	}
	for _, f := range findings {
		want := f.Kind == UnansweredCall
		if f.Expected() != want {
			t.Fatalf("Finding{%s}.Expected() = %v; want %v", f.Kind, f.Expected(), want)
		}
	}
}
