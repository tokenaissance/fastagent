package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/session"
)

// newSlashTestAgent builds an Agent wired enough to exercise the
// slash_goal.go handlers: store + manager + a real session.Manager
// (file-backed in a temp dir) so resolveSessionKey returns a stable
// key without dragging in the rest of NewAgent's machinery.
func newSlashTestAgent(t *testing.T) *Agent {
	t.Helper()
	a := &Agent{
		name:        "agent-test",
		ownerUserID: "user-1",
		registry:    tools.NewRegistry("", ""),
		hooks:       NewHookRegistry(),
		messageBus:  bus.New(),
		sessions:    session.NewManager(t.TempDir()),
	}
	a.WireGoals(&memGoalStore{})
	t.Cleanup(func() {
		if a.goalManager != nil {
			a.goalManager.Shutdown()
		}
	})
	return a
}

// webMsg is the canonical InboundMessage shape the slash tests use —
// web channel, fixed account/chat so resolveSessionKey returns a
// reproducible session_key across calls within a single test.
func webMsg() bus.InboundMessage {
	return bus.InboundMessage{
		Channel:   "web",
		AccountID: "",
		ChatID:    "chat-1",
	}
}

// strongObjective is the canonical objective most slash tests use
// when they don't care about objective content — anything non-empty
// would do, but a stable string keeps test reply assertions diffable.
// (Name is historical: there used to be a weak-objective heuristic
// this string was specifically shaped to bypass; that heuristic was
// removed, but the name kept its callers stable.)
const strongObjective = "Translate fixture data into output.json; verify rows match expected count"

// strongArgs splits strongObjective on whitespace — slash handlers
// receive their objective as []string, the way the dispatcher in
// slash.go feeds them.
func strongArgs() []string { return strings.Fields(strongObjective) }

func TestSlashGoalShowEmptySession(t *testing.T) {
	a := newSlashTestAgent(t)
	res := a.slashGoal(webMsg(), nil)
	if !res.handled {
		t.Fatal("expected handled=true")
	}
	if !strings.Contains(res.reply, "No goal set") {
		t.Errorf("reply doesn't read like an empty-state message:\n%s", res.reply)
	}
}

func TestSlashGoalCreateHappyPath(t *testing.T) {
	a := newSlashTestAgent(t)
	res := a.slashGoal(webMsg(), strongArgs())
	// Successful create is intentionally silent — the streaming
	// continuation IS the user-visible acknowledgement, so the
	// slash reply stays empty to avoid a redundant confirmation
	// bubble in the chat.
	if !res.handled || res.reply != "" {
		t.Fatalf("expected silent success; got handled=%v reply=%q", res.handled, res.reply)
	}

	// Verify the row landed with the right routing tuple.
	key := a.resolveSessionKey(webMsg())
	g, _ := a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	if g == nil {
		t.Fatal("goal not persisted")
	}
	if g.Objective != strongObjective {
		t.Errorf("objective = %q, want %q", g.Objective, strongObjective)
	}
	if g.Channel != "web" || g.ChatID != "chat-1" {
		t.Errorf("routing not stamped: channel=%q chat=%q", g.Channel, g.ChatID)
	}
	if g.Status != goal.StatusActive {
		t.Errorf("status = %q, want active", g.Status)
	}
	// The slash handler should have spun a runtime and triggered it.
	if a.goalManager.Get(key) == nil {
		t.Error("expected GoalRuntime to be running after create")
	}
}

func TestSlashGoalCreateWithoutObjectiveFails(t *testing.T) {
	a := newSlashTestAgent(t)
	// `/goal pause` with no goal — that's not a create; it dispatches
	// to slashGoalPause. To get the "objective required" path, the
	// first arg has to be something other than pause/resume/clear/"".
	// But that's harder to trigger via slashGoal. The actual "no
	// objective" path is slashGoalCreate("") — exercise it directly.
	res := a.slashGoalCreate(webMsg(), "   ")
	if !strings.Contains(res.reply, "Usage:") {
		t.Errorf("expected usage hint on blank objective; got %s", res.reply)
	}
}

func TestSlashGoalCreateRejectsDuplicate(t *testing.T) {
	a := newSlashTestAgent(t)
	if r := a.slashGoal(webMsg(), strongArgs()); !r.handled {
		t.Fatalf("seed not handled: %s", r.reply)
	}
	res := a.slashGoal(webMsg(), strongArgs())
	if !strings.Contains(res.reply, "already exists") {
		t.Errorf("expected duplicate-rejection message, got %s", res.reply)
	}
}

func TestSlashGoalPauseResumeCycle(t *testing.T) {
	a := newSlashTestAgent(t)
	a.slashGoal(webMsg(), strongArgs())

	if r := a.slashGoal(webMsg(), []string{"pause"}); !strings.Contains(r.reply, "⏸") {
		t.Fatalf("pause: %s", r.reply)
	}
	key := a.resolveSessionKey(webMsg())
	g, _ := a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	if g.Status != goal.StatusPaused {
		t.Errorf("status after pause = %q, want paused", g.Status)
	}

	// Resume success is silent (mirroring create) — assert handled
	// + empty reply rather than looking for a confirmation glyph.
	if r := a.slashGoal(webMsg(), []string{"resume"}); !r.handled || r.reply != "" {
		t.Fatalf("resume: handled=%v reply=%q", r.handled, r.reply)
	}
	g, _ = a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	if g.Status != goal.StatusActive {
		t.Errorf("status after resume = %q, want active", g.Status)
	}
}

