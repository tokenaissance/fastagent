package agent

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// newAgentForWireTest builds the minimal Agent skeleton the wiring
// tests need. We don't go through NewAgentWithSkillsCfg because that
// drags in the full config + workspace machinery; the wiring path
// only needs registry / hooks / messageBus / agentID / ownerUserID.
func newAgentForWireTest(t *testing.T) *Agent {
	t.Helper()
	return &Agent{
		name:        "agent-test",
		ownerUserID: "user-1",
		registry:    tools.NewRegistry("", ""),
		hooks:       NewHookRegistry(),
		messageBus:  bus.New(),
	}
}

// TestWireGoalsNilStoreIsNoOp pins the contract that a nil store
// silently turns the feature off. Legacy single-user installs hit
// this path; without it they'd panic on agent boot.
func TestWireGoalsNilStoreIsNoOp(t *testing.T) {
	a := newAgentForWireTest(t)
	a.WireGoals(nil)
	if a.GoalStore() != nil {
		t.Error("GoalStore should remain nil after WireGoals(nil)")
	}
	if a.GoalManager() != nil {
		t.Error("GoalManager should remain nil after WireGoals(nil)")
	}
	if a.registry.GetFunc("get_goal") != nil {
		t.Error("get_goal should not be registered when store is nil")
	}
}

// TestWireGoalsRegistersToolsAndHook is the happy-path smoke test:
// after WireGoals returns, the 3 goal tools must be on the registry
// and the AfterModelCall hook must be present.
func TestWireGoalsRegistersToolsAndHook(t *testing.T) {
	a := newAgentForWireTest(t)
	st := &memGoalStore{}
	a.WireGoals(st)
	t.Cleanup(func() {
		if a.goalManager != nil {
			a.goalManager.Shutdown()
		}
	})

	if a.GoalStore() != st {
		t.Errorf("GoalStore() returned %v, want %v", a.GoalStore(), st)
	}
	if a.GoalManager() == nil {
		t.Fatal("GoalManager() returned nil after WireGoals")
	}
	for _, name := range []string{"get_goal", "create_goal", "update_goal"} {
		if a.registry.GetFunc(name) == nil {
			t.Errorf("tool %q not registered", name)
		}
	}
	// One AfterModelCall registration came from WireGoals; LoggingHook
	// was already there, so we expect ≥ 1 — match >= rather than ==
	// to stay robust against the hook constructor adding extras later.
	if len(a.hooks.hooks[AfterModelCall]) < 1 {
		t.Errorf("AfterModelCall hook count = %d, want ≥1", len(a.hooks.hooks[AfterModelCall]))
	}
}

// TestWireGoalsIsIdempotent: WireGoals replaces an existing manager
// rather than leaking goroutines. Hot-reload paths or
// re-provisioning flows can call it twice safely.
func TestWireGoalsIsIdempotent(t *testing.T) {
	a := newAgentForWireTest(t)
	a.WireGoals(&memGoalStore{})
	first := a.GoalManager()
	a.WireGoals(&memGoalStore{})
	second := a.GoalManager()
	t.Cleanup(second.Shutdown)

	if first == second {
		t.Error("second WireGoals should produce a fresh GoalManager (the old one was shutdown)")
	}
	// First manager's runtimes must be reaped — verify via ActiveCount.
	// Add then remove a session to confirm the OLD manager won't
	// accept new work (it should be inactive after Shutdown).
	if gr := first.Ensure("s-1", "agent-test", "user-1"); gr != nil {
		t.Error("first manager should refuse Ensure after Shutdown (Start was reset)")
	}
}

// TestWireGoalsToolUsesOwnerAndAgentID confirms the registered tools
// pick up the agent's owner + name correctly, not whatever default
// the registry was constructed with. Without this, a multi-user
// install could mint goals against the wrong user_id.
func TestWireGoalsToolUsesOwnerAndAgentID(t *testing.T) {
	a := newAgentForWireTest(t)
	a.name = "agent-Z"
	a.ownerUserID = "user-Z"
	st := &memGoalStore{}
	a.WireGoals(st)
	t.Cleanup(a.goalManager.Shutdown)

	a.registry.SetGoalSessionKey("s-Z")
	if _, err := a.registry.GetFunc("create_goal")(t.Context(),
		[]byte(`{"objective":"x"}`)); err != nil {
		t.Fatalf("create_goal: %v", err)
	}
	g, _ := st.GetGoalBySession(t.Context(), "agent-Z", "s-Z")
	if g == nil {
		t.Fatal("goal not created under expected (agent-Z, s-Z)")
	}
	if g.OwnerUserID != "user-Z" {
		t.Errorf("OwnerUserID = %q, want user-Z", g.OwnerUserID)
	}
}

