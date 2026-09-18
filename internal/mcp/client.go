package mcp

import "encoding/json"

// Client is the interface for communicating with an MCP server.
type Client interface {
	Connect() error
	ListTools() ([]ToolDef, error)
	CallTool(name string, args json.RawMessage) (string, error)
	Close() error
}

// NotificationSink is implemented by clients that can receive server-initiated
// notifications — JSON-RPC messages carrying a method and no id.
//
// Only the stdio transport can: it owns a long-lived pipe whose reader sees
// every line the server writes. The HTTP client speaks one POST per request and
// never opens the SSE stream the Streamable-HTTP spec uses for server→client
// messages, so on that transport a server has nowhere to push to — the
// notification is not dropped by us, it has no wire (docs 10 §3.4, G11).
//
// The handler is called on the client's reader goroutine with the client's lock
// held: it must not call back into the same client.
type NotificationSink interface {
	SetNotificationHandler(func(method string))
}

// ToolDef represents a tool definition returned by an MCP server.
type ToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// JSON-RPC 2.0 types

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	// Method is set on server-initiated messages (notifications and requests):
	// they carry a method and no id, so ID unmarshals to 0.
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeParams struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    struct{}   `json:"capabilities"`
	ClientInfo      clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolsListResult struct {
	Tools []ToolDef `json:"tools"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolCallResult struct {
	Content []toolContent `json:"content"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
