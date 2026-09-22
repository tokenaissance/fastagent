package privacy

import (
	"context"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// ScrubbingProvider is where the piiScrubbing switch is implemented. It wraps
// the provider an agent holds, so *every* model call that agent makes — the
// turn's own call, the delegate_task sub-agent's loop, compaction's summarizer,
// the memory extractor — reads the redacted copy.
//
// A decorator rather than an edit at each call site, on purpose: the switch used
// to be applied at three of the eleven provider call sites (plan mode, the
// non-streaming turn, its forced final delivery), which is how the /v1
// `stream:true` turn and delegate_task came to hand raw user text to the model
// with the knob on — and how the knob's only assignment came to sit in a
// constructor with no callers, leaving it dead in production. One choke point
// that every call already passes through cannot be forgotten by the next call
// site (docs/08 §2.2.2: a rule re-implemented per consumer drifts; a rule
// implemented at the producer does not).
//
// What it does not cover, deliberately: Message.RawAssistant. That field is the
// provider's own serialized assistant message, replayed verbatim so the prompt
// prefix stays byte-identical (prompt cache, DeepSeek thinking mode) — see
// provider/openai.go's toAPIMessages. A PII echo inside the model's *own*
// earlier reply therefore still travels; see the change register for that limit.
type ScrubbingProvider struct {
	inner provider.Provider
}

// Wrap returns p with redaction installed. A nil provider stays nil so callers
// keep the "no provider configured" branches they already have.
func Wrap(p provider.Provider) provider.Provider {
	if p == nil {
		return nil
	}
	return &ScrubbingProvider{inner: p}
}

func (s *ScrubbingProvider) Chat(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (*provider.Response, error) {
	return s.inner.Chat(ctx, ScrubMessages(messages), tools, model, maxTokens, temperature)
}

func (s *ScrubbingProvider) ChatStream(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (*provider.StreamReader, error) {
	return s.inner.ChatStream(ctx, ScrubMessages(messages), tools, model, maxTokens, temperature)
}
