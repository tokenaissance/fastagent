package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// acceptHeader is what this client advertises on every request. The
// Streamable-HTTP transport requires both content types, and the second one is
// a promise rather than decoration: parseResponseBody reads an SSE reply too,
// because a server may answer a request with a stream instead of one object.
const acceptHeader = "application/json, text/event-stream"

// HTTPClient implements the MCP client for HTTP (Streamable HTTP) servers.
type HTTPClient struct {
	url     string
	headers map[string]string
	client  *http.Client
	mu      sync.Mutex
	nextID  int
	// sessionID is the Mcp-Session-Id the server assigned on initialize
	// (Streamable HTTP 2025-11-25). Servers like Quandora require it on
	// every request after the handshake; without it tools/list returns
	// HTTP 400 / -32600 Invalid Request.
	sessionID string
	// auth injects a Bearer token for OAuth-protected servers. Nil keeps
	// the static-header path unchanged.
	auth func(ctx context.Context) (string, error)

	// The standing GET stream — the transport's server→client channel — is
	// opened exactly once per client, and only when a handler is wired: a stream
	// with no one to hand messages to is a connection bought for nothing.
	streamStarted bool
	streamClosed  bool
	streamWg      sync.WaitGroup
	// done is closed by Close; it ends the listener and its in-flight GET.
	done chan struct{}
	// onNotification receives server-initiated notifications. Nil means the
	// caller has not wired a sink.
	onNotification func(method string)
	// lastEventID is the SSE id of the last event seen. It is replayed as
	// Last-Event-ID so a reconnect resumes where the dropped stream stopped
	// instead of losing whatever the server sent in between.
	lastEventID string
}

// NewHTTPClient creates a new HTTP MCP client.
func NewHTTPClient(url string, headers map[string]string) *HTTPClient {
	return &HTTPClient{
		url:     url,
		headers: expandHeaders(headers),
		client:  &http.Client{},
		nextID:  1,
		done:    make(chan struct{}),
	}
}

