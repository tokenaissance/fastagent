package provider

// Same fact as the OpenAI case, different word: Anthropic says "max_tokens"
// where OpenAI says "length". A capped reply is the one ending the agent has to
// log (a truncated tool call is otherwise indistinguishable from a model that
// simply forgot its arguments), so both spellings have to reach the same field.
//
// Falsification: drop the stop_reason capture in the message_delta branch and
// this case fails with an empty FinishReason.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicStreamNormalizesACappedReplyToTheSharedWord(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"type":"message_start","message":{"usage":{"input_tokens":10}}}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{\"path\":"}}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":8192}}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"type":"message_stop"}`+"\n\n")
	}))
	t.Cleanup(srv.Close)

	sr, err := NewAnthropic("test-key", srv.URL).ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil, "test-model", 8192, 0.1)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	done := doneChunkOf(t, sr)
	if done.FinishReason != FinishReasonLength {
		t.Fatalf("FinishReason = %q; want %q — Anthropic's max_tokens is the same fact as OpenAI's length",
			done.FinishReason, FinishReasonLength)
	}
}
