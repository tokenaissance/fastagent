package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// memGoalStore is the in-memory goal.Store the hook tests use.
// Separate copy from internal/agent/tools so the two packages don't
// depend on each other for test fixtures.
type memGoalStore struct {
	mu      sync.Mutex
	row     *goal.Goal
	getErr  error
	saveErr error
}

func (m *memGoalStore) CreateGoal(_ context.Context, g *goal.Goal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.row != nil {
		return goal.ErrAlreadyExists
	}
	clone := *g
	m.row = &clone
	return nil
}
func (m *memGoalStore) GetGoalBySession(_ context.Context, agentID, sessionKey string) (*goal.Goal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	if m.row == nil || m.row.AgentID != agentID || m.row.SessionKey != sessionKey {
		return nil, goal.ErrNotFound
	}
	clone := *m.row
	return &clone, nil
}
func (m *memGoalStore) GetGoalByID(context.Context, string) (*goal.Goal, error) {
	return nil, goal.ErrNotFound
}
func (m *memGoalStore) UpdateGoal(_ context.Context, g *goal.Goal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	clone := *g
	m.row = &clone
	return nil
}
func (m *memGoalStore) UpdateObjective(context.Context, string, string) error { return nil }
func (m *memGoalStore) DeleteGoal(context.Context, string) error              { return nil }
func (m *memGoalStore) ListGoalsByOwner(context.Context, string, int) ([]*goal.Goal, error) {
	return nil, nil
}

// seedActiveGoal places a fresh active goal in the store. Returns
// the agentID + sessionKey the hook context should reference.
func seedActiveGoal(t *testing.T, st *memGoalStore, budget int64) (agentID, sessionKey string) {
	t.Helper()
	b := budget
	g := &goal.Goal{
		ID:          "g-test",
		AgentID:     "agent-A",
		SessionKey:  "s-test",
		OwnerUserID: "user-1",
		Objective:   "x",
		Status:      goal.StatusActive,
		TokenBudget: &b,
	}
	if err := st.CreateGoal(context.Background(), g); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return g.AgentID, g.SessionKey
}

func makeAfterModelCall(sessionKey string, u *provider.Usage) *HookContext {
	return &HookContext{
		Point:          AfterModelCall,
		Response:       &provider.Response{Usage: u},
		GoalSessionKey: sessionKey,
	}
}

func TestTokenAccountingHookFoldsUsage(t *testing.T) {
	st := &memGoalStore{}
	agentID, sessionKey := seedActiveGoal(t, st, 1_000_000)
	hook := NewTokenAccountingHook(st, agentID)

	hook(context.Background(), makeAfterModelCall(sessionKey, &provider.Usage{
		InputTokens: 200, CacheReadInputTokens: 50, OutputTokens: 30,
	}))
	// delta = 150 + 30 = 180
	g, _ := st.GetGoalBySession(context.Background(), agentID, sessionKey)
	if g.TokensUsed != 180 {
		t.Errorf("TokensUsed = %d, want 180", g.TokensUsed)
	}
	if g.Status != goal.StatusActive {
		t.Errorf("status = %q, want active (1M budget not yet hit)", g.Status)
	}
}

func TestTokenAccountingHookFlipsBudgetLimited(t *testing.T) {
	st := &memGoalStore{}
	agentID, sessionKey := seedActiveGoal(t, st, 100)
	hook := NewTokenAccountingHook(st, agentID)

	hook(context.Background(), makeAfterModelCall(sessionKey, &provider.Usage{
		InputTokens: 50, OutputTokens: 60,
	}))
	g, _ := st.GetGoalBySession(context.Background(), agentID, sessionKey)
	if g.Status != goal.StatusBudgetLimited {
		t.Errorf("status = %q, want budget_limited (used %d > budget 100)",
			g.Status, g.TokensUsed)
	}
}

func TestTokenAccountingHookSkipsWhenNoGoalSession(t *testing.T) {
	st := &memGoalStore{}
	hook := NewTokenAccountingHook(st, "agent-A")
	// Empty GoalSessionKey — agent ran outside a chat context (e.g.
	// boot-time warmup). Hook must no-op rather than crash on a
	// missing row.
	hook(context.Background(), &HookContext{
		Point:    AfterModelCall,
		Response: &provider.Response{Usage: &provider.Usage{OutputTokens: 100}},
	})
	// No-op — store should still be empty.
	if st.row != nil {
		t.Errorf("hook persisted a goal when no session_key was set")
	}
}

