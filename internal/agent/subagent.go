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
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
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

// subagentTurnMargin is what a delegating turn keeps for itself: the
// sub-agent's salvage round (subagentSalvageTimeout) plus the parent's final
// model call, which still has to answer after this tool returns.
//
// Derived, not chosen. The two numbers express one relationship — whatever the
// salvage round is given, the margin has to leave it — and a margin that drifted
// below it would put the sub-agent's expiry back on the parent's clock, which is
// the branch that skips salvaging entirely and returns nothing.
const subagentTurnMargin = subagentSalvageTimeout + 30*time.Second

// subagentWallBudget is the one owner of "how long may this sub-agent run": the
// caller's explicit request, then this agent's configured default, then the
// built-in — and then the turn's clock, which is the ceiling nobody may exceed.
//
// The default is configuration, not ambient state: it arrives on the Agent from
// the resolved config (system → user → agent scope) rather than being read from
// the environment here, so the settings layers and the panel stay the single
// place a default is decided.
//
// The ceiling is the newer half. The caller's number was a wish that nothing
// checked, and since delegate_task runs serially, two 25-minute requests in a
// 45-minute turn meant the second sub-agent could never finish (2026-09-14: the
// turn ended with the tool row still reading "Queued (waiting on prior
// sub-agent)…"). The ceiling is read through turnRemaining, not ctx.Deadline:
// the tool context deliberately has no deadline of its own (P5 grace), so the
// turn's clock has to travel as a value.
//
// A caller with no deadline (cron tick, CLI, tests) has no ceiling to clamp
// against and keeps the configured budget. Returns the effective budget, a
// parenthetical naming the clamp when it bit (empty otherwise), and an error
// when the turn has no room to start at all.
func (a *Agent) subagentWallBudget(ctx context.Context, explicit time.Duration) (time.Duration, string, error) {
	budget := subagentDefaultTimeout
	if a.subagentTimeout > 0 {
		budget = a.subagentTimeout
	}
	if explicit > 0 {
		budget = explicit
	}

	left, ok := tools.TurnRemaining(ctx)
	if !ok {
		return budget, "", nil
	}
	if room := left - subagentTurnMargin; room <= 0 {
		return 0, "", fmt.Errorf(
			"this turn has %s left — less than the %s a sub-agent needs to start, work, and still leave you room to answer. Finish the deliverable from what you already have, or re-issue the delegation in a fresh turn (a long sweep belongs in its own turn, not at the end of this one)",
			left.Round(time.Second), subagentTurnMargin)
	}
	if budget <= left-subagentTurnMargin {
		return budget, "", nil
	}
	clamped := left - subagentTurnMargin
	return clamped, fmt.Sprintf(
		" (clamped from the requested %s to the %s left in this turn, %s of it reserved for finishing the turn)",
		budget, clamped.Round(time.Second), subagentTurnMargin), nil
}

// subagentHeartbeat builds one `subagent_progress` event, naming the call it
// belongs to.
//
// The id is the tool_use id of the delegate_task call this run was spawned by
// (tools.ToolCallID, stamped by the SDK bridge). Without it the dashboard can
// only guess which row a heartbeat belongs to — it takes the first call with no
// result yet, and because a run's exit (`phase:"done"`) is stated the moment the
// run returns — before the delegate_task call itself returns its result — that
// guess names a call that finished long ago for every call but the last in a
// fan-out (2026-09-21). A run reached without an id (RunSubagent called
// directly) emits the event without one; the dashboard falls back to its old
// rule then.
func subagentHeartbeat(ctx context.Context, data map[string]any) ChatEvent {
	if id, ok := tools.ToolCallID(ctx); ok {
		data["id"] = id
	}
	return ChatEvent{Type: "subagent_progress", Data: data}
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
		emitEvent(ctx, subagentHeartbeat(ctx, map[string]any{"phase": "done"}))
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
	budget, budgetNote, err := a.subagentWallBudget(ctx, req.WallTimeout)
	if err != nil {
		return "", err
	}
	if budgetNote != "" {
		// Operators see it too: a sweep that silently got a quarter of the wall
		// time it asked for is otherwise indistinguishable from a slow provider.
		slog.Warn("subagent budget clamped to the turn's remaining time",
			"agent", a.name, "budget", budget.String(), "requested", req.WallTimeout.String())
	}
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
		emitEvent(ctx, subagentHeartbeat(ctx, map[string]any{
			"iteration": i + 1,
			"max":       maxIterations,
			"phase":     "thinking",
		}))

		callTools := toolDefs
		llmMsgs := messages
		if allFailedRounds >= failedRoundsLimit {
			slog.Warn("subagent disabling tools after consecutive failed rounds",
				"agent", a.name, "failed_rounds", allFailedRounds)
			callTools = nil
			llmMsgs = append(llmMsgs, failedRoundsNudge(allFailedRounds, true))
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
					"subagent ran out of its %s wall-time budget at iteration %d%s — task was too large; the parent should retry with a tighter scope, a higher wall_timeout_sec, or both",
					budget, i+1, budgetNote)
				// The same expiry reaches the logs as well as the parent's
				// tool_result: a cancellation that only shows up as a bare
				// "context canceled" downstream (e.g. an exec stream cut
				// mid-read) is otherwise indistinguishable from a sandbox or
				// provider fault, and this name is the whole difference.
				slog.Warn("subagent wall-time budget expired",
					"agent", a.name, "iteration", i+1, "budget", budget.String(),
					"cause", ctx.Err())
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
				messages = append(messages, loopDetectedWarning(true))
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
		emitEvent(ctx, subagentHeartbeat(ctx, map[string]any{
			"iteration": i + 1,
			"max":       maxIterations,
			"phase":     "running",
			"tools":     toolNames,
		}))

		// Same contract as the main loop (loop.go): an in-flight tool gets the
		// grace window to finish after this sub-agent's wall budget expires,
		// instead of being cancelled the instant the budget is. finalizeSubagent
		// already salvages in-flight MODEL work for exactly this reason, so
		// without this the tool path was the one place the budget cut work off
		// with nothing to show for it.
		toolCtx, endToolGrace := toolGraceContext(ctx, a.graceWindow())
		// No per-call callback: a sub-agent's tool results are not surfaced as
		// chat events of their own (the parent turn's row reports the whole
		// delegate_task call).
		results := a.engine.executeToolsConcurrently(toolCtx, a.registry, resp.ToolCalls, a.workspacePath, nil)
		endToolGrace()
		roundAllFailed := true
		for idx, r := range results {
			tc := resp.ToolCalls[idx]
			resultContent, _ := extractToolMeta(r.result)
			// Same backstop as the parent loop: a sub-agent's tool result becomes
			// part of ITS next request too (see sandbox.ClipOutput).
			resultContent = sandbox.ClipAndLog(resultContent, "subagent/"+r.toolName)
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
	emitEvent(parentCtx, subagentHeartbeat(parentCtx, map[string]any{
		"iteration": iteration,
		"phase":     "final-delivery",
	}))

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
