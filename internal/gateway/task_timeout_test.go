package gateway

import (
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// Per-source turn budgets (docs/session-turn-integrity.md, P5): only cron may
// differ, only when the operator configured a value, and everything else must
// keep the queue default (zero = "queue default").
func TestTaskTimeoutForSourcePolicy(t *testing.T) {
	configured := &Gateway{cronTaskTimeout: 15 * time.Minute}
	cases := []struct {
		name   string
		gw     *Gateway
		source string
		want   time.Duration
	}{
		{"cron with a configured budget", configured, bus.SourceCron, 15 * time.Minute},
		{"cron without one", &Gateway{}, bus.SourceCron, 0},
		{"user turn", configured, bus.SourceUser, 0},
		{"goal continuation", configured, bus.SourceGoalContext, 0},
		{"heartbeat", configured, bus.SourceHeartbeat, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.gw.taskTimeoutFor(bus.InboundMessage{Source: tc.source})
			if got != tc.want {
				t.Fatalf("taskTimeoutFor(%q) = %s; want %s", tc.source, got, tc.want)
			}
		})
	}
}