// TestGoalManagerLifecycleAfterWire: the manager returned by
// GoalManager() is in the Started state — Ensure must return a real
// runtime, not nil. Without this, a freshly wired agent would
// silently skip every Trigger call from the agent loop.
func TestGoalManagerLifecycleAfterWire(t *testing.T) {
	a := newAgentForWireTest(t)
	a.WireGoals(&memGoalStore{})
	t.Cleanup(a.goalManager.Shutdown)

	gr := a.GoalManager().Ensure("s-1", a.name, a.ownerUserID)
	if gr == nil {
		t.Fatal("Ensure returned nil — GoalManager wasn't Started by WireGoals")
	}
}

// staticGoalStore is a minimal goal.Store stub for the tests above
// that don't need the full per-session map (the tests already in
// goal_hook_test.go share the same name `memGoalStore` in this
// package — re-use that one). The compile-time check below catches
// drift if the goal.Store interface grows.
var _ goal.Store = (*memGoalStore)(nil)

// TestGoalTriggerHookGatesOnUserSource: the PostTurn trigger must
// fire only on genuine user turns. Otherwise a cron tick / sub-agent
// spawn / runtime-injected goal_continuation would each trigger
// another continuation and we'd loop forever.
func TestGoalTriggerHookGatesOnUserSource(t *testing.T) {
	a := newAgentForWireTest(t)
	a.WireGoals(&memGoalStore{})
	t.Cleanup(a.goalManager.Shutdown)

	hook := a.goalTriggerHook(true)
	// The trigger never directly creates work on the bus — it just
	// pokes GoalManager.Ensure(). The observable side effect we can
	// assert in a unit test is the ActiveCount on the manager: a
	// non-user source must NOT create a runtime.
	for _, src := range []string{
		bus.SourceCron, bus.SourceHeartbeat, bus.SourceSubAgent, bus.SourceGoalContinuation,
	} {
		hook(t.Context(), &HookContext{
			Source:         src,
			GoalSessionKey: "s-source-test-" + src,
		})
	}
	if got := a.goalManager.ActiveCount(); got != 0 {
		t.Errorf("non-user-source hooks created %d runtimes, want 0", got)
	}

	// And the converse: a genuine user turn does create one.
	hook(t.Context(), &HookContext{
		Source:         bus.SourceUser,
		GoalSessionKey: "s-user",
	})
	if got := a.goalManager.ActiveCount(); got != 1 {
		t.Errorf("user-source hook created %d runtimes, want 1", got)
	}
}

// TestGoalTriggerHookSkipsUpdateGoalTool: the AfterToolCall trigger
// has to skip update_goal — by the time that fires, the goal is
// Complete, so a continuation probe is wasted work. (Functionally
// harmless because maybeContinue would no-op on a non-Active goal,
// but the skip avoids ever spinning a runtime for a finished goal.)
func TestGoalTriggerHookSkipsUpdateGoalTool(t *testing.T) {
	a := newAgentForWireTest(t)
	a.WireGoals(&memGoalStore{})
	t.Cleanup(a.goalManager.Shutdown)

	hook := a.goalTriggerHook(false /* AfterToolCall doesn't gate on source */)
	hook(t.Context(), &HookContext{
		ToolName:       "update_goal",
		GoalSessionKey: "s-1",
	})
	if got := a.goalManager.ActiveCount(); got != 0 {
		t.Errorf("update_goal tool should not produce a trigger; got %d runtimes", got)
	}
}

// TestGoalTriggerHookNoOpWithoutSessionKey is the safety net for
// boot-time / out-of-chat callbacks: no GoalSessionKey → no work.
func TestGoalTriggerHookNoOpWithoutSessionKey(t *testing.T) {
	a := newAgentForWireTest(t)
	a.WireGoals(&memGoalStore{})
	t.Cleanup(a.goalManager.Shutdown)

	hook := a.goalTriggerHook(true)
	hook(t.Context(), &HookContext{Source: bus.SourceUser, GoalSessionKey: ""})
	if got := a.goalManager.ActiveCount(); got != 0 {
		t.Errorf("empty session key should not create a runtime; got %d", got)
	}
}
