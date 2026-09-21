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
