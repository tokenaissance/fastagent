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

// strongObjective is the canonical "well-shaped" objective shared
// by every test that needs the active path. It contains a verify
// keyword + enough body to skip the §5.7 scaffold heuristic
// (objectiveLooksWeak), so create lands the goal Active. Kept here
// as a single string so the heuristic and the tests can't drift.
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
	if !strings.Contains(res.reply, "🎯 Goal set") {
		t.Fatalf("expected success banner; got %s", res.reply)
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
	if r := a.slashGoal(webMsg(), strongArgs()); !strings.Contains(r.reply, "🎯") {
		t.Fatalf("seed: %s", r.reply)
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

	if r := a.slashGoal(webMsg(), []string{"resume"}); !strings.Contains(r.reply, "▶") {
		t.Fatalf("resume: %s", r.reply)
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
	if !strings.Contains(res.reply, "not active") {
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
	if !strings.Contains(res.reply, "Status:      active") {
		t.Errorf("missing status line:\n%s", res.reply)
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

	hook := a.goalTriggerHook(true /* PostTurn-style: gated on source */)
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
	hook := a.goalTriggerHook(true)
	hook(context.Background(), &HookContext{
		Source:         bus.SourceUser,
		GoalSessionKey: "s-no-goal-here",
	})
	if a.goalManager.ActiveCount() != 0 {
		t.Errorf("triggers on session without a goal should not spin runtimes; ActiveCount=%d",
			a.goalManager.ActiveCount())
	}
}

// TestObjectiveLooksWeak pins the scaffold heuristic — design § 5.7's
// bad example must trip the warning, the good example must not.
// The heuristic exists precisely so users don't waste budget on
// ill-formed objectives; if either of these flips, the UX
// regresses.
func TestObjectiveLooksWeak(t *testing.T) {
	weak := []string{
		"fix the slow dashboard",  // §5.7 bad example
		"do the thing",            // short + no verify
		"",                        // empty
		"translate the README.md", // longer but no verify path
	}
	for _, o := range weak {
		if !objectiveLooksWeak(o) {
			t.Errorf("expected weak: %q", o)
		}
	}
	strong := []string{
		// §5.7 good example
		"Reduce p95 render time of src/pages/dashboard.tsx to under 500ms; run scripts/perf-dashboard.ts to verify; do not memoize in src/lib/",
		// Chinese — verify keyword in Chinese trips a CJK alternative
		"把 README 翻译成英文放到 /tmp/readme.en.md，用 wc -l 验证行数一致，不要碰其他文件",
		// English with explicit verify
		"Migrate /internal/auth from JWT to OAuth2; existing tests in auth_test.go must pass",
	}
	for _, o := range strong {
		if objectiveLooksWeak(o) {
			t.Errorf("expected strong: %q", o)
		}
	}
}

// TestSlashGoalCreateWeakObjectiveAutoPause: a weak objective lands
// the goal in Paused, the reply nudges the user toward verification.
// Without auto-pause, agents would burn budget on under-specified
// goals before the user notices the problem.
func TestSlashGoalCreateWeakObjectiveAutoPause(t *testing.T) {
	a := newSlashTestAgent(t)
	res := a.slashGoal(webMsg(), []string{"fix", "the", "slow", "dashboard"})

	if !strings.Contains(res.reply, "paused for review") {
		t.Errorf("expected paused-for-review banner; got %s", res.reply)
	}
	if !strings.Contains(res.reply, "under-specified") {
		t.Errorf("expected scaffold hint; got %s", res.reply)
	}

	key := a.resolveSessionKey(webMsg())
	g, _ := a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	if g.Status != goal.StatusPaused {
		t.Errorf("status = %q, want paused (weak objective scaffold)", g.Status)
	}
	// Crucially: no runtime spun up — the user hasn't approved yet,
	// continuation would have been wasteful work.
	if a.goalManager.Get(key) != nil {
		t.Error("weak-objective create should NOT have started a runtime")
	}
}

// TestSlashGoalCreateStrongObjectiveSkipsScaffold: the §5.7 good
// example must bypass the scaffold, land Active, and trigger the
// runtime — otherwise well-formed goals would suffer a needless
// /goal resume step.
func TestSlashGoalCreateStrongObjectiveSkipsScaffold(t *testing.T) {
	a := newSlashTestAgent(t)
	strong := strings.Fields(
		"Reduce p95 render time of src/pages/dashboard.tsx to under 500ms; " +
			"run scripts/perf-dashboard.ts to verify; do not memoize in src/lib/")
	res := a.slashGoal(webMsg(), strong)

	if strings.Contains(res.reply, "paused for review") {
		t.Errorf("strong objective should not trigger scaffold; got %s", res.reply)
	}
	key := a.resolveSessionKey(webMsg())
	g, _ := a.goalStore.GetGoalBySession(context.Background(), a.name, key)
	if g.Status != goal.StatusActive {
		t.Errorf("status = %q, want active", g.Status)
	}
	if a.goalManager.Get(key) == nil {
		t.Error("strong-objective create should have spun a runtime")
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
