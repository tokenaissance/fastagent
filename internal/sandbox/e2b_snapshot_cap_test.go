package sandbox

// SnapshotWorkspace rides the same execOn transport as Exec, but it is a
// machine payload: the base64 of a gzipped tar, decoded and untarred by the
// caller. These two cases pin the contract that keeps the tool-result clip from
// reaching it.
//
// Production context (2026-09-14): the post-exec sync re-uploaded 76 MB of this
// payload after every command, because the agent had left a growing run log
// under /workspace and the pod limit is 1 GiB. Clipping it — the shape of the
// first attempt at this fix — would have turned that into "the sync silently
// stops".

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedTransport serves two kinds of command: the snapshot exec streams
// `payload`, and the `du` probe the over-cap path issues gets `duReply`.
type scriptedTransport struct {
	payload []byte
	duReply string

	mu       sync.Mutex
	commands []string
}

func (t *scriptedTransport) command(req *http.Request) string {
	raw, _ := io.ReadAll(req.Body)
	// Connect envelope: 1 flag byte + 4 length bytes + the JSON payload.
	// The exec tests below send ~40 MB here, so slice rather than copy.
	if len(raw) > 5 {
		raw = raw[5:]
	}
	var msg struct {
		Process struct {
			Args []string `json:"args"`
		} `json:"process"`
	}
	_ = json.Unmarshal(raw, &msg)
	if len(msg.Process.Args) > 1 {
		return msg.Process.Args[1]
	}
	return ""
}

func (t *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cmd := t.command(req)
	t.mu.Lock()
	t.commands = append(t.commands, cmd)
	t.mu.Unlock()

	body := connectEnvelope(mustJSON(map[string]any{"event": map[string]any{
		"start": map[string]any{"pid": 9}}}))

	out := t.payload
	if strings.Contains(cmd, "du -ak") {
		out = []byte(t.duReply)
	}
	// Frames are decoded independently, so every chunk must be whole base64
	// (a multiple of 4, padding included).
	const chunk = 12 << 10
	for sent := 0; sent < len(out); sent += chunk {
		end := sent + chunk
		if end > len(out) {
			end = len(out)
		}
		data := mustJSON(map[string]any{"event": map[string]any{
			"data": map[string]any{"stdout": base64.StdEncoding.EncodeToString(out[sent:end])}}})
		body = append(body, connectEnvelope(data)...)
	}
	body = append(body, connectEnvelope(mustJSON(map[string]any{"event": map[string]any{
		"end": map[string]any{"exited": true, "status": "exit status 0"}}}))...)

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// snapshotTar builds what the template's `tar -czf - | base64 -w0` prints for
// the given files.
func snapshotTar(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	b := newTarBundle()
	for name, data := range files {
		if err := b.addBytes(name, data, 0o644, time.Now()); err != nil {
			t.Fatalf("addBytes(%s): %v", name, err)
		}
	}
	if err := b.close(); err != nil {
		t.Fatalf("close bundle: %v", err)
	}
	return b.gz.Bytes()
}

// incompressible returns n bytes gzip cannot shrink, so a snapshot really is as
// large as it looks.
func incompressible(t *testing.T, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return buf
}

// The tool-result clip must not reach the snapshot: the caller decodes and
// untars this, so a shortened payload is corrupt data rather than a shorter
// message.
func TestWorkspaceSnapshotIsNotClippedByTheToolResultCap(t *testing.T) {
	// 200 KB of entropy: tar.gz ~200 KB, base64 ~270 KB — past
	// OutputHeadCap+OutputTailCap (128 KiB) and under snapshotBase64Cap.
	content := incompressible(t, 200<<10)
	payload := base64.StdEncoding.EncodeToString(snapshotTar(t, map[string][]byte{"logs/run.log": content}))
	if len(payload) <= OutputHeadCap+OutputTailCap {
		t.Fatalf("the test payload is only %d bytes; it must exceed the tool cap to prove anything", len(payload))
	}

	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: &scriptedTransport{payload: []byte(payload)}}

	files, err := ex.SnapshotWorkspace(t.Context())
	if err != nil {
		t.Fatalf("SnapshotWorkspace: %v", err)
	}
	got, ok := files["logs/run.log"]
	if !ok {
		t.Fatalf("the snapshot lost the file it carried; got %d entries: %v", len(files), keysOf(files))
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("snapshot contents changed: got %d bytes, want %d", len(got), len(content))
	}
}

// Over the cap the snapshot fails loudly and says what made /workspace big,
// instead of shipping megabytes back to a pod with a 1 GiB limit.
func TestWorkspaceSnapshotRefusesAnOversizedWorkspace(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString(incompressible(t, 26<<20))
	if len(payload) <= snapshotBase64Cap {
		t.Fatalf("the test payload is %d bytes; the cap is %d", len(payload), snapshotBase64Cap)
	}

	ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
	ex.client = &http.Client{Transport: &scriptedTransport{
		payload: []byte(payload),
		duReply: "26624\t/workspace/kronos-wide-run.log\n1\t/workspace/notes.md\n",
	}}

	_, err := ex.SnapshotWorkspace(t.Context())
	if err == nil {
		t.Fatal("an over-cap snapshot must fail rather than stream megabytes back")
	}
	msg := err.Error()
	if !strings.Contains(msg, humanBytes(snapshotBase64Cap)) {
		t.Errorf("the error must name the cap, got: %s", snippet([]byte(msg), 300))
	}
	if !strings.Contains(msg, "/workspace/kronos-wide-run.log") {
		t.Errorf("the error must name what made /workspace big, got: %s", snippet([]byte(msg), 300))
	}
	if strings.Contains(msg, "of output omitted") {
		t.Errorf("the snapshot must be refused, not clipped: %s", snippet([]byte(msg), 300))
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
