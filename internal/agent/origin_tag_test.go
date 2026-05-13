package agent

import (
	"reflect"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// TestOriginForInboundSource pins the Source → Origin mapping the
// userMsg construction depends on. The bug it guards against was
// real for several commits: the constant provider.OriginGoalContext
// was defined and three downstream filters (compaction summary
// input, WebChatHistory, FTS) checked `Origin != OriginUser`, but
// the field was never assigned in production — so goal continuation
// messages all carried "" and the filters silently no-op'd.
func TestOriginForInboundSource(t *testing.T) {
	cases := []struct {
		source string
		want   string
	}{
		{bus.SourceUser, provider.OriginUser},
		{bus.SourceCron, provider.OriginUser},
		{bus.SourceHeartbeat, provider.OriginUser},
		{bus.SourceSubAgent, provider.OriginUser},
		{bus.SourceGoalContinuation, provider.OriginGoalContext},
		{bus.SourceGoalBudgetLimit, provider.OriginGoalContext},
		{"", provider.OriginUser},
		{"unknown_future_source", provider.OriginUser},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			got := originForInboundSource(tc.source)
			if got != tc.want {
				t.Errorf("originForInboundSource(%q) = %q, want %q",
					tc.source, got, tc.want)
			}
		})
	}
}

// TestBuildUserMessageOriginPropagates ties the high-level
// guarantee — "goal-runtime messages land in session history with
// OriginGoalContext, real user turns land with OriginUser" —
// directly to the function HandleMessage / HandleMessageStream /
// handlePlanMode actually call. Replaces the previous "helper is
// unit-tested but the three call sites are only hand-eyeballed"
// gap.
func TestBuildUserMessageOriginPropagates(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{"user turn", bus.SourceUser, provider.OriginUser},
		{"cron tick", bus.SourceCron, provider.OriginUser},
		{"goal continuation", bus.SourceGoalContinuation, provider.OriginGoalContext},
		{"goal budget-limit wrap-up", bus.SourceGoalBudgetLimit, provider.OriginGoalContext},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildUserMessage(bus.InboundMessage{
				Source: tc.source,
				Text:   "hello",
			})
			if got.Origin != tc.want {
				t.Errorf("Origin = %q, want %q", got.Origin, tc.want)
			}
		})
	}
}

// TestBuildUserMessagePlainText: no images → bare Content,
// ContentParts left nil so the provider transport picks the
// cheap single-string shape. The empty ContentParts assertion is
// the load-bearing one: a regression that always emits parts —
// even for text-only messages — would balloon every chat into
// the heavier multimodal wire format.
func TestBuildUserMessagePlainText(t *testing.T) {
	got := buildUserMessage(bus.InboundMessage{Text: "hi"})
	if got.Role != "user" {
		t.Errorf("Role = %q, want user", got.Role)
	}
	if got.Content != "hi" {
		t.Errorf("Content = %q, want hi", got.Content)
	}
	if got.ContentParts != nil {
		t.Errorf("ContentParts should be nil for text-only, got %v", got.ContentParts)
	}
}

// TestBuildUserMessageSingleLegacyPhoto: the legacy IM bridge
// path (PhotoURL, single image) must produce a ContentParts with
// one text + one image_url. Three load-bearing checks: Content
// blanked, ContentParts populated, image URL preserved exactly.
func TestBuildUserMessageSingleLegacyPhoto(t *testing.T) {
	got := buildUserMessage(bus.InboundMessage{
		Text:     "look",
		PhotoURL: "https://im.example/photo.jpg",
	})
	if got.Content != "" {
		t.Errorf("Content should be blanked when ContentParts present, got %q", got.Content)
	}
	if len(got.ContentParts) != 2 {
		t.Fatalf("expected 2 parts (text + image), got %d", len(got.ContentParts))
	}
	if got.ContentParts[0].Type != "text" || got.ContentParts[0].Text != "look" {
		t.Errorf("part[0] = %+v, want {text: 'look'}", got.ContentParts[0])
	}
	if got.ContentParts[1].Type != "image_url" || got.ContentParts[1].ImageURL == nil ||
		got.ContentParts[1].ImageURL.URL != "https://im.example/photo.jpg" {
		t.Errorf("part[1] = %+v, want image_url for the photo", got.ContentParts[1])
	}
}

