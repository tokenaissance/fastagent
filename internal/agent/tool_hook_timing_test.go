package agent

// "hook: after tool call" reported elapsed=2562047h47m16.854775807s — the
// duration of time.Since(time.Time{}) — for every one of the 33 tool calls in
// the 09-22 dev session, so the platform had no per-tool timing at all. The
// diagnosis in that session had to be reconstructed from timestamps.
//
// Cause: LoggingHook reads the clock OFF THE CONTEXT it is handed at
// BeforeToolCall and reads it back at AfterToolCall (internal/agent/hooks.go).
// The two halves were handed different HookContext objects, so the after half
// read a zero time. After *model* call was always right for the same reason it
// is a control here: that pair passes StartTime across explicitly.
//
// Falsification: give the After hook a fresh context again (drop the StartTime
// carry) and the elapsed below is back to 2562047h.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

const toolWorkTime = 300 * time.Millisecond

// Both entry points are covered: the web chat arrives through
// HandleMessageStream, the API/bus path through HandleMessage, and each one
// builds its own AfterToolCall context (finishToolCall, and the inline hook).
func TestAfterToolCallReportsThisCallsDuration(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Agent, bus.InboundMessage)
	}{
		{"HandleMessage", func(a *Agent, msg bus.InboundMessage) { a.HandleMessage(context.Background(), msg) }},
		{"HandleMessageStream", func(a *Agent, msg bus.InboundMessage) { drainStream(a.HandleMessageStream(context.Background(), msg)) }},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAfterToolCallDuration(t, tc.run) })
	}
}

func assertAfterToolCallDuration(t *testing.T, run func(*Agent, bus.InboundMessage)) {
	t.Helper()
	a, _ := newGateAgent(t)
	// Production registers the timing hook at all four points (NewAgent); the
	// gate harness leaves the registry empty, so they are wired up here.
	a.hooks.Register(BeforeModelCall, LoggingHook())
	a.hooks.Register(AfterModelCall, LoggingHook())
	a.hooks.Register(BeforeToolCall, LoggingHook())
	a.hooks.Register(AfterToolCall, LoggingHook())
	a.provider = &streamProvider{}
	a.registry.Register("slow_tool", "test tool", nil, func(ctx context.Context, _ json.RawMessage) (string, error) {
		select {
		case <-time.After(toolWorkTime):
			return "real tool result", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})

	logs := captureHookLogs(t)
	run(a, bus.InboundMessage{Channel: "web", UserID: "u_owner", ChatID: "chat-hook-timing", Text: "go"})

	elapsed, ok := loggedDuration(logs.String(), "hook: after tool call", "tool=slow_tool")
	if !ok {
		t.Fatalf("no \"hook: after tool call\" line for slow_tool:\n%s", logs.String())
	}
	// The call really did take ~300ms, so anything near zero means the clock was
	// taken somewhere else.
	if elapsed < toolWorkTime/2 {
		t.Fatalf("after tool call elapsed = %v; want at least %v (the clock must be the call's own)", elapsed, toolWorkTime/2)
	}
	// And the observed defect: time.Since(<zero time>) is ~292 years.
	if elapsed > time.Minute {
		t.Fatalf("after tool call elapsed = %v; a duration that size is the zero-clock bug", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("after tool call elapsed = %v; want the call's duration, not the turn's", elapsed)
	}

	// Control: the model-call pair has always carried the clock, so it is the
	// witness that this is about the tool pair rather than about LoggingHook.
	if modelElapsed, ok := loggedDuration(logs.String(), "hook: after model call", ""); !ok || modelElapsed > time.Minute {
		t.Fatalf("after model call elapsed = %v (found=%v); that pair was never broken", modelElapsed, ok)
	}
}

// loggedDuration pulls `elapsed=` out of the first log line containing every
// marker, so an assertion can be about a duration without re-implementing the
// handler's formatting.
func loggedDuration(logs, msg string, markers ...string) (time.Duration, bool) {
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, msg) {
			continue
		}
		all := true
		for _, m := range markers {
			if m != "" && !strings.Contains(line, m) {
				all = false
				break
			}
		}
		if !all {
			continue
		}
		i := strings.Index(line, "elapsed=")
		if i < 0 {
			continue
		}
		rest := line[i+len("elapsed="):]
		if end := strings.IndexAny(rest, " \t"); end >= 0 {
			rest = rest[:end]
		}
		d, err := time.ParseDuration(rest)
		if err != nil {
			continue
		}
		return d, true
	}
	return 0, false
}

func captureHookLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}
