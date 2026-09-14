package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SubagentRequest is everything the tool decides before crossing into the
// agent: the bounded task, its iteration cap, and an optional wall-clock
// budget. A struct rather than positional arguments because these three travel
// together and the tool is not the only thing that will ever want to set them.
//
// WallTimeout of zero means "the agent's configured default" — the tool does
// not know that default, and should not: it belongs to the agent's
// configuration, not to the tool call.
type SubagentRequest struct {
	Task          string
	MaxIterations int
	WallTimeout   time.Duration
}

// SubagentRunner is what the delegate_task tool calls to spawn a
// sub-agent. The agent package implements this on Agent so we avoid
// pulling agent into tools (would form an import cycle).
type SubagentRunner interface {
	RunSubagent(ctx context.Context, req SubagentRequest) (string, error)
}

type delegateTaskArgs struct {
	Task           string `json:"task"`
	ExpectedOutput string `json:"expected_output,omitempty"`
	MaxIterations  int    `json:"max_iterations,omitempty"`
	WallTimeoutSec int    `json:"wall_timeout_sec,omitempty"`
}

// RegisterDelegateTask wires the delegate_task tool. No-op when runner
// is nil so callers can opt out by simply not constructing one (e.g.
// in tests or for agent flavors where sub-agent fan-out doesn't make
// sense).
//
// Registered as SERIAL: two delegate_task calls cannot run
// concurrently within one agent. Sub-agents share the parent's single
// sandbox + single camoufox-cli daemon — running 5 in parallel meant
// they trampled each other's browser navigation state, got back
// snapshots of pages other siblings just navigated to, and produced
// garbage. Serialization trades fan-out wall time (5 × N min instead
// of N min) for actually-correct results.
//
// The tool description explains both the WHY of delegation (parent's
// context stays clean, sub-agent gets a fresh iteration budget) and
// the serial-execution contract so the model doesn't expect parallel
// throughput from fan-out. The "no nesting" line is critical — without
// it flash-tier models try to recursively delegate and burn through
// budgets exponentially.
func RegisterDelegateTask(r *Registry, runner SubagentRunner) {
	if runner == nil {
		return
	}
	r.RegisterSerial("delegate_task",
		"Spawn a sub-agent with its own context and its own iteration budget to run one bounded sub-task. "+
			"Sub-agents run SERIALLY: five calls in one round still execute one at a time (they share your sandbox "+
			"and browser daemon), so a fan-out costs N × the single-run wall time — scope each call small rather "+
			"than expecting parallel throughput.\n\n"+
			"Same tools and provider as you, minus delegate_task itself (no nesting). "+
			"Return: the sub-agent's text as a tool result — you assemble the user's deliverable from it.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task": map[string]interface{}{
					"type":        "string",
					"description": "Self-contained task description: the sub-agent does not see this conversation, so include the criteria, search hints, prior findings to build on, and any constraints it must respect.",
				},
				"expected_output": map[string]interface{}{
					"type":        "string",
					"description": "Optional concrete format, appended to the task verbatim — e.g. \"markdown table: name, city, phone, source_url; one row each; no preamble\".",
				},
				"max_iterations": map[string]interface{}{
					"type":        "integer",
					"description": "Optional override for the sub-agent's tool-iteration budget (default: your own cap, typically 20). Browser-heavy work binds on wall time long before the iteration count, so 12-18 is the practical ceiling there; 20-30 for web_search/web_fetch; 3-5 for pure synthesis.",
				},
				"wall_timeout_sec": map[string]interface{}{
					"type":        "integer",
					"description": "Optional wall-clock budget in seconds; unset uses the agent's default (15 min unless the operator changed subagentTimeoutSec). Set it for legitimately long sweeps instead of forcing them into the default. On expiry the sub-agent gets one tools-free round to write down what it has, and that text returns with the failure note — a truncated result is marked partial, never silent. The parent turn's own ceiling still applies.",
				},
			},
			"required": []string{"task"},
		},
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args delegateTaskArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("parse args: %w", err)
			}
			if args.Task == "" {
				return "", fmt.Errorf("task is required")
			}
			taskPrompt := args.Task
			if args.ExpectedOutput != "" {
				taskPrompt += "\n\n## Expected output format\n\n" + args.ExpectedOutput
			}
			out, err := runner.RunSubagent(ctx, SubagentRequest{
				Task:          taskPrompt,
				MaxIterations: args.MaxIterations,
				WallTimeout:   time.Duration(args.WallTimeoutSec) * time.Second,
			})
			if err != nil {
				// Surface the error inside the tool_result so the parent
				// sees it as a normal tool failure (gets the "analyze
				// the error and try a different approach" envelope from
				// the registry) rather than a hard tool-execution error.
				//
				// When the sub-agent ran out of wall time it hands back
				// whatever it managed to write. Dropping that text would
				// throw away the fetched material the parent needs to
				// finish the job, so keep both: the partial artifact, and
				// the reason it is partial.
				if partial := strings.TrimSpace(out); partial != "" {
					return fmt.Sprintf(
						"%s\n\n---\n[subagent stopped early: %s]\nThe text above is what the sub-agent had produced when it ran out of budget — partial, and not yet verified by it. Reuse it, fill the gaps, and re-issue only what is missing.",
						partial, err.Error()), err
				}
				return fmt.Sprintf("[subagent failed: %s]", err.Error()), err
			}
			return out, nil
		})
}
