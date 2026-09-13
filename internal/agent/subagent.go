package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// subagentDefaultTimeout caps the wall time one subagent can spend on
// its loop, independent of how long the parent's overall turn has left.
// Big enough for ~15-20 camoufox-cli-driven iterations including cold-
// start. Smaller than agentTurnTimeout so a parallel fan-out where one
// subagent goes slow doesn't take the rest down with it; parent ctx
// cancel still propagates so a genuinely killed parent kills every
// subagent.
const subagentDefaultTimeout = 15 * time.Minute

// subagentSalvageTimeout bounds the tools-free round that writes down what the
// sub-agent already gathered when its wall budget runs out. Without it a
// budget expiry threw away every fetched document the sub-agent had collected:
// the loop returned ("", err) and the parent only saw a failure note.
const subagentSalvageTimeout = 90 * time.Second

// subagentWallBudget resolves the wall-clock budget for one sub-agent: the
// caller's explicit request, then this agent's configured default, then the
// built-in. The default is configuration, not ambient state — it arrives on the
// Agent from the resolved config (system → user → agent scope) rather than
// being read from the environment here, so the settings layers and the panel
// stay the single place a default is decided.
func (a *Agent) subagentWallBudget(explicit time.Duration) time.Duration {
	if explicit > 0 {
		return explicit
	}
	if a.subagentTimeout > 0 {
		return a.subagentTimeout
	}
	return subagentDefaultTimeout
}

// RunSubagent implements tools.SubagentRunner so the delegate_task tool
// can call back into the Agent without creating an import cycle.
//
// Always emits a `subagent_progress` event with phase="done" on exit
// (success, error, or panic-via-defer) so the frontend's "currently
// delegating" indicator can clear cleanly between serial sub-agent
// runs — even when the next sub-agent doesn't start immediately or
// the parent decides to handle the result before issuing another
// delegate_task.
func (a *Agent) RunSubagent(ctx context.Context, req tools.SubagentRequest) (out string, err error) {
	defer func() {
		emitEvent(ctx, ChatEvent{Type: "subagent_progress", Data: map[string]any{
			"phase": "done",
		}})
	}()
	return a.runSubagentLoop(ctx, req)
}