// SetAuthProvider wires an OAuth token provider. When set, every request
// gets Authorization: Bearer <token> and a 401 triggers exactly one
// refresh-and-replay (never a loop).
func (c *HTTPClient) SetAuthProvider(f func(ctx context.Context) (string, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.auth = f
}

// SetNotificationHandler implements NotificationSink (see client.go). Wiring a
// handler is what opens the transport's standing GET stream — the channel the
// Streamable-HTTP spec defines for server→client messages, and the only way a
// server can say anything to us without being asked first. Nothing opened it
// until 2026-09-22, so on this transport an unprompted change had nowhere to
// arrive rather than being dropped by us (docs 10 §3.4, G11).
func (c *HTTPClient) SetNotificationHandler(f func(method string)) {
	c.mu.Lock()
	c.onNotification = f
	start := f != nil && !c.streamStarted && !c.streamClosed
	if start {
		c.streamStarted = true
		c.streamWg.Add(1)
	}
	c.mu.Unlock()
	if !start {
		return
	}
	go func() {
		defer c.streamWg.Done()
		c.listen()
	}()
}

const (
	// streamHealthyAfter: a stream that stayed up this long was healthy, so the
	// next drop starts its backoff over instead of inheriting the growing delay
	// of a server that is refusing connections.
	streamHealthyAfter = 30 * time.Second
	maxStreamBackoff   = 30 * time.Second
	// serverReplyTimeout bounds the POST that answers a server request. It runs
	// off the stream's goroutine, so it must not be able to wedge it.
	serverReplyTimeout = 10 * time.Second
)

// listen keeps the standing stream open across drops, with a backoff that
// resets once a stream has lived long enough to call healthy. It returns for
// good when the client is closed, or when the server answers 405 — the spec's
// way of saying this endpoint has no stream, and not something to ask again.
func (c *HTTPClient) listen() {
	backoff := time.Second
	for {
		started := time.Now()
		offered, err := c.readStream()
		if c.stopped() {
			return
		}
		if !offered {
			slog.Debug("mcp endpoint offers no standing stream; not asking again", "url", c.url)
			return
		}
		if time.Since(started) > streamHealthyAfter {
			backoff = time.Second
		}
		if err != nil {
			slog.Warn("mcp standing stream ended; reconnecting",
				"url", c.url, "error", err, "in", backoff)
		} else {
			slog.Info("mcp standing stream closed by the server; reconnecting",
				"url", c.url, "in", backoff)
		}
		select {
		case <-c.done:
			return
		case <-time.After(backoff):
		}
		if backoff < maxStreamBackoff {
			backoff *= 2
		}
	}
}

func (c *HTTPClient) stopped() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// readStream opens the standing GET stream and reads it until it ends. The
// first result says whether the server offers a stream at all (405 = it does
// not); the second is why reading stopped.
func (c *HTTPClient) readStream() (offered bool, err error) {
	c.mu.Lock()
	auth := c.auth
	sessionID := c.sessionID
	lastEventID := c.lastEventID
	c.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return true, fmt.Errorf("create stream request: %w", err)
	}
	// The GET advertises the stream only: these are the spec's words for this
	// one request.
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	token := ""
	if auth != nil {
		tok, err := auth(ctx)
		if err != nil {
			return true, fmt.Errorf("mcp: oauth token unavailable: %w", err)
		}
		token = tok
	}
	c.applyHeaders(req, sessionID, token)

	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return true, nil // closed while connecting: not a server-side drop
		}
		return true, fmt.Errorf("open stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusMethodNotAllowed {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return true, fmt.Errorf("open stream: HTTP %d", resp.StatusCode)
	}
	if !isEventStream(resp.Header.Get("Content-Type")) {
		return true, fmt.Errorf("open stream: content-type %q; the spec requires text/event-stream or 405",
			resp.Header.Get("Content-Type"))
	}

	err = c.consumeStream(resp.Body)
	if ctx.Err() != nil {
		return true, nil // closed from under us: not a server-side drop
	}
	return true, err
}

