package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestHTTPClientReplaysSessionID pins Streamable-HTTP session handling:
// the Mcp-Session-Id returned by initialize must be attached to every
// subsequent request (tools/list / tools/call). Quandora rejects requests
// without it (HTTP 400 / -32600 Invalid Request).
func TestHTTPClientReplaysSessionID(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1: // initialize — server assigns a session
			w.Header().Set("Mcp-Session-Id", "sess-123")
			json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0", ID: 1,
				Result: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{}}`),
			})
		default: // tools/list — must carry the session
			if got := r.Header.Get("Mcp-Session-Id"); got != "sess-123" {
				t.Errorf("request %d Mcp-Session-Id = %q, want sess-123", calls.Load(), got)
			}
			json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0", ID: 2,
				Result: json.RawMessage(`{"tools":[]}`),
			})
		}
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, nil)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := c.ListTools(); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("server calls = %d, want 2", got)
	}
}

// TestHTTPClient401SingleReplay verifies the OAuth bearer injection and
// the exactly-once refresh-and-replay on 401 (never a retry loop).
func TestHTTPClient401SingleReplay(t *testing.T) {
	var serverCalls atomic.Int32
	var authCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("authorization = %q, want Bearer tok-1", got)
		}
		if serverCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0", ID: 1,
			Result: json.RawMessage(`{"tools":[]}`),
		})
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, nil)
	c.SetAuthProvider(func(_ context.Context) (string, error) {
		authCalls.Add(1)
		return "tok-1", nil
	})
	if _, err := c.ListTools(); err != nil {
		t.Fatalf("ListTools after single replay: %v", err)
	}
	if authCalls.Load() != 2 {
		t.Fatalf("auth calls = %d, want 2 (initial + one replay)", authCalls.Load())
	}
	if serverCalls.Load() != 2 {
		t.Fatalf("server calls = %d, want 2", serverCalls.Load())
	}
}

// TestHTTPClient401NoRetryLoop: a persistent 401 must not spin — exactly
// two auth calls (initial + one replay), then an error surfaces.
func TestHTTPClient401NoRetryLoop(t *testing.T) {
	var authCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, nil)
	c.SetAuthProvider(func(_ context.Context) (string, error) {
		authCalls.Add(1)
		return "tok-1", nil
	})
	if _, err := c.ListTools(); err == nil {
		t.Fatal("persistent 401 must error")
	}
	if authCalls.Load() != 2 {
		t.Fatalf("auth calls = %d, want 2 (no retry loop)", authCalls.Load())
	}
}

// TestHTTPClientNoAuthStaticHeaders: without an auth provider the static
// header path is unchanged and no Authorization header is injected.
func TestHTTPClientNoAuthStaticHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Static"); got != "v" {
			t.Errorf("X-Static = %q, want v", got)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization should not be set without auth provider")
		}
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0", ID: 1,
			Result: json.RawMessage(`{"tools":[]}`),
		})
	}))
	defer srv.Close()
	if _, err := NewHTTPClient(srv.URL, map[string]string{"X-Static": "v"}).ListTools(); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
}

// The client must advertise both content types, and the advertisement has to
// be true: parseResponseBody reads an SSE reply, so the header promises exactly
// what this client can handle.
func TestHTTPClientAdvertisesBothContentTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json, text/event-stream" {
			t.Errorf("Accept = %q; want both content types", got)
		}
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0", ID: 1,
			Result: json.RawMessage(`{"tools":[]}`),
		})
	}))
	defer srv.Close()

	if _, err := NewHTTPClient(srv.URL, nil).ListTools(); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
}

// The transport lets a server answer a request with text/event-stream instead
// of one JSON object, and the client must support both cases. Reading only the
// JSON case meant such a server was dropped with `invalid character 'e'
// looking for beginning of value`.
func TestHTTPClientReadsAnSSEReplyToARequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// A keep-alive comment, a server message that is not our answer, then
		// the answer itself — split over two data lines and ended with CRLF.
		io.WriteString(w, ": keep-alive\n\n")
		io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"}\n\n")
		io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\r\n")
		io.WriteString(w, "data: \"result\":{\"tools\":[{\"name\":\"qc_backtest\"}]}}\r\n\r\n")
	}))
	defer srv.Close()

	tools, err := NewHTTPClient(srv.URL, nil).ListTools()
	if err != nil {
		t.Fatalf("ListTools over an SSE reply: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "qc_backtest" {
		t.Fatalf("tools = %+v; want the one the stream carried", tools)
	}
}

