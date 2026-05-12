package goal

import (
	"context"
	"errors"
)

// ErrAlreadyExists is returned by CreateGoal when the (agent,
// session_key) pair already has a goal. Callers must clear or update
// the existing goal first.
var ErrAlreadyExists = errors.New("goal already exists for this session")

// ErrNotFound is returned by GetGoal / UpdateGoal when no goal exists
// for the requested (agent, session_key).
var ErrNotFound = errors.New("goal not found")

// Store is the narrow persistence interface the GoalRuntime + slash
// handlers depend on. It's deliberately smaller than store.Store —
// every method is something the goal package actually calls. The
// concrete implementation lives in internal/store and is wrapped by
// StoreAdapter (store_adapter.go).
type Store interface {
	// CreateGoal inserts a fresh Active goal. Returns ErrAlreadyExists
	// when (agent_id, session_key) already has a row.
	CreateGoal(ctx context.Context, g *Goal) error

	// GetGoalBySession returns the goal for (agent, session_key) or
	// ErrNotFound. Returns the goal regardless of status — callers
	// inspect Status to decide whether continuation should fire.
	GetGoalBySession(ctx context.Context, agentID, sessionKey string) (*Goal, error)

	// GetGoalByID is the path used by REST handlers that get the goal
	// id from the URL.
	GetGoalByID(ctx context.Context, goalID string) (*Goal, error)

	// UpdateGoal writes the mutable fields back. Immutable fields
	// (ID, AgentID, SessionKey, OwnerUserID, CreatedAt, Objective) are
	// ignored on update — Objective has its own UpdateObjective path
	// so a stray mutation doesn't accidentally rewrite it without
	// firing the objective_updated continuation.
	UpdateGoal(ctx context.Context, g *Goal) error

	// UpdateObjective rewrites the objective text. Callers should
	// follow this with an ObjectiveUpdatedPrompt injection so the
	// model knows the target shifted.
	UpdateObjective(ctx context.Context, goalID, objective string) error

	// DeleteGoal removes the row. /goal clear is a hard delete by
	// design — audit history lives in the session transcript, not the
	// goals table.
	DeleteGoal(ctx context.Context, goalID string) error

	// ListGoalsByOwner is the cross-session query that powers a future
	// dashboard "my active goals" panel. Filter by status at the
	// caller — the SQL just orders by updated_at desc and caps at a
	// sane limit.
	ListGoalsByOwner(ctx context.Context, ownerUserID string, limit int) ([]*Goal, error)
}