func TestSlashGoalPauseRejectsWrongState(t *testing.T) {
	a := newSlashTestAgent(t)
	a.slashGoal(webMsg(), strongArgs())
	a.slashGoal(webMsg(), []string{"pause"})

	// Already paused — second pause must fail loudly so the user
	// knows nothing changed.
	res := a.slashGoal(webMsg(), []string{"pause"})
	if !strings.Contains(res.reply, "Not active") {
		t.Errorf("expected wrong-state hint; got %s", res.reply)
	}
}

func TestSlashGoalClearRemovesRow(t *testing.T) {
	a := newSlashTestAgent(t)
	a.slashGoal(webMsg(), strongArgs())
	key := a.resolveSessionKey(webMsg())

	res := a.slashGoal(webMsg(), []string{"clear"})
	if !strings.Contains(res.reply, "🗑") {
		t.Errorf("expected clear banner; got %s", res.reply)
	}
	if g, _ := a.goalStore.GetGoalBySession(context.Background(), a.name, key); g != nil {
		t.Error("goal still present after clear")
	}
	// Runtime should be stopped so a future /goal mints a fresh one.
	if a.goalManager.Get(key) != nil {
		t.Error("runtime still alive after clear; should have been stopped")
	}
}

func TestSlashGoalShowFormatsActive(t *testing.T) {
	a := newSlashTestAgent(t)
	a.slashGoal(webMsg(), strongArgs())
	res := a.slashGoal(webMsg(), nil)
	if !strings.Contains(res.reply, "active") {
		t.Errorf("missing status:\n%s", res.reply)
	}
	if !strings.Contains(res.reply, strongObjective) {
		t.Errorf("missing objective:\n%s", res.reply)
	}
}

// TestSlashGoalDisabledWithoutStore: the slash dispatch must
// degrade cleanly when the agent was built without a data store
// (legacy single-user installs). No panic, friendly message.
func TestSlashGoalDisabledWithoutStore(t *testing.T) {
	a := &Agent{
		name:     "agent-test",
		registry: tools.NewRegistry("", ""),
		hooks:    NewHookRegistry(),
		sessions: session.NewManager(t.TempDir()),
	}
	res := a.slashGoal(webMsg(), []string{"x"})
	if !strings.Contains(res.reply, "isn't enabled") {
		t.Errorf("expected feature-off message; got %s", res.reply)
	}
}

// TestPlanModeGatesTrigger: when the turn ran in plan-mode, the
// trigger hook must NOT spin a continuation. Otherwise plan-mode's
// whole "let the user review before more work" promise breaks.
func TestPlanModeGatesTrigger(t *testing.T) {
	a := newSlashTestAgent(t)
	// Seed an active goal so the slow-path Ensure check would
	// otherwise succeed.
	a.slashGoal(webMsg(), strongArgs())
	key := a.resolveSessionKey(webMsg())

	// Drain the runtime spawned by slashGoalCreate so we start clean.
	a.goalManager.StopSession(key)

	hook := a.goalTriggerHook(allowedContinuationSources)
	hook(context.Background(), &HookContext{
		Source:         bus.SourceUser,
		GoalSessionKey: key,
		IsPlanMode:     true,
	})
	if a.goalManager.Get(key) != nil {
		t.Error("plan-mode turn spun up a runtime; trigger gate didn't fire")
	}
}

// TestTriggerLazyEnsureSkipsSessionsWithoutGoal pins the goroutine-
// leak fix: a turn on a session with no goal must NOT create a
// runtime. Without the fix, every chat session that ever ran a turn
// would get its own idle goroutine for up to 30 min.
func TestTriggerLazyEnsureSkipsSessionsWithoutGoal(t *testing.T) {
	a := newSlashTestAgent(t)
	hook := a.goalTriggerHook(allowedContinuationSources)
	hook(context.Background(), &HookContext{
		Source:         bus.SourceUser,
		GoalSessionKey: "s-no-goal-here",
	})
	if a.goalManager.ActiveCount() != 0 {
		t.Errorf("triggers on session without a goal should not spin runtimes; ActiveCount=%d",
			a.goalManager.ActiveCount())
	}
}

// TestSlashGoalCreateLandsActiveAndTriggers: /goal foo creates the
// goal Active (no weak-objective gate) and immediately wakes the
// runtime so the first continuation publishes off the user's own
// slash turn. Matches Codex's slash-only UX — "type /goal and it
// just starts".
func TestSlashGoalCreateLandsActiveAndTriggers(t *testing.T) {
	a := newSlashTestAgent(t)
	// Deliberately short + no verify keyword — would have been
	// auto-paused under the old heuristic. New contract: starts.
	res := a.slashGoal(webMsg(), []string{"fix", "the", "slow", "dashboard"})

	if strings.Contains(res.reply, "paused") {
		t.Errorf("create should not auto-pause any objective; got %s", res.reply)
	}
	key := a.resolveSessionKey(webMsg())
	g, _ := a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	if g == nil || g.Status != goal.StatusActive {
		t.Fatalf("status = %v, want active", g)
	}
	if a.goalManager.Get(key) == nil {
		t.Error("create should spin a runtime so the first continuation fires immediately")
	}
}

// TestNewSessionClearsGoal: /new on a session that has a goal must
// delete the goal row so the fresh session starts clean. Otherwise
// a stale goal would re-arm itself on the user's next message in
// the new conversation (different sessionKey, but same chat from the
// channel adapter's POV).
func TestNewSessionClearsGoal(t *testing.T) {
	a := newSlashTestAgent(t)
	a.slashGoal(webMsg(), []string{"obj"})
	oldKey := a.resolveSessionKey(webMsg())

	a.clearGoalForSession(oldKey)
	if g, _ := a.goalStore.GetGoalBySession(context.Background(), a.name, oldKey); g != nil {
		t.Errorf("/new should have cleared goal for session %q", oldKey)
	}
	if a.goalManager.Get(oldKey) != nil {
		t.Error("runtime for old session should be stopped on /new")
	}
}
