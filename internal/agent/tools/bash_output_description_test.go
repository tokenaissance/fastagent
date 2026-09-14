package tools

// bash_output / kill_shell have NO budget test on purpose, and this comment is the
// reason: the prompt corpus (docs/prompt-inventory/) mentions bash_output,
// kill_shell or run_in_background ZERO times — every A/B block was searched. Unlike
// web_fetch (whose routing rule is stated twice in the system prompt) or exec
// (whose delivery rule is owned by modSandbox), these two schemas are the only home
// of the background-job contract. Trimming them does not remove a duplicate, it
// deletes the explanation. So this test LOCKS the contract instead of budgeting it.

import (
	"encoding/json"
	"strings"
	"testing"
)

func registeredToolJSON(t *testing.T, register func(*Registry), name string) string {
	t.Helper()
	r := NewRegistry("", "")
	register(r)
	for _, def := range r.Definitions() {
		if def.Function.Name != name {
			continue
		}
		raw, err := json.Marshal(def)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		return string(raw)
	}
	t.Fatalf("%s is not registered", name)
	return ""
}

func TestBashOutputSchemaCarriesTheWholeContract(t *testing.T) {
	body := registeredToolJSON(t, registerBashOutput, "bash_output")
	lower := strings.ToLower(body)

	for _, want := range []struct{ marker, why string }{
		{"since the last call", "the cursor semantics are the surprising part: a second call does not repeat the first"},
		{"[status]", "the status line is how the model knows a job finished"},
		{"exited", "…and this is the only wording that guarantees completion"},
		{"[truncated]", "a rolled buffer must be announced, not silently missing"},
		{"filter", "the per-line regex is part of the call, not of the job"},
		{"bash_", "host shells are named bash_N"},
		{"sbg_", "sandbox jobs are named sbg_N — same tool, two backends"},
	} {
		if !strings.Contains(lower, want.marker) {
			t.Errorf("bash_output schema no longer states %q (%s)", want.marker, want.why)
		}
	}
}

func TestKillShellSchemaCarriesItsContract(t *testing.T) {
	body := registeredToolJSON(t, registerKillShell, "kill_shell")
	lower := strings.ToLower(body)

	for _, want := range []struct{ marker, why string }{
		{"sigkill", "what the kill actually does"},
		{"idempotent", "calling it twice must be safe to reason about"},
		{"sbg_", "sandbox jobs are named sbg_N"},
	} {
		if !strings.Contains(lower, want.marker) {
			t.Errorf("kill_shell schema no longer states %q (%s)", want.marker, want.why)
		}
	}
}
