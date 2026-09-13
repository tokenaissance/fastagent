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
		"Spawn a sub-agent with its OWN context and OWN iteration budget to run a single bounded sub-task. "+
			"Use this when the user's request decomposes into several large independent chunks "+
			"(e.g. \"find 10 leads matching X\" then \"find another 10 matching Y\" then \"write 5 emails from this data\"). "+
			"Each sub-agent gets a fresh tool-iteration budget so you don't burn yours exploring, and your own context "+
			"stays clean of the dozens of intermediate tool results the sub-agent goes through. "+
			"\n\n**Sub-agents run SERIALLY, not in parallel.** Even if you emit 5 delegate_task calls in one round, "+
			"they execute one at a time — they share the single sandbox + single browser daemon, so parallel "+
			"execution would trample each other's state. Expect the wall-clock time of a fan-out to be N × the single-"+
			"sub-agent time, not 1× it. Plan accordingly: smaller per-sub-agent scope is better than fewer, larger calls.\n\n"+
			"The sub-agent runs against the same tools and provider you have (minus delegate_task itself — no nesting). "+
			"It cannot see your prior conversation, so pass everything it needs in the `task` arg: criteria, search hints, "+
			"earlier findings to build on, output format. Sub-agents are best for tasks that produce a self-contained "+
			"artifact (a table, a draft email, a structured summary). "+
			"\n\nReturn: the sub-agent's final text exactly as it produced it. You then assemble multiple sub-agent "+
			"results into the final deliverable for the user.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task": map[string]interface{}{
					"type":        "string",
					"description": "Self-contained task description. The sub-agent does NOT see your prior conversation — include all the context it needs to act: criteria, search hints, prior findings it should build on, region / language constraints, anything the sub-agent must respect.",
				},
				"expected_output": map[string]interface{}{
					"type":        "string",
					"description": "Optional concrete format the sub-agent should produce — e.g. \"markdown table with columns: name, city, owner, phone, source_url; one row per business; no preamble\". Appended to the task verbatim so the format spec is unambiguous.",
				},
				"max_iterations": map[string]interface{}{
					"type":        "integer",
					"description": "Optional override for the sub-agent's tool-iteration budget. Default is the same cap as your turn (typically 20). REALISTIC BUDGETS: for browser-heavy sub-tasks (camoufox-cli — each open/snapshot/click is 1-30s real time, plus a 2-3 min cold-start on the first call) the wall clock binds long before the iteration count, so 12-18 is the practical ceiling and setting 40 just wastes the parameter. For quick web_search / web_fetch sub-tasks, 20-30 is fine. For pure synthesis (no tools), 3-5 is enough.",
				},
				"wall_timeout_sec": map[string]interface{}{
					"type":        "integer",
					"description": "Optional wall-clock budget for this sub-agent in seconds. Leave it unset to use the agent's configured default (15 minutes unless the operator changed subagentTimeoutSec). Set it when a sub-task is legitimately long — a multi-source research sweep, a browser-heavy crawl — instead of forcing the work into the default. Two constraints still apply: the whole fan-out is serial, so N sub-agents cost N × their budget, and the parent turn has its own 45-minute ceiling. When the budget expires the sub-agent gets one tools-free round to write down what it already gathered, and that partial text is returned with the failure note — so a truncated result is marked, never silently partial.",
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