// consumeStream reads server-sent events as they arrive. A standing stream is
// read incrementally and never buffered to its end: while the connection is
// healthy it has no end.
func (c *HTTPClient) consumeStream(body io.Reader) error {
	reader := bufio.NewReader(body)
	for {
		id, data, err := readSSEEvent(reader)
		if id != "" {
			c.mu.Lock()
			c.lastEventID = id
			c.mu.Unlock()
		}
		if data != "" {
			c.handleServerMessage([]byte(data))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// handleServerMessage acts on one JSON-RPC message the server sent us. Both of
// this transport's streams go through this one door — a reply that arrives as
// SSE, and the standing GET stream — so a message is treated the same way
// wherever it lands.
//
// A method with no id is a notification and goes to the sink. A method with an
// id is a *request*, and JSON-RPC requires an answer: ignoring one would be the
// silent drop this whole path exists to kill.
func (c *HTTPClient) handleServerMessage(data []byte) {
	var msg struct {
		Method string           `json:"method"`
		ID     *json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Debug("mcp stream carried a non-JSON frame", "url", c.url, "error", err)
		return
	}
	switch {
	case msg.Method == "":
		// A response. The spec lets a reply stream carry one when resuming a
		// previous request's stream; there is nothing for us to do with it here.
		slog.Debug("mcp stream carried a message with no method", "url", c.url)
	case msg.ID != nil:
		c.answerServerRequest(*msg.ID, msg.Method)
	default:
		c.mu.Lock()
		f := c.onNotification
		c.mu.Unlock()
		if f == nil {
			slog.Debug("mcp notification with no handler wired", "url", c.url, "method", msg.Method)
			return
		}
		f(msg.Method)
	}
}

// answerServerRequest replies to a server-initiated request. Our initialize
// advertises no capabilities at all (initializeParams sends an empty object),
// so a conforming server has nothing to ask us beyond the base protocol's ping;
// every other method is answered with "method not found" rather than ignored.
func (c *HTTPClient) answerServerRequest(id json.RawMessage, method string) {
	reply := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *jsonRPCError   `json:"error,omitempty"`
	}{JSONRPC: "2.0", ID: id}
	if method == "ping" {
		reply.Result = json.RawMessage(`{}`)
	} else {
		reply.Error = &jsonRPCError{Code: -32601, Message: "method not found: " + method}
	}
	body, err := json.Marshal(reply)
	if err != nil {
		slog.Warn("mcp: could not marshal a reply to the server's request",
			"url", c.url, "method", method, "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), serverReplyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		slog.Warn("mcp: could not build a reply to the server's request",
			"url", c.url, "method", method, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", acceptHeader)
	c.mu.Lock()
	sessionID := c.sessionID
	auth := c.auth
	c.mu.Unlock()
	token := ""
	if auth != nil {
		if tok, err := auth(ctx); err == nil {
			token = tok
		}
	}
	c.applyHeaders(req, sessionID, token)

	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn("mcp: could not deliver a reply to the server's request",
			"url", c.url, "method", method, "error", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	// A POST whose body is a response is accepted with 202 and no body.
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		slog.Warn("mcp: the server did not accept our reply to its request",
			"url", c.url, "method", method, "status", resp.StatusCode)
	}
}

// applyHeaders puts on req the headers every request to this endpoint carries.
// Callers set Content-Type and Accept first, since those differ between a POST
// of a request and a GET of the standing stream.
func (c *HTTPClient) applyHeaders(req *http.Request, sessionID, authToken string) {
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
}

func expandHeaders(headers map[string]string) map[string]string {
	expanded := make(map[string]string, len(headers))
	for k, v := range headers {
		if strings.HasPrefix(v, "$") {
			expanded[k] = os.Getenv(v[1:])
		} else {
			expanded[k] = v
		}
	}
	return expanded
}

func (c *HTTPClient) sendRequest(method string, params interface{}) (*jsonRPCResponse, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	auth := c.auth
	sessionID := c.sessionID
	c.mu.Unlock()

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	authToken := ""
	for attempt := 0; ; attempt++ {
		httpReq, err := http.NewRequest("POST", c.url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", acceptHeader)
		if auth != nil && authToken == "" {
			tok, err := auth(context.Background())
			if err != nil {
				return nil, fmt.Errorf("mcp: oauth token unavailable: %w", err)
			}
			authToken = tok
		}
		c.applyHeaders(httpReq, sessionID, authToken)

		resp, err := c.client.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("send request: %w", err)
		}

		respBody, readErr := io.ReadAll(resp.Body)
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			c.mu.Lock()
			c.sessionID = sid
			c.mu.Unlock()
		}
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && auth != nil {
			// Force one refresh and replay; a second 401 means the
			// credential is genuinely rejected — fail rather than loop.
			if tok, err := auth(context.Background()); err == nil {
				authToken = tok
				continue
			}
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
		}

		rpcResp, err := c.parseResponseBody(resp.Header.Get("Content-Type"), respBody, id)
		if err != nil {
			return nil, err
		}

		if rpcResp.Error != nil {
			return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
		}

		return rpcResp, nil
	}
}

// parseResponseBody reads one JSON-RPC response out of a POST reply. The
// transport lets a server answer either with a single JSON object or with an
// SSE stream carrying the same object as a `data:` frame, and the client is
// required to support both cases. Reading only the JSON case meant a server
// that chose the stream broke us: tools/list came back as `invalid character
// 'e' looking for beginning of value`, and the server was dropped.
//
// A server-initiated message that rides along on such a stream goes through
// handleServerMessage, the same door the standing stream's messages use.
func (c *HTTPClient) parseResponseBody(contentType string, body []byte, id int) (*jsonRPCResponse, error) {
	if !isEventStream(contentType) {
		var rpcResp jsonRPCResponse
		if err := json.Unmarshal(body, &rpcResp); err != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}
		return &rpcResp, nil
	}

	for _, payload := range sseDataFrames(body) {
		var msg jsonRPCResponse
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			continue // a keep-alive or a comment: not our answer
		}
		if msg.Method != "" {
			c.handleServerMessage([]byte(payload))
			continue
		}
		if msg.ID == id {
			return &msg, nil
		}
	}
	return nil, fmt.Errorf("parse response: the event stream carried no JSON-RPC response with id %d", id)
}

func isEventStream(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "text/event-stream"
}

// sseDataFrames returns the data payload of every event in a whole SSE body.
// A whole body (a reply that arrived as a stream) and a standing stream are read
// by the same parser, so the two cannot drift apart: an event is a run of
// `data:` lines ended by a blank line, `:` lines are comments (keep-alives), and
// the other fields are not needed to find a reply.
func sseDataFrames(body []byte) []string {
	reader := bufio.NewReader(bytes.NewReader(body))
	var frames []string
	for {
		_, data, err := readSSEEvent(reader)
		if data != "" {
			frames = append(frames, data)
		}
		if err != nil {
			return frames
		}
	}
}

// readSSEEvent reads one event: its data (the `data:` lines joined with \n) and
// the stream's last event id. An event that carries no data — a comment, or an
// id-only event — comes back with data == "". io.EOF means the body is
// exhausted; an event accumulated before the end is still returned.
func readSSEEvent(reader *bufio.Reader) (id, data string, err error) {
	var lines []string
	for {
		line, readErr := readSSELine(reader)
		if line == "" {
			if len(lines) > 0 {
				return id, strings.Join(lines, "\n"), nil
			}
			return id, "", readErr
		}
		field, value, _ := strings.Cut(line, ":")
		switch field {
		case "data":
			lines = append(lines, strings.TrimPrefix(value, " "))
		case "id":
			id = value
		}
	}
}

// readSSELine reads one line, accepting the standard's three terminators (LF,
// CRLF and a lone CR). A final line without a terminator is still a line; io.EOF
// comes only once the body is exhausted.
func readSSELine(reader *bufio.Reader) (string, error) {
	var line strings.Builder
	for {
		b, err := reader.ReadByte()
		if err != nil {
			if line.Len() > 0 && errors.Is(err, io.EOF) {
				return line.String(), nil
			}
			return "", err
		}
		switch b {
		case '\n':
			return line.String(), nil
		case '\r':
			if next, err := reader.Peek(1); err == nil && len(next) == 1 && next[0] == '\n' {
				_, _ = reader.ReadByte()
			}
			return line.String(), nil
		}
		line.WriteByte(b)
	}
}

// Connect initializes the connection with the MCP server.
func (c *HTTPClient) Connect() error {
	_, err := c.sendRequest("initialize", initializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo:      clientInfo{Name: "fastagent", Version: "0.1.0"},
	})
	return err
}

// ListTools returns the list of tools available on the MCP server.
func (c *HTTPClient) ListTools() ([]ToolDef, error) {
	resp, err := c.sendRequest("tools/list", struct{}{})
	if err != nil {
		return nil, err
	}

	var result toolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools list: %w", err)
	}

	return result.Tools, nil
}

// CallTool calls a tool on the MCP server.
func (c *HTTPClient) CallTool(name string, args json.RawMessage) (string, error) {
	resp, err := c.sendRequest("tools/call", toolCallParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return "", err
	}

	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("parse tool result: %w", err)
	}

	var texts []string
	for _, c := range result.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}
	return strings.Join(texts, "\n"), nil
}

// Close stops the standing stream. It is no longer a no-op: the stream is a
// goroutine plus a connection, and dropping the client that points at them stops
// neither (docs 11 #44). It is safe to call more than once.
func (c *HTTPClient) Close() error {
	c.mu.Lock()
	if !c.streamClosed {
		c.streamClosed = true
		close(c.done)
	}
	c.mu.Unlock()
	c.streamWg.Wait()
	return nil
}