// runSubagentLoop is a self-contained ReAct loop used by delegate_task.
//
// What it shares with HandleMessage:
//   - the parent's provider, model, tool registry, and SDK engine
//   - the same loop-detection, all-failed-rounds-disable-tools, and
//     cap-reached forced-delivery patterns
//
// What it deliberately does NOT do (vs HandleMessage):
//   - no session persistence — the sub-agent's working messages live in
//     a private slice and never touch session_messages
//   - no chat-event emission — the parent's chat UI sees the
//     delegate_task tool call + final tool_result only, not the sub-
//     agent's intermediate steps
//   - no hooks, no skill-store refresh, no compaction, no runPostTurn
//   - no slash-command / plan-mode short-circuit (caller is the parent
//     model via the delegate_task tool, not a human composer)
//
// delegate_task itself is filtered out of the sub-agent's toolset so
// sub-agents can't spawn further sub-agents (v1 nesting limit).
//
// Return contract: the final synthesized text in all "we got something"
// cases — clean exit, cap-hit forced delivery, budget-expiry salvage, or
// loop-detection abort. A non-nil error is returned only for plumbing failures
// (no provider, transient API error during a Chat call); callers fold that into
// the tool_result so the parent agent can react. When the wall budget expires
// the text returned alongside the error is whatever the sub-agent managed to
// write — see finalizeSubagent.
func (a *Agent) runSubagentLoop(ctx context.Context, req tools.SubagentRequest) (string, error) {
	if a.provider == nil {
		return "", fmt.Errorf("agent has no provider configured")
	}
	maxIterations := req.MaxIterations
	if maxIterations <= 0 {
		maxIterations = a.maxToolIterations
	}
	if maxIterations <= 0 {
		maxIterations = 20
	}

	// Each subagent gets its own bounded ctx so a slow sibling can't
	// drain the rest of a parallel fan-out. Parent cancel still wins —
	// we're wrapping, not detaching. Keep the parent ctx: telling "my budget
	// expired" from "the parent gave up" is what decides whether salvaging is
	// allowed to spend one more round.
	budget := a.subagentWallBudget(req.WallTimeout)
	parentCtx := ctx
	subCtx, cancel := context.WithTimeout(parentCtx, budget)
	defer cancel()
	ctx = subCtx
	// lastContent is the newest thing the model actually wrote. Tool-calling
	// rounds carry their prose in the same message, so this is usually non-empty
	// long before the loop ends — it is the floor salvage falls back to.
	lastContent := ""

	systemPrompt := a.ctxBuilder.BuildSystemPrompt() + subagentSystemSuffix()
	messages := []provider.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: req.Task},
	}

	// Filter delegate_task out of the sub-agent's toolset — no nesting
	// in v1. Other tools (web_fetch, exec, file ops, MCP, …) flow
	// through unchanged.
	var toolDefs []provider.Tool
	for _, t := range a.registry.Definitions() {
		if t.Function.Name == "delegate_task" {
			continue
		}
		toolDefs = append(toolDefs, t)
	}

	type sig struct {
		name string
		hash [32]byte
	}
	var lastSig sig
	consecutiveCount := 0
	allFailedRounds := 0
	const failedRoundsLimit = 3

	for i := 0; i < maxIterations; i++ {
		slog.Info("subagent iteration",
			"agent", a.name,
			"iteration", i+1,
			"max", maxIterations,
		)
		// Heartbeat to the parent's chat stream so the UI can show
		// the user that a sub-agent is making progress and where it
		// is. Without this the delegate_task tool card looks frozen
		// for the entire sub-agent run (often 5-15 min). We emit at
		// the start of every iteration plus right before tool execution
		// (with the tool name) so the user sees both "thinking" and
		// "running web_search" phases.
		emitEvent(ctx, ChatEvent{Type: "subagent_progress", Data: map[string]any{
			"iteration": i + 1,
			"max":       maxIterations,
			"phase":     "thinking",
		}})

		callTools := toolDefs
		llmMsgs := messages
		if allFailedRounds >= failedRoundsLimit {
			slog.Warn("subagent disabling tools after consecutive failed rounds",
				"agent", a.name, "failed_rounds", allFailedRounds)
			callTools = nil
			llmMsgs = append(llmMsgs, provider.Message{
				Role: "system",
				Content: fmt.Sprintf(
					"The last %d rounds of tool calls all failed (HTTP 4xx/5xx or empty results). Stop calling tools and produce the deliverable from what you already gathered, with explicit gaps marked.",
					allFailedRounds,
				),
			})
		}

		resp, err := a.provider.Chat(ctx, llmMsgs, callTools, a.model, a.maxTokens, a.temperature)
		if err != nil {
			// If the ctx itself expired, the parent caller has more
			// useful framing than "context deadline exceeded" mid-
			// stream — surface the timeout explicitly so the parent
			// agent can decide to retry with a tighter task scope.
			if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				if parentCtx.Err() != nil {
					// The parent's turn is gone: no point spending another
					// round, and nothing is listening for the result.
					return "", fmt.Errorf("subagent cancelled with its parent at iteration %d: %w", i+1, parentCtx.Err())
				}
				budgetErr := fmt.Errorf(
					"subagent ran out of its %s wall-time budget at iteration %d — task was too large; the parent should retry with a tighter scope, a higher wall_timeout_sec, or both",
					budget, i+1)
				text, ferr := a.finalizeSubagent(parentCtx, messages, lastContent, budgetNudge(budget), i+1, budget)
				if ferr != nil {
					slog.Warn("subagent finalization after budget expiry failed",
						"agent", a.name, "iteration", i+1, "error", ferr)
				}
				return text, budgetErr
			}
			return "", fmt.Errorf("subagent chat failed at iteration %d: %w", i+1, err)
		}

		if !resp.HasToolCalls() {
			return resp.Content, nil
		}
		if strings.TrimSpace(resp.Content) != "" {
			lastContent = resp.Content
		}

		messages = append(messages, provider.Message{
			Role:         "assistant",
			Content:      resp.Content,
			ToolCalls:    resp.ToolCalls,
			Thinking:     resp.Thinking,
			RawAssistant: resp.RawAssistant,
		})

		// Loop detection: same shape as HandleMessage but on private state.
		loopDetected := false
		for _, tc := range resp.ToolCalls {
			s := sig{name: tc.Function.Name, hash: sha256.Sum256([]byte(tc.Function.Arguments))}
			if s.name == lastSig.name && s.hash == lastSig.hash {
				consecutiveCount++
			} else {
				consecutiveCount = 1
				lastSig = s
			}
			if consecutiveCount >= 3 {
				slog.Warn("subagent tool-loop detected", "agent", a.name, "tool", tc.Function.Name)
				messages = append(messages, provider.Message{
					Role:    "system",
					Content: "Loop detected: same tool with same arguments 3 times. Stop and produce the deliverable from what you have.",
				})
				loopDetected = true
				break
			}
		}
		if loopDetected {
			break
		}

		// Second heartbeat: tools are about to run. Surface their names
		// so the UI can show "running web_search" / "running exec
		// (camoufox-cli open …)" instead of just a spinner.
		toolNames := make([]string, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			toolNames = append(toolNames, tc.Function.Name)
		}
		emitEvent(ctx, ChatEvent{Type: "subagent_progress", Data: map[string]any{
			"iteration": i + 1,
			"max":       maxIterations,
			"phase":     "running",
			"tools":     toolNames,
		}})

		results := a.engine.executeToolsConcurrently(ctx, a.registry, resp.ToolCalls, a.workspacePath)
		roundAllFailed := true
		for idx, r := range results {
			tc := resp.ToolCalls[idx]
			resultContent, _ := extractToolMeta(r.result)
			if !isFailedToolResult(r.err, resultContent) {
				roundAllFailed = false
			}
			messages = append(messages, provider.Message{
				Role:       "tool",
				Content:    resultContent,
				ToolCallID: tc.ID,
				Name:       r.toolName,
			})
		}
		if roundAllFailed {
			allFailedRounds++
		} else {
			allFailedRounds = 0
		}
	}

	// Cap reached — finalize with tools off, same as a budget expiry does.
	slog.Warn("subagent max iterations reached — forcing final delivery",
		"agent", a.name, "max", maxIterations)
	text, err := a.finalizeSubagent(ctx, messages, lastContent, capReachedNudge(maxIterations), maxIterations, budget)
	if err != nil {
		return text, fmt.Errorf("subagent forced final delivery failed: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Sprintf("[subagent reached %d-iteration limit without producing a final answer]", maxIterations), nil
	}
	return text, nil
}

