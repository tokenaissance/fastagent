// Package port holds the LLM provider contract and the message/response
// types that cross it.
//
// The adapter package internal/provider implements this port with the
// OpenAI-compatible and Anthropic clients. Consumers that need only the
// contract can depend on this package without importing either client.
package port

import (
	"context"
	"encoding/json"
	"strings"
)

// Origin tags a Message that was produced by the runtime rather than a real
// user / model exchange.
const (
	OriginUser        = ""
	OriginGoalContext = "goal_context"
)

const (
	StoppedToolResult      = "(stopped — execution was interrupted before the tool returned)"
	NoReplyTurnAliveResult = "(no reply recorded yet — the owning turn is still running; do not re-issue this call; wait, or read what has already landed)"
	NoReplyUnknownResult   = "(no reply recorded yet; whether the owning turn is still running is not known to this projection)"
)

var SyntheticToolPads = []string{StoppedToolResult, NoReplyTurnAliveResult, NoReplyUnknownResult}

// Message represents a chat message.
type Message struct {
	Role         string          `json:"role"`
	Content      string          `json:"content,omitempty"`
	ContentParts []ContentPart   `json:"content_parts,omitempty"`
	ToolCalls    []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID   string          `json:"tool_call_id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Thinking     string          `json:"thinking,omitempty"`
	Timestamp    int64           `json:"timestamp,omitempty"`
	Metadata     map[string]any  `json:"metadata,omitempty"`
	RawAssistant json.RawMessage `json:"_raw,omitempty"`
	Origin       string          `json:"origin,omitempty"`
	Provider     string          `json:"provider,omitempty"`
	Model        string          `json:"model,omitempty"`
}

// EffectiveToolCalls returns the tool calls an assistant message declares.
func (m Message) EffectiveToolCalls() []ToolCall {
	if len(m.ToolCalls) > 0 {
		return m.ToolCalls
	}
	if len(m.RawAssistant) == 0 {
		return nil
	}
	var raw struct {
		ToolCalls []ToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal(m.RawAssistant, &raw); err != nil {
		return nil
	}
	return raw.ToolCalls
}

// TextContent returns the message's user-visible text.
func (m Message) TextContent() string {
	if m.Content != "" {
		return StripAttachedPrefix(m.Content)
	}
	var parts []string
	for _, p := range m.ContentParts {
		if p.Type == "text" && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return StripAttachedPrefix(strings.Join(parts, "\n"))
}

// StripAttachedPrefix removes leading "[Attached: …]" tags.
func StripAttachedPrefix(s string) string {
	for {
		if !strings.HasPrefix(s, "[Attached:") {
			break
		}
		end := strings.IndexByte(s, ']')
		if end < 0 {
			break
		}
		s = strings.TrimLeft(s[end+1:], " \t\r\n")
	}
	return s
}

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

type Response struct {
	Content      string
	ToolCalls    []ToolCall
	Thinking     string
	Usage        Usage
	RawAssistant json.RawMessage
}

func (r *Response) HasToolCalls() bool {
	return len(r.ToolCalls) > 0
}

const FinishReasonLength = "length"

type StreamChunk struct {
	Content           string
	ToolCalls         []ToolCall
	Done              bool
	FinishReason      string
	Thinking          string
	ThinkingSignature string
	Usage             Usage
	RawAssistant      json.RawMessage
}

type StreamReader struct {
	ch  chan StreamChunk
	err error
}

func NewStreamReader(ch chan StreamChunk) *StreamReader {
	return &StreamReader{ch: ch}
}

func (r *StreamReader) Next() (StreamChunk, bool) {
	chunk, ok := <-r.ch
	return chunk, ok
}

func (r *StreamReader) Err() error {
	return r.err
}

func (r *StreamReader) SetErr(err error) {
	r.err = err
}

type Provider interface {
	Chat(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*Response, error)
	ChatStream(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*StreamReader, error)
}
