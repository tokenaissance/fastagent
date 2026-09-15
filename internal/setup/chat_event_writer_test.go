package setup

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The cursor rule, on its own. It is the only thing standing between three
// sources (replay, hub, tail) and a duplicate frame on the wire, and the
// duplicate it prevents is a race — see chat_event_writer.go.
func TestChatEventWriterSendsEachSeqOnce(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := newChatEventWriter(rec, rec, -1)

	cw.emit(7, "tool_result", []byte(`{"id":"call_0","result":"brief"}`))
	body := rec.Body.String()
	if !strings.Contains(body, "id: 7") || !strings.Contains(body, `"type":"tool_result"`) {
		t.Fatalf("the first event must be written with its seq as the SSE id; got %q", body)
	}
	if cw.cursor() != 7 {
		t.Fatalf("cursor = %d, want 7", cw.cursor())
	}

	// Same event again (the hub delivering what the tail already sent).
	cw.emit(7, "tool_result", []byte(`{"id":"call_0","result":"brief"}`))
	if got := strings.Count(rec.Body.String(), "data: "); got != 1 {
		t.Fatalf("a repeated seq was written again (%d frames); body=%q", got, rec.Body.String())
	}

	// An out-of-order older event (the tail finishing a range the hub overtook).
	cw.emit(6, "tool_call", []byte(`{"id":"call_0"}`))
	if got := strings.Count(rec.Body.String(), "data: "); got != 1 {
		t.Fatalf("a stale seq was written (%d frames); body=%q", got, rec.Body.String())
	}

	// A newer one goes through, and moves the cursor.
	cw.emit(8, "done", nil)
	if cw.cursor() != 8 || !strings.Contains(rec.Body.String(), `"type":"done"`) {
		t.Fatalf("cursor = %d, body=%q", cw.cursor(), rec.Body.String())
	}
}

// Live-only events carry seq=-1 and are deliberately not id'd: `id:` is what the
// browser echoes back as Last-Event-ID, and a non-replayable event must not
// become a resume point. They also must not move the cursor.
func TestChatEventWriterLeavesLiveOnlyEventsOutOfTheCursor(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := newChatEventWriter(rec, rec, 4)

	cw.emit(-1, "content_delta", []byte(`{"delta":"tok"}`))
	if strings.Contains(rec.Body.String(), "id: ") {
		t.Fatalf("a live-only event must not carry an id; got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"delta":"tok"`) {
		t.Fatalf("a live-only event must still be delivered; got %q", rec.Body.String())
	}
	if cw.cursor() != 4 {
		t.Fatalf("cursor moved to %d for a live-only event", cw.cursor())
	}
}
