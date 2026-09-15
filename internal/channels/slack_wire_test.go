package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// These payloads are the wire contract the Slack adapter reads: the
// event_callback object Slack pushes inside a Socket Mode events_api request.
// They are pinned as literal JSON rather than built from slack-go structs,
// because building them with the library under test would hide exactly the kind
// of change a dependency bump can introduce — slack-go went from v0.19.0 to
// v0.29.0 in 2026-09 (the bump that closed GHSA-gxhx-2686-5h9g).
//
// Cross-checked by building the same fixture through both versions: every field
// below is identical on v0.19.0 and v0.29.0. The one delta is that the decoded
// MessageEvent marshals with an extra `"blocks": null` — a field the adapter
// never reads, and never writes back.
const (
	slackBotUserID = "U0BOT"
	slackAccountID = "acct-1"
)

func slackEventCallback(event string) string {
	return `{
	  "type": "event_callback",
	  "team_id": "T024BE7LD",
	  "event_id": "Ev024BE7L",
	  "event_time": 1757900000,
	  "event": ` + event + `
	}`
}

const (
	slackEventChannelMessage = `{
	  "type": "message",
	  "channel": "C024BE7LR",
	  "channel_type": "channel",
	  "user": "U024BE7LH",
	  "text": "can someone look at this?",
	  "ts": "1757900000.000100",
	  "event_ts": "1757900000.000100"
	}`
	slackEventDirectMessage = `{
	  "type": "message",
	  "channel": "D024BFF1M",
	  "channel_type": "im",
	  "user": "U024BE7LH",
	  "text": "hello there",
	  "ts": "1757900000.000200"
	}`
	slackEventBotMessage = `{
	  "type": "message",
	  "channel": "C024BE7LR",
	  "channel_type": "channel",
	  "bot_id": "B024BE7LH",
	  "user": "U024BE7LH",
	  "text": "deployed to staging",
	  "ts": "1757900000.000300"
	}`
	slackEventEditedMessage = `{
	  "type": "message",
	  "channel": "C024BE7LR",
	  "channel_type": "channel",
	  "user": "U024BE7LH",
	  "text": "fixed a typo",
	  "subtype": "message_changed",
	  "ts": "1757900000.000400"
	}`
	slackEventOwnMessage = `{
	  "type": "message",
	  "channel": "C024BE7LR",
	  "channel_type": "channel",
	  "user": "U0BOT",
	  "text": "on it",
	  "ts": "1757900000.000500"
	}`
	slackEventMention = `{
	  "type": "message",
	  "channel": "C024BE7LR",
	  "channel_type": "channel",
	  "user": "U024BE7LH",
	  "text": "hey <@U0MENTION> can you look at this?",
	  "ts": "1757900000.000600"
	}`
)

func parseSlackEvent(t *testing.T, payload string) slackevents.EventsAPIEvent {
	t.Helper()
	evt, err := slackevents.ParseEvent(json.RawMessage(payload),
		slackevents.OptionNoVerifyToken())
	if err != nil {
		t.Fatalf("slackevents.ParseEvent: %v", err)
	}
	return evt
}

// newTestSlack builds the adapter around a real slack.Client whose Web API calls
// are redirected by the caller (see the mention test); tests that never mention
// anyone make no HTTP call at all.
func newTestSlack(mb *bus.MessageBus, opts ...slack.Option) *Slack {
	return &Slack{
		client:      slack.New("xoxb-test-token", opts...),
		bus:         mb,
		accountID:   slackAccountID,
		botUserID:   slackBotUserID,
		botUsername: "fastagent",
	}
}

func nextInbound(t *testing.T, mb *bus.MessageBus) bus.InboundMessage {
	t.Helper()
	select {
	case msg := <-mb.Inbound:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound message reached the bus")
		return bus.InboundMessage{}
	}
}