// A stream that carries only server-initiated messages is not the reply we
// asked for: that must surface as an error, not as an empty answer.
func TestHTTPClientDoesNotTakeAServerMessageForTheReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
	}))
	defer srv.Close()

	_, err := NewHTTPClient(srv.URL, nil).ListTools()
	if err == nil {
		t.Fatal("a stream carrying no reply must error, not answer")
	}
	if !strings.Contains(err.Error(), "id 1") {
		t.Fatalf("error = %v; want it to name the id that never came", err)
	}
}

// The standing GET stream is the transport's server→client channel. Until
// 2026-09-22 nothing opened it, so on HTTP a server had nowhere to push: an
// unprompted change waited for a reload (docs 10 §3.4, G11). The witness has to
// be a message that arrives *while the stream stays open* — reading the whole
// body first would pass this test at EOF and still be the old behaviour.
func TestServerNotificationArrivesOverTheStandingStream(t *testing.T) {
	announce := make(chan struct{})
	accepts := make(chan string, 4)
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s to the MCP endpoint", r.Method)
			return
		}
		gets.Add(1)
		accepts <- r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		<-announce
		io.WriteString(w, ": keep-alive\n\n")
		io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
		flusher.Flush()
		<-r.Context().Done() // hold the stream open until the client goes away
	}))
	defer srv.Close()

	got := make(chan string, 4)
	c := NewHTTPClient(srv.URL, nil)
	defer c.Close()
	c.SetNotificationHandler(func(method string) { got <- method })

	select {
	case accept := <-accepts:
		if accept != "text/event-stream" {
			t.Fatalf("the GET's Accept = %q; the spec's word for this one request is text/event-stream", accept)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the client never opened the standing stream")
	}

	close(announce)

	select {
	case method := <-got:
		if method != "notifications/tools/list_changed" {
			t.Fatalf("notification handed to the handler = %q", method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the server's unprompted notification never reached the handler")
	}
	if n := gets.Load(); n != 1 {
		t.Fatalf("GET count = %d; want exactly one standing stream", n)
	}
}

// A stream with no handler is a connection bought for nothing, so wiring a
// handler is what opens it.
func TestTheStandingStreamIsNotOpenedWithoutAHandler(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			return
		}
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0", ID: 1,
			Result: json.RawMessage(`{"tools":[]}`),
		})
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, nil)
	defer c.Close()
	if _, err := c.ListTools(); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if n := gets.Load(); n != 0 {
		t.Fatalf("GET count = %d; a client with no notification handler must not buy a stream", n)
	}
}

// 405 is the spec's way of saying "this endpoint has no stream". Asking again
// would be asking a question the server already answered — and the POST path
// must keep working regardless.
func TestAnEndpointWithoutAStreamIsNotAskedTwice(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0", ID: 1,
			Result: json.RawMessage(`{"tools":[]}`),
		})
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, nil)
	defer c.Close()
	c.SetNotificationHandler(func(string) {})
	if _, err := c.ListTools(); err != nil {
		t.Fatalf("ListTools must keep working when the endpoint has no stream: %v", err)
	}
	// Long enough for a 1 s backoff to have fired a second GET.
	time.Sleep(1500 * time.Millisecond)
	if n := gets.Load(); n != 1 {
		t.Fatalf("GET count = %d; a 405 must not be retried", n)
	}
}