// finalizeSubagent is the sub-agent's single "last word" path: one tools-free
// round on a fresh bounded deadline, falling back to the prose the model
// already produced. Both exits that have material worth keeping — the iteration
// cap and the wall budget — go through here, so the fallback rules exist once.
//
// Why: the budget used to end the sub-agent with ("", err), so up to fifteen
// minutes of fetched documentation and numbers were discarded and the parent
// received only a failure note. The material is already in `messages` as tool
// results; one more round turns it into something the parent can use, and the
// caller keeps its own reason for the truncation so partial work is never
// mistaken for a complete answer.
//
// The fresh ctx is detached from an expired one but still bounded; callers only
// reach this when the parent is alive, because a cancelled parent means nobody
// is waiting for the result.
func (a *Agent) finalizeSubagent(parentCtx context.Context, messages []provider.Message, lastContent string, nudge provider.Message, iteration int, budget time.Duration) (string, error) {
	emitEvent(parentCtx, ChatEvent{Type: "subagent_progress", Data: map[string]any{
		"iteration": iteration,
		"phase":     "final-delivery",
	}})

	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), subagentSalvageTimeout)
	defer cancel()

	resp, err := a.provider.Chat(finalCtx, append(messages, nudge), nil, a.model, a.maxTokens, a.temperature)
	if err == nil && strings.TrimSpace(resp.Content) != "" {
		slog.Info("subagent finalized from gathered material",
			"agent", a.name, "iteration", iteration, "budget", budget)
		return resp.Content, nil
	}
	if trimmed := strings.TrimSpace(lastContent); trimmed != "" {
		slog.Info("subagent finalization produced nothing usable; returning earlier prose",
			"agent", a.name, "iteration", iteration, "error", err)
		return lastContent, nil
	}
	return "", err
}

// budgetNudge is finalizeSubagent's instruction for a run that ran out of
// wall-clock rather than iterations — same contract, different stated reason.
func budgetNudge(budget time.Duration) provider.Message {
	return provider.Message{
		Role: "system",
		Content: fmt.Sprintf(
			"Your %s wall-time budget is exhausted. Tools are disabled for this final response — do not attempt to call any. "+
				"Write the deliverable now from what you have already gathered, in the requested format, and mark anything you could not confirm as 'unknown' / 'partial' / [UNVERIFIED]. "+
				"Producing a complete-but-shorter artifact beats apologizing or explaining what you would have done.",
			budget),
	}
}

// subagentSystemSuffix is appended to the agent's normal system prompt
// when running under runSubagentLoop. Spells out the contract: the
// reply is a tool result for the parent, not chat with a human. Without
// this, sub-agents kept producing chatty "Hi! I'll help you find …"
// preambles that the parent then had to strip before splicing.
func subagentSystemSuffix() string {
	return "\n\n# Subagent mode\n\n" +
		"You are running as a delegated sub-agent invoked by a parent " +
		"agent via the `delegate_task` tool. Your reply is consumed as a " +
		"tool result, not displayed to a human as chat. Follow these " +
		"rules strictly:\n\n" +
		"- Output **only** the deliverable the task asks for. No " +
		"preamble (\"Sure, I'll help…\"), no reassurance, no follow-up " +
		"questions, no offers to continue.\n" +
		"- If the task specifies an output format (table, JSON, " +
		"markdown rows), produce exactly that format — the parent " +
		"splices your output into a larger result.\n" +
		"- If you can't complete the task, return a brief note " +
		"explaining what you got and what blocked you. Partial " +
		"structured output beats no output.\n" +
		"- You have the parent's full tool set except `delegate_task` " +
		"itself (no nesting). Use them as normal.\n" +
		"- You don't see the parent's prior conversation. Everything " +
		"you need to do this task is in the user message below."
}
