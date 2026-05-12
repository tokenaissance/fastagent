package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
)

// memGoalStore is the in-memory goal.Store implementation the tool
// tests use. Keyed by (agentID, sessionKey) to mirror the production
// UNIQUE index and exercise the same conflict path.
type memGoalStore struct {
	mu   sync.Mutex
	rows map[string]*goal.Goal // key = agentID + "|" + sessionKey
}

func newMemGoalStore() *memGoalStore {
	return &memGoalStore{rows: map[string]*goal.Goal{}}
}

func (m *memGoalStore) key(agent, sess string) string { return agent + "|" + sess }

func (m *memGoalStore) CreateGoal(_ context.Context, g *goal.Goal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(g.AgentID, g.SessionKey)
	if _, exists := m.rows[k]; exists {
		return goal.ErrAlreadyExists
	}
	clone := *g
	m.rows[k] = &clone
	return nil
}
func (m *memGoalStore) GetGoalBySession(_ context.Context, agentID, sessionKey string) (*goal.Goal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.rows[m.key(agentID, sessionKey)]
	if !ok {
		return nil, goal.ErrNotFound
	}
	clone := *g
	return &clone, nil
}
func (m *memGoalStore) GetGoalByID(context.Context, string) (*goal.Goal, error) {
	return nil, goal.ErrNotFound
}
func (m *memGoalStore) UpdateGoal(_ context.Context, g *goal.Goal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(g.AgentID, g.SessionKey)
	if _, ok := m.rows[k]; !ok {
		return goal.ErrNotFound
	}
	clone := *g
	m.rows[k] = &clone
	return nil
}
func (m *memGoalStore) UpdateObjective(context.Context, string, string) error { return nil }
func (m *memGoalStore) DeleteGoal(context.Context, string) error              { return nil }
func (m *memGoalStore) ListGoalsByOwner(context.Context, string, int) ([]*goal.Goal, error) {
	return nil, nil
}

// fixture builds a Registry pre-bound to a session and a memGoalStore,
// with the three goal tools registered. The (agentID, ownerUserID,
// sessionKey) values are the ones every test exercises against.
func fixture(t *testing.T) (*Registry, *memGoalStore) {
	t.Helper()
	r := NewRegistry("", "")
	r.SetGoalSessionKey("s-fixture-1")
	st := newMemGoalStore()
	RegisterGoalTools(r, st, "agent-A", "user-1")
	return r, st
}

// callTool runs the named tool with the given args JSON and returns
// (result, err). Centralizes the registry-lookup boilerplate.
func callTool(t *testing.T, r *Registry, name, argsJSON string) (string, error) {
	t.Helper()
	fn := r.GetFunc(name)
	if fn == nil {
		t.Fatalf("tool %q not registered", name)
	}
	return fn(context.Background(), json.RawMessage(argsJSON))
}

// TestGetGoalReturnsNoGoalEnvelope: get_goal must NOT fail when no
// goal is set. Returning {"status": "no_goal"} lets the model probe
// safely without a tool error that could derail the turn.
func TestGetGoalReturnsNoGoalEnvelope(t *testing.T) {
	r, _ := fixture(t)
	out, err := callTool(t, r, "get_goal", `{}`)
	if err != nil {
		t.Fatalf("get_goal: %v", err)
	}
	if !strings.Contains(out, `"no_goal"`) {
		t.Errorf("expected no_goal envelope, got %s", out)
	}
}

// TestCreateGoalHappyPath: a fresh session can create a goal; status
// defaults to active; the session_key + agent_id come from registry
// state (the model can't forge them via args).
func TestCreateGoalHappyPath(t *testing.T) {
	r, st := fixture(t)
	out, err := callTool(t, r, "create_goal",
		`{"objective":"translate README","token_budget":200000}`)
	if err != nil {
		t.Fatalf("create_goal: %v", err)
	}
	if !strings.Contains(out, `"active"`) {
		t.Errorf("expected status active in response, got %s", out)
	}
	g, _ := st.GetGoalBySession(context.Background(), "agent-A", "s-fixture-1")
	if g == nil {
		t.Fatal("goal not persisted")
	}
	if g.OwnerUserID != "user-1" {
		t.Errorf("OwnerUserID = %q, want user-1 (must come from registry, not args)", g.OwnerUserID)
	}
	if g.TokenBudget == nil || *g.TokenBudget != 200000 {
		t.Errorf("TokenBudget round-trip mismatch: %v", g.TokenBudget)
	}
}

// TestCreateGoalRejectsDuplicate: the UNIQUE constraint failure
// must surface as a model-recoverable error, not a 500 — the model
// should be able to recover by asking the user to /goal clear first.
func TestCreateGoalRejectsDuplicate(t *testing.T) {
	r, _ := fixture(t)
	if _, err := callTool(t, r, "create_goal", `{"objective":"first"}`); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := callTool(t, r, "create_goal", `{"objective":"second"}`)
	if err == nil {
		t.Fatal("second create should have failed (a goal already exists)")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention duplicate; got %v", err)
	}
}

