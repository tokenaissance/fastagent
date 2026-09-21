package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"
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
}

// NewHTTPClient creates a new HTTP MCP client.
func NewHTTPClient(url string, headers map[string]string) *HTTPClient {
	return &HTTPClient{
		url:     url,
		headers: expandHeaders(headers),
		client:  &http.Client{},
		nextID:  1,
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
		for k, v := range c.headers {
			httpReq.Header.Set(k, v)
		}
		if sessionID != "" {
			httpReq.Header.Set("Mcp-Session-Id", sessionID)
		}
		if auth != nil {
			if authToken == "" {
				tok, err := auth(context.Background())
				if err != nil {
					return nil, fmt.Errorf("mcp: oauth token unavailable: %w", err)
				}
				authToken = tok
			}
			httpReq.Header.Set("Authorization", "Bearer "+authToken)
		}

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

		rpcResp, err := parseResponseBody(resp.Header.Get("Content-Type"), respBody, id)
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
// A server-initiated message that rides along on such a stream is traced and
// skipped. Acting on one belongs to a standing channel, and standing channels
// are the open half of G11 (docs 10 §3.4) — this function does not open one.
func parseResponseBody(contentType string, body []byte, id int) (*jsonRPCResponse, error) {
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
			slog.Debug("mcp server message rode along on a request's stream",
				"method", msg.Method, "id", msg.ID)
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

// sseDataFrames returns the data payload of every event in an SSE body: `:`
// lines are comments (keep-alives), `field: value` lines carry the fields, and
// a blank line dispatches the accumulated `data:` lines as one event. Unknown
// fields (event:, id:) are not needed to find the reply.
func sseDataFrames(body []byte) []string {
	var frames []string
	var data []string
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if line == "" {
			if len(data) > 0 {
				frames = append(frames, strings.Join(data, "\n"))
				data = data[:0]
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok || field != "data" {
			continue
		}
		data = append(data, strings.TrimPrefix(value, " "))
	}
	if len(data) > 0 {
		frames = append(frames, strings.Join(data, "\n"))
	}
	return frames
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

// Close is a no-op for HTTP clients.
func (c *HTTPClient) Close() error {
	return nil
}
