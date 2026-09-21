package mcp

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// A server-initiated notification is a JSON-RPC message with a method and no id.
// The stdio reader used to walk straight past it — which is where "this server's
// tool list changed" died silently (docs 10 §3.4, G11).
func TestStdioClientHandsNotificationsToTheHandler(t *testing.T) {
	stdout := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"qc_backtest"}]}}`,
	}, "\n") + "\n"

	var got []string
	c := &StdioClient{
		stdin:          nopWriteCloser{io.Discard},
		scanner:        bufio.NewScanner(strings.NewReader(stdout)),
		nextID:         1,
		onNotification: func(method string) { got = append(got, method) },
	}

	resp, err := c.sendRequest("tools/list", struct{}{})
	if err != nil {
		t.Fatalf("sendRequest: %v", err)
	}
	if len(got) != 1 || got[0] != "notifications/tools/list_changed" {
		t.Fatalf("notifications handed to the handler = %v; want the list_changed one", got)
	}
	if !strings.Contains(string(resp.Result), "qc_backtest") {
		t.Fatalf("the response we were waiting for was consumed as a notification: %s", resp.Result)
	}
}

// Without a handler the reader must still behave: the notification is skipped
// and the real response reaches the caller.
func TestStdioClientWithoutAHandlerStillReadsTheResponse(t *testing.T) {
	stdout := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/message"}`,
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
	}, "\n") + "\n"
	c := &StdioClient{
		stdin:   nopWriteCloser{io.Discard},
		scanner: bufio.NewScanner(strings.NewReader(stdout)),
		nextID:  1,
	}
	resp, err := c.sendRequest("tools/list", struct{}{})
	if err != nil {
		t.Fatalf("sendRequest: %v", err)
	}
	if resp.ID != 1 {
		t.Fatalf("resolved response id = %d; want 1", resp.ID)
	}
}

// The manager forwards notifications to its handler, naming the server, and the
// gate collapses a burst from one server without touching its neighbours.
func TestManagerWiresNotificationsThroughTheGate(t *testing.T) {
	type seen struct{ server, method string }
	var got []seen
	m := &Manager{
		gate: newNotificationGate(30 * time.Second),
		onNotification: func(server, method string) {
			got = append(got, seen{server, method})
		},
	}
	fake := &fakeSinkClient{}
	m.wireNotificationSink("quantconnect", fake)
	if fake.handler == nil {
		t.Fatal("the sink was not wired")
	}

	now := time.Unix(1_800_000_000, 0)
	m.gate.now = func() time.Time { return now }

	fake.handler("notifications/tools/list_changed")
	fake.handler("notifications/tools/list_changed") // burst: collapsed
	if len(got) != 1 || got[0].server != "quantconnect" {
		t.Fatalf("handler calls = %v; want exactly one, tagged with the server", got)
	}

	now = now.Add(31 * time.Second)
	fake.handler("notifications/tools/list_changed")
	if len(got) != 2 {
		t.Fatalf("handler calls = %v; want the gate to reopen after the interval", got)
	}
}

// A transport that cannot receive notifications is left alone: no handler is
// invented for a wire that does not exist. (Since 2026-09-22 HTTP is not that
// transport any more — it opens the spec's GET stream — so this uses a client
// that really has no way to receive one.)
func TestManagerLeavesANonSinkTransportAlone(t *testing.T) {
	called := false
	m := &Manager{
		gate:           newNotificationGate(time.Second),
		onNotification: func(string, string) { called = true },
	}
	m.wireNotificationSink("legacy", &plainClient{})
	if called {
		t.Fatal("a notification came out of a transport that cannot receive one")
	}
}

// The HTTP half of G11, end to end: a real Manager, a real Streamable-HTTP
// endpoint, and a change the server announces *unprompted* — no request of ours
// is in flight when it arrives — reaching the manager's handler through the
// same gate the stdio half uses.
func TestManagerHearsAnHTTPNotificationOverTheStandingStream(t *testing.T) {
	announce := make(chan struct{})
	streamUp := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			flusher.Flush()
			streamUp <- struct{}{}
			<-announce
			io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
			flusher.Flush()
			<-r.Context().Done()
			return
		}

		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode %s: %v", req.Method, err)
			return
		}
		result := `{}`
		switch req.Method {
		case "initialize":
			result = `{"protocolVersion":"2024-11-05","capabilities":{}}`
		case "tools/list":
			result = `{"tools":[{"name":"qc_backtest"}]}`
		}
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(result),
		})
	}))
	defer srv.Close()

	got := make(chan [2]string, 4)
	m := NewManager(
		map[string]config.MCPServerConfig{"quantconnect": {Type: "http", URL: srv.URL}},
		WithNotificationHandler(func(server, method string) { got <- [2]string{server, method} }),
	)
	defer m.Close()

	select {
	case <-streamUp:
	case <-time.After(2 * time.Second):
		t.Fatal("the manager never opened the server's standing stream")
	}
	close(announce)

	select {
	case seen := <-got:
		if seen[0] != "quantconnect" || seen[1] != "notifications/tools/list_changed" {
			t.Fatalf("handler got %v; want the quantconnect list change", seen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the unprompted change never reached the manager (docs 10 §3.4, G11)")
	}
}

type fakeSinkClient struct {
	handler func(string)
}

func (f *fakeSinkClient) Connect() error                                   { return nil }
func (f *fakeSinkClient) ListTools() ([]ToolDef, error)                    { return nil, nil }
func (f *fakeSinkClient) CallTool(string, json.RawMessage) (string, error) { return "", nil }
func (f *fakeSinkClient) Close() error                                     { return nil }
func (f *fakeSinkClient) SetNotificationHandler(h func(string))            { f.handler = h }

// plainClient is a transport with no way to receive anything: it is not a
// NotificationSink.
type plainClient struct{}

func (*plainClient) Connect() error                                   { return nil }
func (*plainClient) ListTools() ([]ToolDef, error)                    { return nil, nil }
func (*plainClient) CallTool(string, json.RawMessage) (string, error) { return "", nil }
func (*plainClient) Close() error                                     { return nil }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
