package goal

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// StoreAdapter wraps a store.Store and satisfies the narrow Store
// interface this package exposes. It owns the GoalRecord ↔ Goal
// conversion so callers (GoalRuntime, slash handlers, REST API) only
// see the domain type.
type StoreAdapter struct {
	st store.Store
}

// NewStoreAdapter binds an existing store.Store as a goal.Store.
func NewStoreAdapter(st store.Store) *StoreAdapter {
	return &StoreAdapter{st: st}
}

func (a *StoreAdapter) CreateGoal(ctx context.Context, g *Goal) error {
	rec, err := toRecord(g)
	if err != nil {
		return err
	}
	if err := a.st.CreateGoal(ctx, rec); err != nil {
		if errors.Is(err, store.ErrGoalAlreadyExists) {
			return ErrAlreadyExists
		}
		return err
	}
	*g = *fromRecord(rec)
	return nil
}

func (a *StoreAdapter) GetGoalBySession(ctx context.Context, agentID, sessionKey string) (*Goal, error) {
	rec, err := a.st.GetGoalBySession(ctx, agentID, sessionKey)
	return wrapGet(rec, err)
}

func (a *StoreAdapter) GetGoalByID(ctx context.Context, goalID string) (*Goal, error) {
	rec, err := a.st.GetGoalByID(ctx, goalID)
	return wrapGet(rec, err)
}

func (a *StoreAdapter) UpdateGoal(ctx context.Context, g *Goal) error {
	rec, err := toRecord(g)
	if err != nil {
		return err
	}
	if err := a.st.UpdateGoal(ctx, rec); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (a *StoreAdapter) UpdateObjective(ctx context.Context, goalID, objective string) error {
	if err := a.st.UpdateGoalObjective(ctx, goalID, objective); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (a *StoreAdapter) DeleteGoal(ctx context.Context, goalID string) error {
	return a.st.DeleteGoal(ctx, goalID)
}

func (a *StoreAdapter) ListGoalsByOwner(ctx context.Context, ownerUserID string, limit int) ([]*Goal, error) {
	recs, err := a.st.ListGoalsByOwner(ctx, ownerUserID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*Goal, len(recs))
	for i := range recs {
		out[i] = fromRecord(&recs[i])
	}
	return out, nil
}

func wrapGet(rec *store.GoalRecord, err error) (*Goal, error) {
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if rec == nil {
		return nil, ErrNotFound
	}
	return fromRecord(rec), nil
}

func toRecord(g *Goal) (*store.GoalRecord, error) {
	usageJSON, err := json.Marshal(g.LastAccountedTokenUsage)
	if err != nil {
		return nil, err
	}
	return &store.GoalRecord{
		ID:                      g.ID,
		AgentID:                 g.AgentID,
		SessionKey:              g.SessionKey,
		OwnerUserID:             g.OwnerUserID,
		Channel:                 g.Channel,
		AccountID:               g.AccountID,
		ChatID:                  g.ChatID,
		ProjectID:               g.ProjectID,
		Objective:               g.Objective,
		Status:                  string(g.Status),
		TokenBudget:             g.TokenBudget,
		TokensUsed:              g.TokensUsed,
		LastAccountedTokenUsage: usageJSON,
		TimeUsedSeconds:         g.TimeUsedSeconds,
		LastAccountedAt:         g.LastAccountedAt,
		SafetyMaxIterations:     g.SafetyMaxIterations,
		Iterations:              g.Iterations,
		CreatedAt:               g.CreatedAt,
		UpdatedAt:               g.UpdatedAt,
	}, nil
}

func fromRecord(rec *store.GoalRecord) *Goal {
	g := &Goal{
		ID:                  rec.ID,
		AgentID:             rec.AgentID,
		SessionKey:          rec.SessionKey,
		OwnerUserID:         rec.OwnerUserID,
		Channel:             rec.Channel,
		AccountID:           rec.AccountID,
		ChatID:              rec.ChatID,
		ProjectID:           rec.ProjectID,
		Objective:           rec.Objective,
		Status:              Status(rec.Status),
		TokenBudget:         rec.TokenBudget,
		TokensUsed:          rec.TokensUsed,
		TimeUsedSeconds:     rec.TimeUsedSeconds,
		LastAccountedAt:     rec.LastAccountedAt,
		SafetyMaxIterations: rec.SafetyMaxIterations,
		Iterations:          rec.Iterations,
		CreatedAt:           rec.CreatedAt,
		UpdatedAt:           rec.UpdatedAt,
	}
	if len(rec.LastAccountedTokenUsage) > 0 {
		// Ignore unmarshal errors — empty/invalid payloads just mean
		// "no baseline yet," which is the same as a zero-valued
		// TokenUsage. Logging here would spam on every fresh goal.
		_ = json.Unmarshal(rec.LastAccountedTokenUsage, &g.LastAccountedTokenUsage)
	}
	return g
}
