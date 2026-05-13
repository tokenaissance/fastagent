package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// slashGoal dispatches `/goal …` to one of the sub-handlers. The
// argument grammar mirrors the design doc § 6:
//
//	/goal <objective>          → create
//	/goal                      → show current
//	/goal pause | resume | clear
//
// `/goal budget <N>` is deliberately absent — Codex doesn't ship it
// and mid-flight budget changes have ambiguous semantics (do tokens
// already spent count?). Set the budget at create time instead.
func (a *Agent) slashGoal(msg bus.InboundMessage, args []string) slashResult {
	if a.goalStore == nil {
		return slashResult{
			handled: true,
			reply:   "The /goal feature isn't enabled on this install (no data store configured).",
		}
	}

	// First arg may be a sub-command. Anything else is treated as
	// objective text for the create path. `/goal pause objective`
	// would otherwise be ambiguous, but pause/resume/clear are short
	// keywords nobody would use as an objective opener.
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "":
		return a.slashGoalShow(msg)
	case "pause":
		return a.slashGoalPause(msg)
	case "resume":
		return a.slashGoalResume(msg)
	case "clear":
		return a.slashGoalClear(msg)
	}
	// Default: treat the entire remainder as objective text.
	objective := strings.Join(args, " ")
	return a.slashGoalCreate(msg, objective)
}

// resolveSessionKey returns the persistent session_key for the
// in-flight turn. Defensive: if `Manager.Get` returns nil (an
// already-stopped manager, an out-of-context call), the slash
// handlers downgrade to a clean "feature not available here"
// message rather than dereferencing nil.
func (a *Agent) resolveSessionKey(msg bus.InboundMessage) string {
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	if sess == nil {
		return ""
	}
	return sess.SessionKey()
}

func (a *Agent) slashGoalShow(msg bus.InboundMessage) slashResult {
	key := a.resolveSessionKey(msg)
	g, err := a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	if errors.Is(err, goal.ErrNotFound) || g == nil {
		return slashResult{handled: true, reply: "No goal set for this session.\n\nUse `/goal <objective>` to create one."}
	}
	if err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error reading goal: %v", err)}
	}
	return slashResult{handled: true, reply: formatGoalView(g)}
}

func (a *Agent) slashGoalCreate(msg bus.InboundMessage, objective string) slashResult {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return slashResult{handled: true, reply: "Usage: `/goal <objective>`\n\nExample: `/goal Translate README.md into English and save to /tmp/readme.en.md; verify the line count matches.`"}
	}

	key := a.resolveSessionKey(msg)
	if key == "" {
		return slashResult{handled: true, reply: "Cannot create a goal here — no session context."}
	}

	g := &goal.Goal{
		ID:          newGoalID(),
		AgentID:     a.name,
		SessionKey:  key,
		OwnerUserID: a.ownerUserID,
		Channel:     msg.Channel,
		AccountID:   msg.AccountID,
		ChatID:      msg.ChatID,
		ProjectID:   msg.ProjectID,
		Objective:   objective,
		Status:      goal.StatusActive,
	}
	// Scaffold: a too-short or verify-less objective gets created as
	// Paused instead of Active. The user sees a nudge to add
	// verification criteria + has to `/goal resume` (or clear+re-do)
	// before continuation fires. Cheap heuristic — exact criteria
	// are documented in objectiveLooksWeak; tuned not to false-
	// positive on the well-shaped objectives in §5.7's example.
	weak := objectiveLooksWeak(objective)
	if weak {
		g.Status = goal.StatusPaused
	}
	if err := a.goalStore.CreateGoal(context.Background(), g); err != nil {
		if errors.Is(err, goal.ErrAlreadyExists) {
			return slashResult{handled: true, reply: "A goal already exists for this session. Run `/goal clear` first, or `/goal` to inspect it."}
		}
		return slashResult{handled: true, reply: fmt.Sprintf("Error creating goal: %v", err)}
	}

	if weak {
		return slashResult{
			handled: true,
			reply:   fmt.Sprintf("🎯 Goal created (paused for review).\n\n%s\n%s", formatGoalView(g), weakObjectiveHint()),
			events:  []ChatEvent{goalCreatedEvent(g)},
		}
	}

	// Mint + kick the runtime. The runtime would also come up via
	// the trigger hook on the next turn, but kicking it here means
	// the very first continuation fires off the user's own /goal
	// turn instead of waiting for them to send something else.
	if a.goalManager != nil {
		if gr := a.goalManager.Ensure(key, a.name, a.ownerUserID); gr != nil {
			gr.Trigger()
		}
	}
	return slashResult{
		handled: true,
		reply:   fmt.Sprintf("🎯 Goal set.\n\n%s", formatGoalView(g)),
		events:  []ChatEvent{goalCreatedEvent(g)},
	}
}

