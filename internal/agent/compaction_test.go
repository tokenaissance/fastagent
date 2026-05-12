package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// fakeSummarizer captures the summarize-call prompt so tests can
// assert what compaction actually ships off to the LLM. The
// returned Response is whatever the test wants — we don't care
// about the summary content, only the input.
type fakeSummarizer struct {
	gotSummaryRequest string
}

func (f *fakeSummarizer) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	// compressOlderMessages builds the user-role prompt as the
	// second message; the older-history text lives in its Content
	// after the "Summarize this conversation:\n\n" prefix.
	if len(msgs) >= 2 {
		f.gotSummaryRequest = msgs[1].Content
	}
	return &provider.Response{Content: "[fake summary]"}, nil
}

func (f *fakeSummarizer) ChatStream(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	return nil, nil
}

// TestCompactionDropsGoalContextFromSummary pins design §5.3 (b):
// when compaction folds older messages, runtime-injected
// goal_context messages must be excluded from the summary — their
// content is synthetic audit scaffolding and the latest one is
// already preserved verbatim in the recent tail.
func TestCompactionDropsGoalContextFromSummary(t *testing.T) {
	// Build a history that's longer than PruneTurnAge so
	// compression actually runs. Interleave goal_context messages
	// among real user/assistant turns.
	var msgs []provider.Message
	for i := 0; i < PruneTurnAge+5; i++ {
		msgs = append(msgs,
			provider.Message{Role: "user", Content: "real user message", Origin: provider.OriginUser},
			provider.Message{Role: "user", Content: "RUNTIME_AUDIT_PROMPT", Origin: provider.OriginGoalContext},
			provider.Message{Role: "assistant", Content: "real assistant reply", Origin: provider.OriginUser},
		)
	}

	f := &fakeSummarizer{}
	out, err := compressOlderMessages(msgs, f, "fake-model")
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if !strings.Contains(f.gotSummaryRequest, "real user message") {
		t.Errorf("summary input lost real user content: %s", f.gotSummaryRequest)
	}
	if strings.Contains(f.gotSummaryRequest, "RUNTIME_AUDIT_PROMPT") {
		t.Errorf("summary input included runtime-injected goal_context — should have been filtered:\n%s",
			f.gotSummaryRequest)
	}
	// The recent tail must still carry whatever was there; in
	// particular if the tail contained a goal_context the model
	// still needs it for the next audit.
	tailHasContext := false
	for _, m := range out[1:] /* skip the summary prepended at [0] */ {
		if m.Origin == provider.OriginGoalContext {
			tailHasContext = true
			break
		}
	}
	if !tailHasContext {
		t.Error("recent tail should still carry the live goal_context message")
	}
}

// TestCompactionPreservesContentWhenShortCircuits: when the input
// is already under PruneTurnAge, compressOlderMessages returns it
// unchanged. Goal_context filtering shouldn't change that fast path.
func TestCompactionPreservesContentWhenShortCircuits(t *testing.T) {
	in := []provider.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	out, err := compressOlderMessages(in, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("short input should pass through; got %d messages", len(out))
	}
}