// TestBuildUserMessageMultiplePhotoURLs: web upload path
// (PhotoURLs, slice). Each URL must appear in order.
func TestBuildUserMessageMultiplePhotoURLs(t *testing.T) {
	got := buildUserMessage(bus.InboundMessage{
		Text:      "caption",
		PhotoURLs: []string{"https://web/a.png", "https://web/b.png", "https://web/c.png"},
	})
	if len(got.ContentParts) != 4 {
		t.Fatalf("expected 4 parts (text + 3 images), got %d", len(got.ContentParts))
	}
	urls := []string{
		got.ContentParts[1].ImageURL.URL,
		got.ContentParts[2].ImageURL.URL,
		got.ContentParts[3].ImageURL.URL,
	}
	want := []string{"https://web/a.png", "https://web/b.png", "https://web/c.png"}
	if !reflect.DeepEqual(urls, want) {
		t.Errorf("image URLs out of order: got %v, want %v", urls, want)
	}
}

// TestBuildUserMessageImageOnly: image with empty Text must NOT
// produce a leading empty-text part. Some upstream providers
// reject `[{text: ""}, {image_url}]` as a content-less wire
// message — that's why the construction code special-cases the
// empty-Text branch.
func TestBuildUserMessageImageOnly(t *testing.T) {
	got := buildUserMessage(bus.InboundMessage{
		PhotoURL: "https://im.example/photo.jpg",
	})
	if len(got.ContentParts) != 1 {
		t.Fatalf("expected 1 part (image only, no leading empty text), got %d: %+v",
			len(got.ContentParts), got.ContentParts)
	}
	if got.ContentParts[0].Type != "image_url" {
		t.Errorf("part[0] should be image_url, got %q", got.ContentParts[0].Type)
	}
}

// TestBuildUserMessageMergesPhotoURLAndPhotoURLs: both legacy
// (PhotoURL) and modern (PhotoURLs) populated on the same message
// — PhotoURL is prepended so it lands first in the order. The
// scenario is a multimodal IM bridge that uses the new web shape
// for batched uploads but still sets the legacy single field for
// older clients.
func TestBuildUserMessageMergesPhotoURLAndPhotoURLs(t *testing.T) {
	got := buildUserMessage(bus.InboundMessage{
		Text:      "see attachments",
		PhotoURL:  "https://legacy/first.jpg",
		PhotoURLs: []string{"https://web/second.png", "https://web/third.png"},
	})
	if len(got.ContentParts) != 4 {
		t.Fatalf("expected 4 parts (text + 3 images), got %d", len(got.ContentParts))
	}
	// PhotoURL (legacy single) is prepended → part[1].
	if got.ContentParts[1].ImageURL.URL != "https://legacy/first.jpg" {
		t.Errorf("legacy PhotoURL should be first image, got %q", got.ContentParts[1].ImageURL.URL)
	}
}

// TestBuildUserMessageGoalContinuationWithText: the highest-stakes
// combination — a goal continuation message must land with both
// the OriginGoalContext tag AND the audit prompt text intact. Any
// regression in either half re-opens a real bug (silent compaction
// no-op or lost continuation content).
func TestBuildUserMessageGoalContinuationWithText(t *testing.T) {
	got := buildUserMessage(bus.InboundMessage{
		Source: bus.SourceGoalContinuation,
		Text:   "<goal_context>...audit prompt...</goal_context>",
	})
	if got.Origin != provider.OriginGoalContext {
		t.Errorf("Origin = %q, want %q", got.Origin, provider.OriginGoalContext)
	}
	if got.Content != "<goal_context>...audit prompt...</goal_context>" {
		t.Errorf("audit prompt body dropped or mangled: %q", got.Content)
	}
}
