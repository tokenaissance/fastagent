package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/doctor"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// Compaction is the one path that removes messages wholesale, so it is the one
// path that can split a tool pair: summarise the assistant that declared a
// call, keep its replies, and every provider rejects the next request.
//
// P3 makes that harmless (the prompt is a projection repaired by
// normalizeForPrompt), and P4 is the assertion that it stays true no matter
// where the cutoff lands. The doctor scanner is the oracle: compacted history,
// after normalisation, must carry no pairing findings at all.
func TestCompactionOutputNormalisesToAPairingCleanHistory(t *testing.T) {
	// Long enough to trigger compression (PruneTurnAge is the retention tail).
	build := func(tailMutation func(msgs []provider.Message) []provider.Message) []provider.Message {
		var msgs []provider.Message
		for i := 0; i < PruneTurnAge+4; i++ {
			msgs = append(msgs,
				provider.Message{Role: "user", Content: fmt.Sprintf("turn %d", i), Origin: provider.OriginUser},
				provider.Message{Role: "assistant", Content: "working", Origin: provider.OriginUser},
			)
		}
		return tailMutation(msgs)
	}

	cases := []struct {
		name string
		msgs []provider.Message
	}{
		{
			// The cutoff (len-PruneTurnAge) lands right after the declaring
			// assistant, so the tail would start with its replies.
			name: "cutoff inside a pair: assistant summarised, replies kept",
			msgs: build(func(msgs []provider.Message) []provider.Message {
				cut := len(msgs) - PruneTurnAge
				call := provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{
					ID: "call_split", Type: "function", Function: provider.FunctionCall{Name: "exec"},
				}}}
				replies := []provider.Message{
					{Role: "tool", ToolCallID: "call_split", Name: "exec", Content: "half"},
					{Role: "tool", ToolCallID: "call_split", Name: "exec", Content: "other"},
				}
				out := append([]provider.Message{}, msgs[:cut]...)
				out = append(out, call)
				out = append(out, replies...)
				return append(out, msgs[cut:]...)
			}),
		},
		{
			// Duplicate replies inside the retained tail (the incident shape).
			name: "retained tail carries a duplicate reply",
			msgs: build(func(msgs []provider.Message) []provider.Message {
				return append(msgs,
					provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{
						ID: "call_dup", Type: "function", Function: provider.FunctionCall{Name: "exec"},
					}}},
					provider.Message{Role: "tool", ToolCallID: "call_dup", Name: "exec", Content: provider.StoppedToolResult},
					provider.Message{Role: "tool", ToolCallID: "call_dup", Name: "exec", Content: "real"},
				)
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Pruning first (content truncation), then compression (structural).
			pruned := pruneOldToolResults(tc.msgs)
			compacted, err := compressOlderMessages(context.Background(), pruned, &fakeSummarizer{}, "fake-model")
			if err != nil {
				t.Fatalf("compress: %v", err)
			}
			for _, shape := range [][]provider.Message{pruned, compacted} {
				normalised := normalizeForPrompt(shape)
				if findings := doctor.Scan(normalised); len(findings) != 0 {
					t.Fatalf("normalised output still violates the pairing contract: %+v", findings)
				}
			}
		})
	}
}