// objectiveLooksWeak heuristically detects an under-specified
// objective. Heuristics in priority order:
//
//   - <40 chars total: too short to encode scope + verification
//   - no English or Chinese verification keyword (verify / verified /
//     check / test / 验证 / 检查 / 验收 / passes / expect / ensure):
//     the user didn't tell the agent how to know it succeeded
//
// Tuned to false-negative on the design § 5.7 good example
// ("Reduce p95 render time... run scripts/perf-dashboard.ts to
// verify; do not memoize in src/lib/") and false-positive on its
// bad example ("fix the slow dashboard"). Future drift in either
// direction is fine — the only consequence is one extra /goal
// resume keystroke for the user.
func objectiveLooksWeak(objective string) bool {
	if len(objective) < 40 {
		return true
	}
	lower := strings.ToLower(objective)
	keywords := []string{
		"verify", "verified", "verification",
		"check", "test", "tests",
		"expect", "ensure", "assert",
		"passes", "should",
		// CJK markers — fastclaw is bilingual; without these a
		// Chinese-speaking user's "用 X 验证" objective would
		// always get flagged.
		"验证", "检查", "验收", "确认",
	}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) || strings.Contains(objective, kw) {
			return false
		}
	}
	return true
}

func weakObjectiveHint() string {
	return "⚠ This objective looks under-specified.\n" +
		"For best results, add:\n" +
		"  • Specific file paths / modules to touch\n" +
		"  • An explicit verification path (a command, a test, a file existence check)\n" +
		"  • Anything that should NOT be changed\n\n" +
		"When you're ready:\n" +
		"  `/goal resume`     — proceed with the current objective\n" +
		"  `/goal clear`      — discard and re-create with a clearer one"
}

func (a *Agent) slashGoalPause(msg bus.InboundMessage) slashResult {
	return a.transitionGoal(msg, goal.StatusActive, goal.StatusPaused,
		"⏸ Goal paused. Use `/goal resume` to continue.",
		"Goal is not active — nothing to pause.",
		"user_paused")
}

func (a *Agent) slashGoalResume(msg bus.InboundMessage) slashResult {
	res := a.transitionGoal(msg, goal.StatusPaused, goal.StatusActive,
		"▶ Goal resumed.",
		"Goal is not paused — `/goal` to see current status.",
		"user_resumed")
	// Triggering after a successful resume kicks the runtime so the
	// next continuation fires without waiting for another user turn.
	if res.handled && strings.HasPrefix(res.reply, "▶") && a.goalManager != nil {
		key := a.resolveSessionKey(msg)
		if gr := a.goalManager.Ensure(key, a.name, a.ownerUserID); gr != nil {
			gr.Trigger()
		}
	}
	return res
}

