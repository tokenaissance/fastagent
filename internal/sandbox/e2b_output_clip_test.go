package sandbox

// The production OOM (2026-09-14) came through this exact path: envd streams the
// command's stdout back in ~17 KB frames and execOn assembled every one of them
// into a single Go string — 73 MB in the case that killed the pod. This test
// feeds a big stream through the real execOn and requires the result to arrive
// clipped, with both ends intact.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// bigStreamTransport answers Process/Start with one start frame, N data frames
// and a clean end frame — a command that prints a lot and exits 0.
type bigStreamTransport struct {
	payload int // bytes of stdout to stream
}

func (t *bigStreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start, _ := json.Marshal(map[string]any{"event": map[string]any{
		"start": map[string]any{"pid": 7}}})
	body := connectEnvelope(start)

	const chunk = 12 << 10
	sent := 0
	for sent < t.payload {
		n := chunk
		if remaining := t.payload - sent; remaining < n {
			n = remaining
		}
		// "line <n>\n" repeated: real, line-structured output.
		text := strings.Repeat("x", n-1) + "\n"
		data, _ := json.Marshal(map[string]any{"event": map[string]any{
			"data": map[string]any{"stdout": base64.StdEncoding.EncodeToString([]byte(text))}}})
		body = append(body, connectEnvelope(data)...)
		sent += n
	}
	end, _ := json.Marshal(map[string]any{"event": map[string]any{
		"end": map[string]any{"exited": true, "status": "exit status 0"}}})
	body = append(body, connectEnvelope(end)...)

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}, nil
}

func TestE2BExecClipsHugeOutput(t *testing.T) {
	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: &bigStreamTransport{payload: 400 << 10}}

	out, err := ex.Exec(context.Background(), "big-output", 30*time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out, "of output omitted") {
		t.Fatalf("a 400 KB output must come back clipped; got %d bytes without a marker", len(out))
	}
	if len(out) > OutputHeadCap+OutputTailCap+400 {
		t.Fatalf("clipped result is %d bytes — the cap must bound what leaves the sandbox", len(out))
	}
}