// A message with a method and an id is a server→client *request*, and JSON-RPC
// requires an answer. Ignoring it would be the silent-drop shape this whole path
// exists to kill.
func TestAServerRequestOnTheStreamIsAnswered(t *testing.T) {
	replies := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			flusher.Flush()
			io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"ping\"}\n\n")
			io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":8,\"method\":\"sampling/createMessage\"}\n\n")
			flusher.Flush()
			<-r.Context().Done()
			return
		}
		var msg map[string]any
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Errorf("decode POST: %v", err)
			return
		}
		replies <- msg
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, nil)
	defer c.Close()
	c.SetNotificationHandler(func(string) {})

	assertReply := func(id float64, wantKey string) {
		t.Helper()
		select {
		case msg := <-replies:
			if msg["id"] != id {
				t.Fatalf("reply id = %v; want %v", msg["id"], id)
			}
			if _, ok := msg[wantKey]; !ok {
				t.Fatalf("reply to %v = %v; want a %q", id, msg, wantKey)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no reply to the server's request %v", id)
		}
	}
	assertReply(7, "result") // ping: the base protocol's one request we can serve
	assertReply(8, "error")  // anything else: answered, not ignored
}

// Close is not a no-op for HTTP any more: the stream is a goroutine plus a
// connection, and dropping the client that points at them stops neither.
func TestClosingTheClientEndsTheStandingStream(t *testing.T) {
	streamUp := make(chan struct{}, 1)
	ended := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		streamUp <- struct{}{}
		<-r.Context().Done()
		ended <- struct{}{}
	}))
	// CloseClientConnections first: if this client ever leaks a stream, the test
	// must fail on the assertion below rather than hang the package on cleanup.
	defer func() { srv.CloseClientConnections(); srv.Close() }()

	c := NewHTTPClient(srv.URL, nil)
	c.SetNotificationHandler(func(string) {})
	select {
	case <-streamUp:
	case <-time.After(2 * time.Second):
		t.Fatal("no standing stream was opened")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil { // idempotent
		t.Fatalf("second Close: %v", err)
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not end the standing stream")
	}
}

// A dropped stream is reconnected, and the reconnect resumes from the last event
// id instead of losing whatever the server sent while we were away.
func TestAReconnectResumesWithTheLastEventID(t *testing.T) {
	reconnected := make(chan string, 4)
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		if gets.Add(1) == 1 {
			io.WriteString(w, "id: 5\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"}\n\n")
			flusher.Flush()
			return // the server drops the stream
		}
		reconnected <- r.Header.Get("Last-Event-ID")
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, nil)
	defer c.Close()
	c.SetNotificationHandler(func(string) {})

	select {
	case lastEventID := <-reconnected:
		if lastEventID != "5" {
			t.Fatalf("Last-Event-ID = %q; want the last event this client saw", lastEventID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the client never reconnected after the stream dropped")
	}
}

// A server message can also ride along on the SSE reply to one of our requests.
// It goes through the same door as the standing stream's, so the two cannot
// drift apart.
func TestMessagesOnAReplyStreamGoThroughTheSameDoor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed) // this endpoint has no standing stream
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
		io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[]}}\n\n")
	}))
	defer srv.Close()

	got := make(chan string, 2)
	c := NewHTTPClient(srv.URL, nil)
	defer c.Close()
	c.SetNotificationHandler(func(method string) { got <- method })
	if _, err := c.ListTools(); err != nil {
		t.Fatalf("ListTools over an SSE reply: %v", err)
	}
	select {
	case method := <-got:
		if method != "notifications/tools/list_changed" {
			t.Fatalf("notification = %q", method)
		}
	case <-time.After(time.Second):
		t.Fatal("a server message on a reply stream was dropped instead of handed to the sink")
	}
}
