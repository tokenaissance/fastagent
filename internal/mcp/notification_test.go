package mcp

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
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

// A transport that cannot receive notifications (HTTP) is left alone: no handler
// is invented for a wire that does not exist.
func TestManagerDoesNotWireATransportWithoutNotifications(t *testing.T) {
	called := false
	m := &Manager{
		gate:           newNotificationGate(time.Second),
		onNotification: func(string, string) { called = true },
	}
	m.wireNotificationSink("quandora", NewHTTPClient("https://example.test/mcp", nil))
	if called {
		t.Fatal("a notification came out of a transport that has no wire for it")
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

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
