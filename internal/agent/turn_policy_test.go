package agent

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func TestDetectToolLoop(t *testing.T) {
	calls := []provider.ToolCall{{
		ID:       "call-1",
		Function: provider.FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`},
	}}
	var last toolCallSignature
	consecutive := 0
	if detectToolLoop("agent", calls, &last, &consecutive) {
		t.Fatal("first call must not be a loop")
	}
	if detectToolLoop("agent", calls, &last, &consecutive) {
		t.Fatal("second identical call must not be a loop")
	}
	if !detectToolLoop("agent", calls, &last, &consecutive) {
		t.Fatal("third identical call must be a loop")
	}
}

func TestExtendIterationBudget(t *testing.T) {
	rt := &agentRuntime{maxToolIterations: 10, maxToolContinues: 2}
	rounds := 10
	segments := 1
	nextRounds, nextSegments, nudge, extended := extendIterationBudget(rt, 9, rounds, segments, true)
	if !extended {
		t.Fatal("a progressing segment must extend")
	}
	if nextRounds != 20 || nextSegments != 2 {
		t.Fatalf("extension = rounds %d segments %d; want 20 / 2", nextRounds, nextSegments)
	}
	if nudge.Role == "" {
		t.Fatal("extension must carry a nudge message")
	}
	if _, _, _, extended := extendIterationBudget(rt, 9, rounds, segments, false); extended {
		t.Fatal("a non-progressing segment must not extend")
	}
	if _, _, _, extended := extendIterationBudget(rt, 8, rounds, segments, true); extended {
		t.Fatal("only the last round may extend")
	}
}