// TestCreateGoalNoSessionContext: the model can't create a goal
// outside a chat turn — guards against an agent (e.g. boot-time hook
// or webhook handler) accidentally minting goals where no session
// has been bound.
func TestCreateGoalNoSessionContext(t *testing.T) {
	r := NewRegistry("", "")
	// Deliberately skip SetGoalSessionKey.
	st := newMemGoalStore()
	RegisterGoalTools(r, st, "agent-A", "user-1")
	_, err := callTool(t, r, "create_goal", `{"objective":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "no active session") {
		t.Fatalf("expected no-session error, got %v", err)
	}
}

// TestCreateGoalEmptyObjectiveRejected: empty / whitespace-only
// objectives are rejected up front rather than landing as a blank
// row in the store.
func TestCreateGoalEmptyObjectiveRejected(t *testing.T) {
	r, _ := fixture(t)
	if _, err := callTool(t, r, "create_goal", `{"objective":""}`); err == nil {
		t.Error("empty objective should be rejected")
	}
	if _, err := callTool(t, r, "create_goal", `{"objective":"   "}`); err == nil {
		t.Error("whitespace objective should be rejected")
	}
}

// TestCreateGoalRejectsNonPositiveBudget: a budget of 0 or negative
// would either run forever or never start; reject it at the door.
func TestCreateGoalRejectsNonPositiveBudget(t *testing.T) {
	r, _ := fixture(t)
	if _, err := callTool(t, r, "create_goal", `{"objective":"x","token_budget":0}`); err == nil {
		t.Error("zero budget should be rejected")
	}
	if _, err := callTool(t, r, "create_goal", `{"objective":"x","token_budget":-5}`); err == nil {
		t.Error("negative budget should be rejected")
	}
}

// TestUpdateGoalCompleteFlipsStatus: the happy-path completion flips
// status from active → complete and returns the final token tally.
// Mirrors what the model would do after audit succeeds.
func TestUpdateGoalCompleteFlipsStatus(t *testing.T) {
	r, st := fixture(t)
	if _, err := callTool(t, r, "create_goal", `{"objective":"do it"}`); err != nil {
		t.Fatalf("seed create: %v", err)
	}
	// Simulate some token accounting happened (step 2b will do this
	// for real via AfterModelCall; here we hand-set so the response
	// can be asserted).
	g, _ := st.GetGoalBySession(context.Background(), "agent-A", "s-fixture-1")
	g.TokensUsed = 12345
	_ = st.UpdateGoal(context.Background(), g)

	out, err := callTool(t, r, "update_goal", `{"status":"complete"}`)
	if err != nil {
		t.Fatalf("update_goal: %v", err)
	}
	if !strings.Contains(out, `"final_token_usage":12345`) {
		t.Errorf("expected final_token_usage in response, got %s", out)
	}
	final, _ := st.GetGoalBySession(context.Background(), "agent-A", "s-fixture-1")
	if final.Status != goal.StatusComplete {
		t.Errorf("status = %q, want complete", final.Status)
	}
}

// TestUpdateGoalRejectsBadStatus is the load-bearing test of the
// "model can only mark complete" contract. Anything else, especially
// pause / budget_limited / unmet, must error out.
func TestUpdateGoalRejectsBadStatus(t *testing.T) {
	r, _ := fixture(t)
	if _, err := callTool(t, r, "create_goal", `{"objective":"x"}`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, bad := range []string{`"pause"`, `"paused"`, `"budget_limited"`, `"unmet"`, `"active"`, `""`} {
		args := `{"status":` + bad + `}`
		if _, err := callTool(t, r, "update_goal", args); err == nil {
			t.Errorf("update_goal accepted forbidden status %s", bad)
		}
	}
}

// TestUpdateGoalNoActiveGoal: calling update_goal without a goal in
// place is a user error the model should surface — not a panic and
// not a silent success.
func TestUpdateGoalNoActiveGoal(t *testing.T) {
	r, _ := fixture(t)
	_, err := callTool(t, r, "update_goal", `{"status":"complete"}`)
	if err == nil {
		t.Fatal("expected error when no goal exists")
	}
}

// TestUpdateGoalOnlyTransitionsFromActive: a goal that's already
// complete / paused / budget_limited can't be re-completed by the
// model. Runtime / user are the only writers of those states.
func TestUpdateGoalOnlyTransitionsFromActive(t *testing.T) {
	r, st := fixture(t)
	if _, err := callTool(t, r, "create_goal", `{"objective":"x"}`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, blocked := range []goal.Status{goal.StatusPaused, goal.StatusBudgetLimited, goal.StatusComplete} {
		g, _ := st.GetGoalBySession(context.Background(), "agent-A", "s-fixture-1")
		g.Status = blocked
		_ = st.UpdateGoal(context.Background(), g)

		_, err := callTool(t, r, "update_goal", `{"status":"complete"}`)
		if err == nil {
			t.Errorf("update_goal should reject from status %q", blocked)
		}
	}
}

// TestGoalToolsRegistered confirms the 3 expected names show up on
// the registry (and the function is non-nil). Cheap guard against
// renaming one and forgetting to update the registration call.
func TestGoalToolsRegistered(t *testing.T) {
	r, _ := fixture(t)
	for _, name := range []string{"get_goal", "create_goal", "update_goal"} {
		if r.GetFunc(name) == nil {
			t.Errorf("tool %q not registered", name)
		}
	}
}

// TestUpdateGoalErrorIsErrorType: sanity that errors come back as
// proper Go errors (not encoded into the result string). Code that
// gates on err != nil — most of the tool execution path — relies on
// this.
func TestUpdateGoalErrorIsErrorType(t *testing.T) {
	r, _ := fixture(t)
	_, err := callTool(t, r, "update_goal", `{"status":"complete"}`)
	if !errors.Is(err, err) || err == nil {
		t.Fatal("expected non-nil error")
	}
}