func assertNoInbound(t *testing.T, mb *bus.MessageBus) {
	t.Helper()
	select {
	case msg := <-mb.Inbound:
		t.Fatalf("unexpected inbound message: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSlackChannelMessageReachesTheBus(t *testing.T) {
	payload := slackEventCallback(slackEventChannelMessage)

	// Pin the decoded shape first: this is the part a slack-go bump can move.
	evt := parseSlackEvent(t, payload)
	if got := string(evt.Type); got != "event_callback" {
		t.Fatalf("outer event type = %q, want event_callback", got)
	}
	if got := string(evt.InnerEvent.Type); got != "message" {
		t.Fatalf("inner event type = %q, want message", got)
	}
	inner, ok := evt.InnerEvent.Data.(*slackevents.MessageEvent)
	if !ok {
		t.Fatalf("inner event data is %T, want *slackevents.MessageEvent", evt.InnerEvent.Data)
	}
	if inner.Channel != "C024BE7LR" || inner.User != "U024BE7LH" ||
		inner.TimeStamp != "1757900000.000100" || inner.ChannelType != "channel" {
		t.Fatalf("decoded fields moved: %+v", inner)
	}

	mb := bus.New()
	newTestSlack(mb).handleEventsAPI(evt)

	msg := nextInbound(t, mb)
	if msg.Channel != "slack" {
		t.Fatalf("channel = %q, want slack", msg.Channel)
	}
	if msg.AccountID != slackAccountID {
		t.Fatalf("account = %q, want %q", msg.AccountID, slackAccountID)
	}
	if msg.ChatID != "C024BE7LR" {
		t.Fatalf("chat = %q, want C024BE7LR", msg.ChatID)
	}
	if msg.UserID != "U024BE7LH" {
		t.Fatalf("user = %q, want U024BE7LH", msg.UserID)
	}
	if msg.MessageID != "1757900000.000100" {
		t.Fatalf("message id = %q, want the Slack ts", msg.MessageID)
	}
	if msg.Text != "can someone look at this?" {
		t.Fatalf("text = %q", msg.Text)
	}
	if msg.PeerKind != "group" {
		t.Fatalf("peer kind = %q, want group for a channel message", msg.PeerKind)
	}
	if msg.IsBotMessage {
		t.Fatal("a user message was flagged as a bot message")
	}
}

func TestSlackDirectMessageIsPeerKindDM(t *testing.T) {
	mb := bus.New()
	newTestSlack(mb).handleEventsAPI(parseSlackEvent(t, slackEventCallback(slackEventDirectMessage)))

	if msg := nextInbound(t, mb); msg.PeerKind != "dm" {
		t.Fatalf("peer kind = %q, want dm for channel_type=im", msg.PeerKind)
	}
}

func TestSlackBotMessageIsFlagged(t *testing.T) {
	mb := bus.New()
	newTestSlack(mb).handleEventsAPI(parseSlackEvent(t, slackEventCallback(slackEventBotMessage)))

	if msg := nextInbound(t, mb); !msg.IsBotMessage {
		t.Fatal("a message carrying bot_id was not flagged as a bot message")
	}
}

func TestSlackEditedMessageIsIgnored(t *testing.T) {
	mb := bus.New()
	newTestSlack(mb).handleEventsAPI(parseSlackEvent(t, slackEventCallback(slackEventEditedMessage)))
	assertNoInbound(t, mb)
}

func TestSlackOwnMessageIsIgnored(t *testing.T) {
	mb := bus.New()
	newTestSlack(mb).handleEventsAPI(parseSlackEvent(t, slackEventCallback(slackEventOwnMessage)))
	assertNoInbound(t, mb)
}

// TestSlackMentionsResolveToUsernames drives the one path that needs a Web API
// hit: the adapter replaces <@U…> with @username and records the mention. The
// server on the other end is a stub, so the test pins the request path
// (users.info) and the field the adapter reads (user.name) without touching
// Slack.
func TestSlackMentionsResolveToUsernames(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		user := r.URL.Query().Get("user")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"user":{"id":%q,"name":"alice","real_name":"Alice"}}`, user)
	}))
	defer srv.Close()

	mb := bus.New()
	newTestSlack(mb, slack.OptionAPIURL(srv.URL+"/")).
		handleEventsAPI(parseSlackEvent(t, slackEventCallback(slackEventMention)))

	msg := nextInbound(t, mb)
	if msg.Text != "hey @alice can you look at this?" {
		t.Fatalf("mention was not rewritten: %q", msg.Text)
	}
	if len(msg.Mentions) != 1 || msg.Mentions[0] != "alice" {
		t.Fatalf("mentions = %v, want [alice]", msg.Mentions)
	}
	for _, p := range paths {
		if p != "/users.info" {
			t.Fatalf("unexpected Web API call: %s", p)
		}
	}
}

// TestSlackEventLoopIgnoresNonEventsAPIEvents covers what the v0.22.0 release
// changed in this adapter's path: socketmode no longer tears the connection down
// on a malformed frame, it emits an event instead (incoming_error, plus the
// connection-level error events). The adapter's switch only handles
// events_api, so those have to be inert — a panic or a mis-dispatched event here
// would take the whole channel down for one bad frame.
func TestSlackEventLoopIgnoresNonEventsAPIEvents(t *testing.T) {
	mb := bus.New()
	events := make(chan socketmode.Event, 4)
	s := newTestSlack(mb)
	s.socketMode = &socketmode.Client{Events: events}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleEvents(ctx)
	}()

	events <- socketmode.Event{Type: socketmode.EventTypeIncomingError}
	events <- socketmode.Event{Type: socketmode.EventTypeConnectionError}
	events <- socketmode.Event{Type: socketmode.EventTypeConnected}
	assertNoInbound(t, mb)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleEvents did not return after the context was cancelled")
	}
}
