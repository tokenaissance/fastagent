package provider

// The stream knows why it ended and throws that away.
//
// On 2026-09-22 prod shipped two write_file calls whose arguments stopped
// mid-string, both at output_tokens = 8192 (the configured max). The wire said
// finish_reason "length" on both; nothing read it, so the only evidence left was
// a tool complaining about a missing key. These cases pin the field the agent
// logs, so "the model ran out of room" is a fact in the logs rather than a
// reconstruction.
//
// Falsification: drop the assignment that carries the reason onto the Done chunk
// and the first case fails with an empty FinishReason.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// finishReasonStream serves one chunked completion whose final choice carries
// the given finish_reason, the way OpenAI-compatible providers send it.
func finishReasonStream(t *testing.T, reason string) *StreamReader {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"role":"assistant","content":"{\"path\":"}}]}`+"\n\n")
		fmt.Fprintf(w, `data: {"choices":[{"delta":{},"finish_reason":%q}]}`+"\n\n", reason)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	sr, err := NewOpenAI("test-key", srv.URL).ChatStream(context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil, "test-model", 8192, 0.1)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	return sr
}

func doneChunkOf(t *testing.T, sr *StreamReader) StreamChunk {
	t.Helper()
	var done StreamChunk
	var seen bool
	for {
		chunk, ok := sr.Next()
		if !ok {
			break
		}
		if chunk.Done {
			done, seen = chunk, true
		}
	}
	if err := sr.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	if !seen {
		t.Fatal("the stream never delivered a Done chunk")
	}
	return done
}

func TestChatStreamReportsThatTheOutputHitTheCap(t *testing.T) {
	done := doneChunkOf(t, finishReasonStream(t, "length"))

	if done.FinishReason != FinishReasonLength {
		t.Fatalf("FinishReason = %q; want %q so the agent can log a real cause", done.FinishReason, FinishReasonLength)
	}
}

// The other half: a normal ending must not be reported as a cap. Otherwise the
// warning becomes noise and stops meaning anything.
func TestChatStreamDoesNotCallANormalEndingACap(t *testing.T) {
	done := doneChunkOf(t, finishReasonStream(t, "stop"))

	if done.FinishReason == FinishReasonLength {
		t.Fatalf("FinishReason = %q; a plain stop is not a cap", done.FinishReason)
	}
	if done.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q; want the provider's own word passed through", done.FinishReason)
	}
}
