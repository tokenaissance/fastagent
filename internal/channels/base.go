package channels

import (
	"context"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// SplitMessageMarker is the on-the-wire control token the LLM emits to
// ask an IM-style adapter (WeChat, …) to split a single outbound text
// payload into multiple separate chat bubbles. We picked a token that
//
//  1. won't appear in natural prose, so the agent can't trigger a split
//     by accident in markdown / code / quoted text;
//  2. survives WeChat's wechatStripMarkdown pass — it's not parsed as
//     any markdown construct;
//  3. reads as "control instruction" both to a human inspecting the
//     transcript and to the LLM emitting it.
//
// The agent-side hint that introduces this token to the model lives in
// internal/agent/loop.go under the per-turn system-prompt addendum, so
// the protocol stays advertised in exactly one place.
const SplitMessageMarker = "<|split|>"

// SplitOutboundText splits a reply payload on SplitMessageMarker into
// one chunk per bubble the adapter should send. Trims whitespace on each
// chunk and drops empties so a trailing marker or accidental double-
// split doesn't produce a blank message. Returns a single-element slice
// for the common case where the agent didn't ask to split — adapters
// can call this unconditionally without a branch.
func SplitOutboundText(text string) []string {
	parts := strings.Split(text, SplitMessageMarker)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Channel is the interface that all channel implementations must satisfy.
type Channel interface {
	// Name returns the channel type identifier (e.g. "telegram").
	Name() string
	// AccountID returns the account identifier within the channel.
	AccountID() string
	// BotUsername returns the bot's username for this channel (e.g. "mike_fastclaw_bot").
	// Returns empty string if not applicable.
	BotUsername() string
	// Start begins listening for messages. It should block until ctx is cancelled.
	Start(ctx context.Context) error
	// Send sends a plain text message to the specified chat.
	Send(chatID string, text string) error
	// SendMessage sends a rich outbound message with formatting, reply-to, buttons, etc.
	SendMessage(msg bus.OutboundMessage) error
	// SendTyping sends a typing indicator to the specified chat.
	SendTyping(chatID string) error
}