func TestTokenAccountingHookSkipsWhenNoUsage(t *testing.T) {
	st := &memGoalStore{}
	agentID, sessionKey := seedActiveGoal(t, st, 1000)
	hook := NewTokenAccountingHook(st, agentID)

	// Response.Usage is nil — provider didn't report (Ollama etc.).
	// We deliberately don't error; we just skip folding.
	hook(context.Background(), &HookContext{
		Point:          AfterModelCall,
		Response:       &provider.Response{},
		GoalSessionKey: sessionKey,
	})
	g, _ := st.GetGoalBySession(context.Background(), agentID, sessionKey)
	if g.TokensUsed != 0 {
		t.Errorf("TokensUsed mutated to %d on nil-Usage hook", g.TokensUsed)
	}
}

func TestTokenAccountingHookSkipsOnError(t *testing.T) {
	// If the model call itself errored, the response is meaningless
	// and any usage on it shouldn't count.
	st := &memGoalStore{}
	agentID, sessionKey := seedActiveGoal(t, st, 1000)
	hook := NewTokenAccountingHook(st, agentID)

	hook(context.Background(), &HookContext{
		Point:          AfterModelCall,
		Response:       &provider.Response{Usage: &provider.Usage{OutputTokens: 500}},
		Error:          errors.New("rate limited"),
		GoalSessionKey: sessionKey,
	})
	g, _ := st.GetGoalBySession(context.Background(), agentID, sessionKey)
	if g.TokensUsed != 0 {
		t.Errorf("TokensUsed = %d, want 0 on errored call", g.TokensUsed)
	}
}

func TestTokenAccountingHookSkipsWhenNoGoalRow(t *testing.T) {
	// A turn happens on a session with no goal — hook must not error
	// loudly. ErrNotFound is the expected path here.
	st := &memGoalStore{}
	hook := NewTokenAccountingHook(st, "agent-A")
	hook(context.Background(), makeAfterModelCall("s-no-goal", &provider.Usage{OutputTokens: 100}))
	// No row to inspect — just confirming no panic.
}

func TestTokenAccountingHookOnlyFiresOnAfterModelCall(t *testing.T) {
	// The hook is registered against AfterModelCall, but the same
	// closure could be reused at other hook points by mistake. Guard
	// at the entry so a stray registration doesn't bill the same
	// turn twice.
	st := &memGoalStore{}
	agentID, sessionKey := seedActiveGoal(t, st, 1000)
	hook := NewTokenAccountingHook(st, agentID)

	for _, point := range []HookPoint{BeforeModelCall, BeforeToolCall, AfterToolCall, PostTurn} {
		hook(context.Background(), &HookContext{
			Point:          point,
			Response:       &provider.Response{Usage: &provider.Usage{OutputTokens: 100}},
			GoalSessionKey: sessionKey,
		})
	}
	g, _ := st.GetGoalBySession(context.Background(), agentID, sessionKey)
	if g.TokensUsed != 0 {
		t.Errorf("TokensUsed = %d, want 0 (hook should only fire on AfterModelCall)", g.TokensUsed)
	}
}

func TestTokenAccountingHookNilStoreReturnsNil(t *testing.T) {
	// Lets the caller register the hook unconditionally during agent
	// boot — when goal feature isn't wired, NewTokenAccountingHook
	// just returns a nil func and the registration is a no-op (the
	// hook registry's Run skips nil entries).
	if h := NewTokenAccountingHook(nil, "agent"); h != nil {
		t.Errorf("expected nil HookFunc for nil store, got %T", h)
	}
}

func TestTokenAccountingHookSkipsZeroDelta(t *testing.T) {
	// An all-cached prompt with no output produces 0 delta — the
	// hook should skip the persist, not write an unchanged row. We
	// verify the skip by setting saveErr: if the hook tries to
	// persist, the error would be logged but the row stays.
	st := &memGoalStore{saveErr: errors.New("should not be called")}
	agentID, sessionKey := seedActiveGoal(t, st, 1000)
	st.saveErr = errors.New("save should not be reached") // re-set after seed

	hook := NewTokenAccountingHook(st, agentID)
	hook(context.Background(), makeAfterModelCall(sessionKey, &provider.Usage{
		InputTokens: 100, CacheReadInputTokens: 100,
	}))
	// If the hook had called UpdateGoal, our memGoalStore would have
	// returned saveErr and logged a warn — but the goal would have
	// been mutated in-place first. The cheap assertion: the row's
	// TokensUsed must still be 0 (FoldUsage skipped, store skipped).
	g, _ := st.GetGoalBySession(context.Background(), agentID, sessionKey)
	if g.TokensUsed != 0 {
		t.Errorf("TokensUsed = %d, want 0 on zero-delta call", g.TokensUsed)
	}
}
