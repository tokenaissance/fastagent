package provider

import (
	"strings"
	"testing"
)

// TestOpenAIParseSSEUsage feeds a canned SSE stream through the OpenAI
// parser and checks Usage was extracted from the terminal include_usage
// chunk. This is the path goal-budget accounting relies on for every
// web-chat streaming turn.
func TestOpenAIParseSSEUsage(t *testing.T) {
	// Two chunks: a content delta, then the terminal usage-only chunk
	// (choices: [] + usage: {...}) that include_usage adds, then [DONE].
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hello"},"finish_reason":null}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":15,"prompt_tokens_details":{"cached_tokens":80}}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	p := &OpenAIProvider{}
	resp, err := p.parseSSE(strings.NewReader(sse))
	if err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	if resp.Content != "hello" {
		t.Errorf("content = %q, want %q", resp.Content, "hello")
	}
	if resp.Usage == nil {
		t.Fatalf("Usage is nil — include_usage chunk wasn't captured")
	}
	if resp.Usage.InputTokens != 120 {
		t.Errorf("InputTokens = %d, want 120", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 15 {
		t.Errorf("OutputTokens = %d, want 15", resp.Usage.OutputTokens)
	}
	if resp.Usage.CacheReadInputTokens != 80 {
		t.Errorf("CacheReadInputTokens = %d, want 80", resp.Usage.CacheReadInputTokens)
	}
}

// TestOpenAIParseSSENoUsage exercises the providers-don't-report-usage
// path (legacy endpoints, Ollama, etc.). Usage must be nil rather than
// a zero-valued struct so goal-budget code can detect "can't measure"
// and refuse to create a goal.
func TestOpenAIParseSSENoUsage(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	p := &OpenAIProvider{}
	resp, err := p.parseSSE(strings.NewReader(sse))
	if err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	if resp.Usage != nil {
		t.Errorf("Usage = %+v, want nil for streams that omit usage", resp.Usage)
	}
}

// TestAnthropicParseSSEUsage exercises the Anthropic SSE shape:
// usage rides on message_start (prompt + cache fields) and the final
// output_tokens count lands on message_delta. mergeUsage picks the
// larger of overlapping fields so the cumulative shape is correct.
func TestAnthropicParseSSEUsage(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":50,"output_tokens":1,"cache_read_input_tokens":30,"cache_creation_input_tokens":10}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`data: {"type":"message_delta","usage":{"input_tokens":50,"output_tokens":42}}`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	p := &AnthropicProvider{}
	resp, err := p.parseSSE(strings.NewReader(sse))
	if err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	if resp.Content != "hi" {
		t.Errorf("content = %q, want %q", resp.Content, "hi")
	}
	if resp.Usage == nil {
		t.Fatalf("Usage is nil — message_start/message_delta usage missed")
	}
	if resp.Usage.InputTokens != 50 {
		t.Errorf("InputTokens = %d, want 50", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, want 42 (message_delta wins via max)", resp.Usage.OutputTokens)
	}
	if resp.Usage.CacheReadInputTokens != 30 {
		t.Errorf("CacheReadInputTokens = %d, want 30 (from message_start)", resp.Usage.CacheReadInputTokens)
	}
	if resp.Usage.CacheCreationInputTokens != 10 {
		t.Errorf("CacheCreationInputTokens = %d, want 10 (from message_start)", resp.Usage.CacheCreationInputTokens)
	}
}

// TestAnthropicMergeUsageMonotonic guards the mergeUsage contract: a
// later event reporting a smaller value on any field must not clobber
// an earlier larger value. Anthropic occasionally refines an early
// estimate downward; we want the larger (truthful) number.
func TestAnthropicMergeUsageMonotonic(t *testing.T) {
	u := anthropicUsage{InputTokens: 100, OutputTokens: 5, CacheReadInputTokens: 80}
	mergeUsage(&u, anthropicUsage{InputTokens: 90, OutputTokens: 50, CacheReadInputTokens: 70})
	if u.InputTokens != 100 {
		t.Errorf("InputTokens regressed: got %d, want 100 (max of 100, 90)", u.InputTokens)
	}
	if u.OutputTokens != 50 {
		t.Errorf("OutputTokens did not advance: got %d, want 50", u.OutputTokens)
	}
	if u.CacheReadInputTokens != 80 {
		t.Errorf("CacheReadInputTokens regressed: got %d, want 80", u.CacheReadInputTokens)
	}
}
