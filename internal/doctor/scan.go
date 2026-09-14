// Package doctor analyses stored session history for the shapes that break
// providers or silently drop context. It is deliberately pure: callers hand it
// messages, it hands back findings, and nothing here knows about storage, HTTP
// or the agent loop. `fastagent doctor sessions` is the first caller; the same
// three shapes are what the wire builder and the prompt normaliser defend
// against at request time.
package doctor

import (
	"sort"
	"strconv"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// Kind classifies a finding.
type Kind string

const (
	// DuplicateReply — one tool_call_id answered more than once. Providers
	// reject it ("Messages with role 'tool' must be a response to a preceding
	// message with 'tool_calls'"); our projection layer collapses it, at the
	// cost of dropping the later reply (and its call) from the model's context.
	DuplicateReply Kind = "duplicate_tool_reply"
	// OrphanReply — a tool reply whose call no assistant message declared.
	OrphanReply Kind = "orphan_tool_reply"
	// UnansweredCall — an assistant declared a call that no reply answers.
	// Since Q4 (docs/session-turn-integrity.md) this is the shape stored
	// history is SUPPOSED to show for an interrupted turn: the loop leaves
	// the call open, and the prompt projection answers it at request time.
	UnansweredCall Kind = "unanswered_tool_call"
)

// Finding points at one message index in the slice that was scanned.
type Finding struct {
	Kind       Kind   `json:"kind"`
	Index      int    `json:"index"`
	ToolCallID string `json:"toolCallId"`
	Detail     string `json:"detail,omitempty"`
}

// Expected reports whether the finding describes a shape the runtime leaves
// in stored history on purpose, which the prompt projection
// (internal/agent/normalize.go) repairs at request time. An open call is
// exactly that: after Q4 the loop no longer persists a synthetic reply, so an
// interrupted turn shows up here and needs no operator. Duplicates and
// orphans do need one — nothing in the runtime writes them any more, so their
// presence means old or foreign history (the incident's duplicate replies, a
// compaction that dropped a declaring assistant).
func (f Finding) Expected() bool { return f.Kind == UnansweredCall }

// Unexpected returns the findings that need an operator: everything except
// the shapes Expected classifies. `doctor sessions` gates on this, not on the
// raw finding count.
func Unexpected(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if !f.Expected() {
			out = append(out, f)
		}
	}
	return out
}

// Scan returns every tool-call pairing violation in msgs, ordered by message
// index. Declarations are read through EffectiveToolCalls so a call that only
// exists inside RawAssistant (older streamed sessions) still counts as
// declared — otherwise every reply to it would look like an orphan.
func Scan(msgs []provider.Message) []Finding {
	declaredAt := make(map[string]int) // tool_call_id → declaring assistant index
	for i, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.EffectiveToolCalls() {
			if tc.ID != "" {
				if _, seen := declaredAt[tc.ID]; !seen {
					declaredAt[tc.ID] = i
				}
			}
		}
	}

	var findings []Finding
	answeredAt := make(map[string]int) // tool_call_id → first reply index
	for i, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		id := m.ToolCallID
		_, declared := declaredAt[id]
		switch {
		case id == "":
			findings = append(findings, Finding{Kind: OrphanReply, Index: i,
				Detail: "tool reply without a tool_call_id"})
		case !declared:
			findings = append(findings, Finding{Kind: OrphanReply, Index: i, ToolCallID: id,
				Detail: "no assistant message declares this call"})
		default:
			if first, seen := answeredAt[id]; seen {
				findings = append(findings, Finding{Kind: DuplicateReply, Index: i, ToolCallID: id,
					Detail: "already answered at index " + strconv.Itoa(first)})
			} else {
				answeredAt[id] = i
			}
		}
	}

	for _, id := range sortedIDs(declaredAt) {
		if _, answered := answeredAt[id]; !answered {
			i := declaredAt[id]
			findings = append(findings, Finding{Kind: UnansweredCall, Index: i, ToolCallID: id,
				Detail: "assistant declared this call; no reply answers it"})
		}
	}

	sort.SliceStable(findings, func(a, b int) bool { return findings[a].Index < findings[b].Index })
	return findings
}

func sortedIDs(declaredAt map[string]int) []string {
	ids := make([]string, 0, len(declaredAt))
	for id := range declaredAt {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return declaredAt[ids[a]] < declaredAt[ids[b]] })
	return ids
}
