package tools

// delegate_task was the single most expensive line in the tool schema: 3,457
// characters of JSON (≈860 tokens) sent with EVERY request, because a tool
// definition is part of the schema, not of the turn that calls it. Most of it
// restated what the system prompt's task-delegation module already teaches (when
// to delegate, how to write the task arg, the worked example), so the schema now
// carries only what a caller needs AT THE MOMENT OF THE CALL.
//
// These two tests are the contract for that split: the call-time facts must
// survive, and the size must not creep back.

import (
	"encoding/json"
	"strings"
	"testing"
)

const delegateDescriptionBudget = 2300

// delegateToolPayload returns the marshaled definition — description AND
// parameters, because a parameter's guidance is part of what the model reads and
// part of what every request pays for.
func delegateToolPayload(t *testing.T) string {
	t.Helper()
	r := NewRegistry("", "")
	RegisterDelegateTask(r, stubbedRunner{})
	for _, def := range r.Definitions() {
		if def.Function.Name != "delegate_task" {
			continue
		}
		raw, err := json.Marshal(def)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(raw)
	}
	t.Fatal("delegate_task is not registered")
	return ""
}

// The facts that only the call site needs — everything else belongs to the
// system prompt, which is where the model reads it before deciding anything.
func TestDelegateTaskSchemaKeepsCallTimeFacts(t *testing.T) {
	lower := strings.ToLower(delegateToolPayload(t))

	for _, want := range []struct{ marker, why string }{
		{"serially", "a fan-out costs N × wall time; planning around parallel throughput wastes the budget"},
		{"no nesting", "without this line flash-tier models recurse and burn budgets exponentially"},
		{"sub-agent", "what runs is a sub-agent, not a parallel copy of the agent"},
		{"tool result", "the return value is a tool result the caller assembles, not a user-facing message"},
	} {
		if !strings.Contains(lower, want.marker) {
			t.Errorf("delegate_task description no longer states %q (%s)", want.marker, want.why)
		}
	}
	// Per-parameter guidance the caller cannot get anywhere else: the sub-agent
	// sees ONLY what is passed in, and a truncated run is marked as partial.
	for _, param := range []string{"does not see", "partial"} {
		if !strings.Contains(lower, param) {
			t.Errorf("delegate_task schema no longer carries %q", param)
		}
	}
}

func TestDelegateTaskSchemaStaysUnderItsBudget(t *testing.T) {
	if payload := len(delegateToolPayload(t)); payload > delegateDescriptionBudget {
		t.Fatalf("delegate_task payload is %d chars (budget %d) — it is sent with every request",
			payload, delegateDescriptionBudget)
	}
}
