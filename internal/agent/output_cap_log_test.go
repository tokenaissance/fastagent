package agent

// The moment a reply is cut off is the only moment the cause is still a fact.
//
// 2026-09-22 prod: two write_file calls arrived with arguments that stopped
// mid-string. Both were the tail of an output that ended at finish_reason
// "length" — the configured 8192-token cap — and nothing said so. The round was
// diagnosed afterwards from token_usage_log (the only two 8192s in the session)
// and from the shape of the broken JSON. A log line at the end of the stream is
// the difference between that and reading it off the screen.
//
// Falsification: drop the warning in the Done branch and the first case fails.
// Make it fire on every ending and the second case fails — a warning that also
// fires on "stop" is noise, and noise is how the real one gets missed.

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// finishReasonProvider ends one stream with the given reason, carrying the same
// truncated tool call prod produced alongside it.
type finishReasonProvider struct{ reason string }

func (p *finishReasonProvider) Chat(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.Response, error) {
	return &provider.Response{}, nil
}

func (p *finishReasonProvider) ChatStream(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.StreamReader, error) {
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{
		Done:         true,
		FinishReason: p.reason,
		ToolCalls: []provider.ToolCall{{
			ID: "call_cut_off", Type: "function",
			Function: provider.FunctionCall{Name: "write_file", Arguments: `{"path":"quantconnect-2026-09.md","content":"# Quant`},
		}},
	}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func cappedAgent(reason string) *Agent {
	return &Agent{
		name:      "quant",
		model:     "deepseek-chat",
		maxTokens: 8192,
		provider:  &finishReasonProvider{reason: reason},
	}
}

func TestACappedReplyIsLoggedWhenTheStreamEnds(t *testing.T) {
	logs := captureWarnings(t)

	if _, err := cappedAgent(provider.FinishReasonLength).streamChatToResponse(
		context.Background(), nil, nil); err != nil {
		t.Fatalf("streamChatToResponse: %v", err)
	}

	out := logs.String()
	if !strings.Contains(out, "token cap") {
		t.Fatalf("a reply that ran out of room was not logged; logs=%q", out)
	}
	// The two facts that make it actionable: the ceiling that was hit, and that
	// a tool call came with it (so a truncated call is expected downstream).
	if !strings.Contains(out, "max_tokens=8192") {
		t.Fatalf("the warning does not name the ceiling; logs=%q", out)
	}
	if !strings.Contains(out, "tool_calls=1") {
		t.Fatalf("the warning does not say a tool call was in flight; logs=%q", out)
	}
}

func TestAnOrdinaryEndingIsNotLoggedAsACap(t *testing.T) {
	logs := captureWarnings(t)

	if _, err := cappedAgent("stop").streamChatToResponse(
		context.Background(), nil, nil); err != nil {
		t.Fatalf("streamChatToResponse: %v", err)
	}

	if out := logs.String(); strings.Contains(out, "token cap") {
		t.Fatalf("a model that chose to stop was reported as cut off; logs=%q", out)
	}
}
