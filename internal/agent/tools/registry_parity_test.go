package tools

// The registry used to carry two copies of nine tools — a host copy and a
// sandbox copy installed by SetExecutor — so every schema fix had to land on
// both copies or it shipped to nobody. TurnScope removed the second copy: one
// registration now dispatches from the turn's executor at call time.
//
// These cases pin the shape that replaced the parity rule:
//   - the advertised schema does not depend on whether a turn has an executor;
//   - exec still advertises both the shared parameters and the host-only
//     `sandbox` switch.

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// parityExecutor exists only so a turn takes the sandbox branch.
type parityExecutor struct{}

func (parityExecutor) Exec(context.Context, string, time.Duration) (string, error) { return "", nil }
func (parityExecutor) ReadFile(context.Context, string) (string, error)            { return "", nil }
func (parityExecutor) WriteFile(context.Context, string, string) (string, error)   { return "", nil }
func (parityExecutor) ListDir(context.Context, string) (string, error)             { return "", nil }
func (parityExecutor) Backend() string                                             { return "parity" }
func (parityExecutor) Close() error                                                { return nil }

var _ sandbox.Executor = parityExecutor{}

type toolPayload struct {
	description string
	properties  map[string]interface{}
	required    []string
}

func registryPayloads(t *testing.T, r *Registry) map[string]toolPayload {
	t.Helper()
	out := map[string]toolPayload{}
	for _, def := range r.Definitions() {
		raw, err := json.Marshal(def.Function.Parameters)
		if err != nil {
			t.Fatalf("marshal %s: %v", def.Function.Name, err)
		}
		var params map[string]interface{}
		if err := json.Unmarshal(raw, &params); err != nil {
			t.Fatalf("unmarshal %s: %v", def.Function.Name, err)
		}
		var required []string
		if rs, ok := params["required"].([]interface{}); ok {
			for _, v := range rs {
				required = append(required, v.(string))
			}
		}
		props, _ := params["properties"].(map[string]interface{})
		out[def.Function.Name] = toolPayload{description: def.Function.Description, properties: props, required: required}
	}
	return out
}

func toolNames(m map[string]toolPayload) []string {
	var names []string
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func payloadJSON(t *testing.T, p toolPayload) string {
	t.Helper()
	b, err := json.Marshal(map[string]interface{}{"description": p.description, "properties": p.properties, "required": p.required})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// A per-turn executor changes where a tool runs, never what the model is told
// the tool accepts.
func TestToolSchemaDoesNotDependOnTheTurnExecutor(t *testing.T) {
	host := registryPayloads(t, NewRegistry(t.TempDir(), t.TempDir()))

	sbxReg := NewRegistry(t.TempDir(), t.TempDir())
	bindTestTurn(sbxReg, &TurnScope{Executor: parityExecutor{}})
	sandboxed := registryPayloads(t, sbxReg)

	hostNames, sandboxNames := toolNames(host), toolNames(sandboxed)
	if len(hostNames) != len(sandboxNames) {
		t.Fatalf("tool sets differ: host %v, sandbox %v", hostNames, sandboxNames)
	}
	for i := range hostNames {
		if hostNames[i] != sandboxNames[i] {
			t.Fatalf("tool sets differ: host %v, sandbox %v", hostNames, sandboxNames)
		}
	}
	for _, name := range hostNames {
		if got, want := payloadJSON(t, sandboxed[name]), payloadJSON(t, host[name]); got != want {
			t.Errorf("%s advertises a different schema on a sandbox turn:\n  sandbox: %s\n  host:    %s", name, got, want)
		}
	}
}

// exec has one registration, so the host-only switch and the shared parameters
// must all be present in that single schema.
func TestExecSchemaKeepsEveryParameter(t *testing.T) {
	p := registryPayloads(t, NewRegistry(t.TempDir(), t.TempDir()))["exec"]
	for _, key := range []string{"command", "stdin", "timeout", "run_in_background", "allow_long_wait", "sandbox"} {
		if _, ok := p.properties[key]; !ok {
			t.Errorf("exec schema lost %q", key)
		}
	}
}
