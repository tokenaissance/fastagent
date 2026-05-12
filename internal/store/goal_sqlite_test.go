package store

import (
	"context"
	"errors"
	"testing"
)

func TestGoalSQLiteRoundTrip(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	ctx := context.Background()
	budget := int64(200_000)

	g := &GoalRecord{
		ID:                      "goal-1",
		AgentID:                 "agent-A",
		SessionKey:              "s-12345-abcdef",
		OwnerUserID:             "user-1",
		Objective:               "translate README to English",
		Status:                  "active",
		TokenBudget:             &budget,
		LastAccountedTokenUsage: []byte(`{"InputTokens":0,"OutputTokens":0}`),
	}

	if err := db.CreateGoal(ctx, g); err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.SafetyMaxIterations != 100 {
		t.Errorf("CreateGoal should default SafetyMaxIterations to 100, got %d", g.SafetyMaxIterations)
	}

	// UNIQUE (agent_id, session_key) must reject a second goal on the
	// same pair — this is the contract slash and REST handlers rely on
	// to give users a clean "clear the existing goal first" error.
	dup := *g
	dup.ID = "goal-1-dup"
	if err := db.CreateGoal(ctx, &dup); !errors.Is(err, ErrGoalAlreadyExists) {
		t.Fatalf("expected ErrGoalAlreadyExists, got %v", err)
	}

	got, err := db.GetGoalBySession(ctx, "agent-A", "s-12345-abcdef")
	if err != nil {
		t.Fatalf("get by session: %v", err)
	}
	if got.Objective != "translate README to English" {
		t.Errorf("objective round-trip mismatch: %q", got.Objective)
	}
	if got.TokenBudget == nil || *got.TokenBudget != 200_000 {
		t.Errorf("token budget round-trip mismatch: %v", got.TokenBudget)
	}

	// Update via UpdateGoal — exercises the "tokens accumulated, status
	// flipped" path that GoalRuntime will use every continuation.
	got.TokensUsed = 50_000
	got.Status = "budget_limited"
	got.Iterations = 5
	if err := db.UpdateGoal(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := db.GetGoalByID(ctx, got.ID)
	if again.TokensUsed != 50_000 || again.Status != "budget_limited" || again.Iterations != 5 {
		t.Errorf("update didn't round-trip: %+v", again)
	}

	// UpdateGoalObjective is the path /goal <new-text> uses; the runtime
	// follows it with an ObjectiveUpdatedPrompt injection.
	if err := db.UpdateGoalObjective(ctx, got.ID, "translate to Japanese instead"); err != nil {
		t.Fatalf("update objective: %v", err)
	}
	again, _ = db.GetGoalByID(ctx, got.ID)
	if again.Objective != "translate to Japanese instead" {
		t.Errorf("objective rewrite didn't stick: %q", again.Objective)
	}

	// ListGoalsByOwner powers a future "all my goals" panel; even with
	// one goal it should return that one.
	list, err := db.ListGoalsByOwner(ctx, "user-1", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 goal in list, got %d", len(list))
	}

	if err := db.DeleteGoal(ctx, got.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetGoalByID(ctx, got.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestGoalUnboundedBudget(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	g := &GoalRecord{
		ID:          "goal-2",
		AgentID:     "agent-B",
		SessionKey:  "s-no-budget",
		OwnerUserID: "user-1",
		Objective:   "open-ended exploration",
		Status:      "active",
		// TokenBudget intentionally nil — exercises the nullable column path
	}
	if err := db.CreateGoal(ctx, g); err != nil {
		t.Fatalf("create unbounded: %v", err)
	}
	got, err := db.GetGoalBySession(ctx, "agent-B", "s-no-budget")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TokenBudget != nil {
		t.Errorf("expected nil TokenBudget, got %v", got.TokenBudget)
	}
}
