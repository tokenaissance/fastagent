package agent

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// TestOriginForInboundSource pins the Source → Origin mapping the
// three userMsg construction sites in loop.go depend on. The bug
// it guards against was real for several commits: the constant
// provider.OriginGoalContext was defined and three downstream
// filters (compaction summary input, WebChatHistory, FTS) checked
// `Origin != OriginUser`, but the field was never assigned in
// production — so goal continuation messages all carried "" and
// the filters silently no-op'd. If this mapping breaks, those
// three filters quietly stop working again.
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
