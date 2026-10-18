package provider

import (
	"net/http"
	"strings"
	"time"

	providerport "github.com/fastclaw-ai/fastclaw/internal/provider/port"
)

// The provider contract and message types live in internal/provider/port.
// These aliases keep the adapter package source-compatible for existing
// callers, while new code can depend on the port directly.
type Message = providerport.Message
type ContentPart = providerport.ContentPart
type ImageURL = providerport.ImageURL
type ToolCall = providerport.ToolCall
type FunctionCall = providerport.FunctionCall
type Tool = providerport.Tool
type ToolFunction = providerport.ToolFunction
type Usage = providerport.Usage
type Response = providerport.Response
type StreamChunk = providerport.StreamChunk
type StreamReader = providerport.StreamReader
type Provider = providerport.Provider

const (
	OriginUser             = providerport.OriginUser
	OriginGoalContext      = providerport.OriginGoalContext
	StoppedToolResult      = providerport.StoppedToolResult
	NoReplyTurnAliveResult = providerport.NoReplyTurnAliveResult
	NoReplyUnknownResult   = providerport.NoReplyUnknownResult
	FinishReasonLength     = providerport.FinishReasonLength
)

var SyntheticToolPads = append([]string(nil), providerport.SyntheticToolPads...)
var NewStreamReader = providerport.NewStreamReader
var StripAttachedPrefix = providerport.StripAttachedPrefix

// newLLMHTTPClient returns an *http.Client suitable for streaming LLM API
// calls. It adds ResponseHeaderTimeout without changing DefaultTransport.
func newLLMHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: tr}
}

// StripProviderPrefix removes the "provider/" prefix from a model string.
func StripProviderPrefix(model string) string {
	if idx := strings.Index(model, "/"); idx >= 0 {
		return model[idx+1:]
	}
	return model
}

// SplitProviderModel splits "<providerKey>/<modelId>" into its two parts.
func SplitProviderModel(s string) (provider, model string) {
	if idx := strings.Index(s, "/"); idx >= 0 {
		return s[:idx], s[idx+1:]
	}
	return "", s
}

// NewProvider creates a Provider based on apiType.
// "anthropic-messages" creates an Anthropic provider, anything else creates
// an OpenAI-compatible provider.
func NewProvider(apiKey, apiBase, apiType string) Provider {
	if apiType == "anthropic-messages" {
		return NewAnthropic(apiKey, apiBase)
	}
	return NewOpenAI(apiKey, apiBase)
}