// transitionGoal centralizes the "load goal → check it's in the
// expected source state → flip → persist" pattern for pause/resume.
// Returns a slashResult tailored to the outcome. On success emits a
// goal_status_changed event tagged with reason so the frontend can
// distinguish a user toggle from a runtime-driven transition.
func (a *Agent) transitionGoal(msg bus.InboundMessage, from, to goal.Status, okMsg, wrongStateMsg, reason string) slashResult {
	key := a.resolveSessionKey(msg)
	g, err := a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	if errors.Is(err, goal.ErrNotFound) || g == nil {
		return slashResult{handled: true, reply: "No goal set for this session."}
	}
	if err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error reading goal: %v", err)}
	}
	if g.Status != from {
		return slashResult{handled: true, reply: wrongStateMsg + "\nCurrent status: " + string(g.Status)}
	}
	g.Status = to
	if err := a.goalStore.UpdateGoal(context.Background(), g); err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error updating goal: %v", err)}
	}
	return slashResult{
		handled: true,
		reply:   okMsg,
		events:  []ChatEvent{goalStatusChangedEvent(g, reason)},
	}
}

func (a *Agent) slashGoalClear(msg bus.InboundMessage) slashResult {
	key := a.resolveSessionKey(msg)
	g, err := a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	if errors.Is(err, goal.ErrNotFound) || g == nil {
		return slashResult{handled: true, reply: "No goal set for this session."}
	}
	if err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error reading goal: %v", err)}
	}
	clearedID := g.ID
	if err := a.goalStore.DeleteGoal(context.Background(), g.ID); err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error clearing goal: %v", err)}
	}
	// Stop the runtime so a future /goal in the same session starts
	// with a fresh goroutine — without StopSession, the cached idle
	// runtime would keep checking the now-deleted row until its 30
	// min self-shutdown.
	if a.goalManager != nil {
		a.goalManager.StopSession(key)
	}
	return slashResult{
		handled: true,
		reply:   "🗑 Goal cleared.",
		events:  []ChatEvent{goalClearedEvent(clearedID)},
	}
}

// clearGoalForSession removes any goal attached to the named
// session_key + stops the matching runtime. Called by /new and
// /reset so an old session's goal doesn't leak into a brand-new
// conversation thread on the same chat. Best-effort: store errors
// are logged at the caller, not surfaced — /new shouldn't fail
// because of a stray goal row.
func (a *Agent) clearGoalForSession(sessionKey string) {
	if a.goalStore == nil || sessionKey == "" {
		return
	}
	g, err := a.goalStore.GetGoalBySession(context.Background(), a.name, sessionKey)
	if err != nil || g == nil {
		return
	}
	_ = a.goalStore.DeleteGoal(context.Background(), g.ID)
	if a.goalManager != nil {
		a.goalManager.StopSession(sessionKey)
	}
}

// formatGoalView renders a Goal as a chat-friendly status block.
// Mirrors the fields get_goal exposes to the model so the human and
// the model see the same shape; just rearranged for terminal
// readability.
func formatGoalView(g *goal.Goal) string {
	var sb strings.Builder
	sb.WriteString("🎯 Goal\n")
	sb.WriteString("─────────────────\n")
	sb.WriteString("Status:      " + string(g.Status) + "\n")
	sb.WriteString("Tokens used: " + strconv.FormatInt(g.TokensUsed, 10))
	if g.TokenBudget != nil {
		sb.WriteString(" / " + strconv.FormatInt(*g.TokenBudget, 10))
		if remaining, ok := g.RemainingTokens(); ok {
			sb.WriteString(" (" + strconv.FormatInt(remaining, 10) + " remaining)")
		}
	} else {
		sb.WriteString(" / unbounded")
	}
	sb.WriteString("\n")
	if g.TimeUsedSeconds > 0 {
		sb.WriteString("Time spent:  " + strconv.FormatInt(g.TimeUsedSeconds, 10) + "s\n")
	}
	sb.WriteString("Iterations:  " + strconv.Itoa(g.Iterations) + "\n")
	sb.WriteString("Objective:\n")
	sb.WriteString("  " + g.Objective + "\n")
	return sb.String()
}

// newGoalID mints a fresh opaque id for a Goal row. Mirrors the
// helper in internal/agent/tools/goal.go — kept duplicated rather
// than exported to avoid pulling that package into the slash path.
// Slash creation is rare; the small duplication is the right
// tradeoff against an importable surface.
func newGoalID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("slash_goal: crypto/rand failed: %v", err))
	}
	return "g-" + hex.EncodeToString(buf[:])
}
