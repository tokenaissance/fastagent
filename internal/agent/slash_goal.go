package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
		return slashResult{handled: true, reply: "No goal set."}
	}
	if err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error reading goal: %v", err)}
	}
	return slashResult{handled: true, reply: fmt.Sprintf("🎯 %s\n%s", g.Status, g.Objective)}
}

func (a *Agent) slashGoalCreate(msg bus.InboundMessage, objective string) slashResult {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return slashResult{handled: true, reply: "Usage: `/goal <objective>`"}
	}

	key := a.resolveSessionKey(msg)
	if key == "" {
		return slashResult{handled: true, reply: "No session context."}
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
	if err := a.goalStore.CreateGoal(context.Background(), g); err != nil {
		if errors.Is(err, goal.ErrAlreadyExists) {
			return slashResult{handled: true, reply: "Goal already exists; `/goal clear` first."}
		}
		return slashResult{handled: true, reply: fmt.Sprintf("Error creating goal: %v", err)}
	}

	// Kick the runtime so the first continuation fires immediately
	// off the user's own /goal turn instead of waiting for the next
	// message. Empty reply on purpose — the continuation streaming
	// in is the acknowledgement. Two replies for one command (a
	// "🎯 Goal set." confirmation plus the actual work response)
	// felt like UI clutter; matching Codex's "type and it just
	// starts" behaviour drops the confirmation.
	if a.goalManager != nil {
		if gr := a.goalManager.Ensure(key, a.name, a.ownerUserID); gr != nil {
			gr.Trigger()
		}
	}
	return slashResult{handled: true, reply: ""}
}

func (a *Agent) slashGoalPause(msg bus.InboundMessage) slashResult {
	return a.transitionGoal(msg, goal.StatusActive, goal.StatusPaused,
		"⏸ Paused.", "Not active.")
}

func (a *Agent) slashGoalResume(msg bus.InboundMessage) slashResult {
	// Empty success reply for the same reason as create — the next
	// continuation streaming in is the visible acknowledgement.
	// wrongStateMsg stays loud so the user knows nothing happened
	// when they resumed a goal that isn't paused.
	res := a.transitionGoal(msg, goal.StatusPaused, goal.StatusActive,
		"", "Not paused.")
	// Triggering after a successful resume kicks the runtime so the
	// next continuation fires without waiting for another user turn.
	// reply=="" signals the success path; non-empty means we hit
	// wrongStateMsg or an error and shouldn't trigger.
	if res.handled && res.reply == "" && a.goalManager != nil {
		key := a.resolveSessionKey(msg)
		if gr := a.goalManager.Ensure(key, a.name, a.ownerUserID); gr != nil {
			gr.Trigger()
		}
	}
	return res
}

// transitionGoal centralizes the "load goal → check it's in the
// expected source state → flip → persist" pattern for pause/resume.
// Returns a slashResult tailored to the outcome.
func (a *Agent) transitionGoal(msg bus.InboundMessage, from, to goal.Status, okMsg, wrongStateMsg string) slashResult {
	key := a.resolveSessionKey(msg)
	g, err := a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	if errors.Is(err, goal.ErrNotFound) || g == nil {
		return slashResult{handled: true, reply: "No goal set."}
	}
	if err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error reading goal: %v", err)}
	}
	if g.Status != from {
		return slashResult{handled: true, reply: wrongStateMsg}
	}
	g.Status = to
	if err := a.goalStore.UpdateGoal(context.Background(), g); err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error updating goal: %v", err)}
	}
	return slashResult{handled: true, reply: okMsg}
}

func (a *Agent) slashGoalClear(msg bus.InboundMessage) slashResult {
	key := a.resolveSessionKey(msg)
	g, err := a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	if errors.Is(err, goal.ErrNotFound) || g == nil {
		return slashResult{handled: true, reply: "No goal set."}
	}
	if err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error reading goal: %v", err)}
	}
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
	return slashResult{handled: true, reply: "🗑 Goal cleared."}
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
