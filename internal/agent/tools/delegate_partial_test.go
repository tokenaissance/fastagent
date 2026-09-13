package tools

// What the parent agent sees when a sub-agent runs out of budget. The tool must
// keep two things at once: the artifact the sub-agent had produced, and the
// fact that it is truncated — dropping either loses the point.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubbedRunner struct {
	out string
	err error
}

func (r stubbedRunner) RunSubagent(context.Context, SubagentRequest) (string, error) {
	return r.out, r.err
}

func TestDelegateTaskKeepsPartialResult(t *testing.T) {
	budgetErr := errors.New("subagent ran out of its 15m0s wall-time budget at iteration 3")
	r := NewRegistry("", "")
	RegisterDelegateTask(r, stubbedRunner{out: "PARTIAL BRIEF: 1) tiers …", err: budgetErr})

	out, err := r.Execute(context.Background(), "delegate_task", `{"task":"research QuantConnect"}`)
	if err == nil {
		t.Fatal("a truncated sub-agent must still surface as a tool error")
	}
	for _, want := range []string{"PARTIAL BRIEF", "stopped early", "wall-time budget", "partial"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool result is missing %q:\n%s", want, out)
		}
	}
}

func TestDelegateTaskReportsBareFailureWhenNothingWasSalvaged(t *testing.T) {
	r := NewRegistry("", "")
	RegisterDelegateTask(r, stubbedRunner{out: "   ", err: errors.New("subagent chat failed at iteration 1: boom")})

	out, err := r.Execute(context.Background(), "delegate_task", `{"task":"x"}`)
	if err == nil {
		t.Fatal("want the failure surfaced")
	}
	if !strings.HasPrefix(out, "[subagent failed: ") {
		t.Fatalf("out = %q, want the bare failure framing", out)
	}
	if strings.Contains(out, "stopped early") {
		t.Fatalf("nothing was salvaged, so there is no partial to frame: %q", out)
	}
}
