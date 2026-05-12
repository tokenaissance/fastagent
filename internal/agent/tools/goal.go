package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
)

// RegisterGoalTools wires the three model-callable goal tools onto r.
// The schema and validation mirror Codex CLI's goal_spec.rs:
//
//   - get_goal: no parameters; returns the current goal or {"status": "no_goal"}.
//   - create_goal: requires `objective`, optional `token_budget`. Fails when a goal
//     already exists for the session — the user has to clear it first.
//   - update_goal: status is a single-value enum {"complete"}. Pause / resume /
//     budget_limited transitions are user- or runtime-controlled, not model.
//
// All three resolve the session via r.GoalSessionKey() at execute time —
// no per-call wiring. The agent loop's HandleMessage / HandleMessageStream
// already calls r.SetGoalSessionKey(sess.SessionKey()) at the top of every
// turn.
func RegisterGoalTools(r *Registry, st goal.Store, agentID, ownerUserID string) {
	r.Register("get_goal",
		"Get the current goal for this session, including status, token budget, "+
			"tokens used, and remaining tokens. Returns {\"status\": \"no_goal\"} when "+
			"no goal is set — that's not an error.",
		map[string]interface{}{
			"type":                 "object",
			"properties":           map[string]interface{}{},
			"additionalProperties": false,
		},
		makeGetGoal(st, r, agentID),
	)

	r.Register("create_goal",
		"Create a goal for the current session. Only call this when the user or "+
			"developer instructions explicitly ask for one — never infer a goal from "+
			"an ordinary task. Fails if a goal already exists; use update_goal to "+
			"mark an existing goal complete, or have the user /goal clear first. "+
			"Set token_budget only when an explicit budget is requested.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"objective": map[string]interface{}{
					"type":        "string",
					"description": "Concrete objective to pursue. Should include the scoped target, expected end state, explicit non-goals, and a verification path.",
				},
				"token_budget": map[string]interface{}{
					"type":        "integer",
					"description": "Optional positive token budget. Counts non-cached input + output tokens. Omit for an unbounded goal.",
				},
			},
			"required":             []string{"objective"},
			"additionalProperties": false,
		},
		makeCreateGoal(st, r, agentID, ownerUserID),
	)

	r.Register("update_goal",
		"Mark the active goal complete. Status is restricted to \"complete\"; "+
			"pausing, resuming, and budget_limited transitions are controlled by "+
			"the user or the runtime, not by the model. Only call this when the "+
			"objective has actually been achieved and no required work remains — "+
			"do not call it merely because the budget is nearly exhausted or "+
			"because you want to stop.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"status": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"complete"},
					"description": "Must be the literal string \"complete\".",
				},
			},
			"required":             []string{"status"},
			"additionalProperties": false,
		},
		makeUpdateGoal(st, r, agentID),
	)
}

// makeGetGoal returns a ToolFunc that reads the current goal. Returns
// a {"status": "no_goal"} envelope rather than an error when nothing
// is set — the model treats that as informational, not a failure.
func makeGetGoal(st goal.Store, r *Registry, agentID string) ToolFunc {
	return func(ctx context.Context, _ json.RawMessage) (string, error) {
		sessionKey := r.GoalSessionKey()
		if sessionKey == "" {
			return jsonString(map[string]any{"status": "no_goal"})
		}
		g, err := st.GetGoalBySession(ctx, agentID, sessionKey)
		if errors.Is(err, goal.ErrNotFound) {
			return jsonString(map[string]any{"status": "no_goal"})
		}
		if err != nil {
			return "", fmt.Errorf("get_goal: %w", err)
		}
		return jsonString(goalView(g))
	}
}

// makeCreateGoal returns a ToolFunc that inserts a fresh active goal.
// Two failure modes the model can recover from:
//   - no session context (tool called outside a chat turn)
//   - a goal already exists for this session (user must clear first)
//
// Anything else (DB unreachable, validation) bubbles up as an error.
func makeCreateGoal(st goal.Store, r *Registry, agentID, ownerUserID string) ToolFunc {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var a struct {
			Objective   string `json:"objective"`
			TokenBudget *int64 `json:"token_budget,omitempty"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("create_goal: parse args: %w", err)
		}
		objective := strings.TrimSpace(a.Objective)
		if objective == "" {
			return "", errors.New("create_goal: objective is required")
		}
		if a.TokenBudget != nil && *a.TokenBudget <= 0 {
			return "", errors.New("create_goal: token_budget must be positive when provided")
		}

		sessionKey := r.GoalSessionKey()
		if sessionKey == "" {
			return "", errors.New("create_goal: no active session context")
		}

		g := &goal.Goal{
			ID:          newGoalID(),
			AgentID:     agentID,
			SessionKey:  sessionKey,
			OwnerUserID: ownerUserID,
			Objective:   objective,
			Status:      goal.StatusActive,
			TokenBudget: a.TokenBudget,
		}
		if err := st.CreateGoal(ctx, g); err != nil {
			if errors.Is(err, goal.ErrAlreadyExists) {
				return "", errors.New("create_goal: a goal already exists for this session; clear it first")
			}
			return "", fmt.Errorf("create_goal: %w", err)
		}
		return jsonString(goalView(g))
	}
}

// makeUpdateGoal returns a ToolFunc that flips the active goal to
// Complete. The status field is enum-restricted to "complete" at the
// schema level — we re-validate here defensively in case a non-OpenAI
// provider ships through a non-conforming model response.
func makeUpdateGoal(st goal.Store, r *Registry, agentID string) ToolFunc {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		var a struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("update_goal: parse args: %w", err)
		}
		if a.Status != "complete" {
			return "", fmt.Errorf(
				"update_goal: status must be \"complete\"; pause / resume / budget_limited are user- or runtime-controlled, not model-controlled")
		}

		sessionKey := r.GoalSessionKey()
		if sessionKey == "" {
			return "", errors.New("update_goal: no active session context")
		}

		g, err := st.GetGoalBySession(ctx, agentID, sessionKey)
		if errors.Is(err, goal.ErrNotFound) {
			return "", errors.New("update_goal: no active goal for this session")
		}
		if err != nil {
			return "", fmt.Errorf("update_goal: load goal: %w", err)
		}
		if g.Status != goal.StatusActive {
			return "", fmt.Errorf("update_goal: goal status is %q; only an active goal can be marked complete", g.Status)
		}

		g.Status = goal.StatusComplete
		if err := st.UpdateGoal(ctx, g); err != nil {
			return "", fmt.Errorf("update_goal: %w", err)
		}
		return jsonString(map[string]any{
			"ok":                true,
			"final_token_usage": g.TokensUsed,
		})
	}
}

// goalView reduces a domain Goal to the model-visible map both
// get_goal and create_goal emit. Hides internal accounting fields
// (LastAccountedTokenUsage, Iterations, ...) — the model only needs
// the user-facing slice.
func goalView(g *goal.Goal) map[string]any {
	v := map[string]any{
		"objective":         g.Objective,
		"status":            string(g.Status),
		"tokens_used":       g.TokensUsed,
		"time_used_seconds": g.TimeUsedSeconds,
	}
	if g.TokenBudget != nil {
		v["token_budget"] = *g.TokenBudget
		if remaining, ok := g.RemainingTokens(); ok {
			v["remaining_tokens"] = remaining
		}
	}
	return v
}

// jsonString marshals v and returns the string form — every ToolFunc
// ultimately returns a string, and the encode-then-stringify dance
// shows up enough across goal tools to deserve a tiny helper.
func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// newGoalID returns a fresh opaque identifier for a goal row. Random
// bytes rather than the cron tool's time-prefixed format because
// goals are rare per session — one at a time — and the time prefix
// would leak the creation moment into the ID with no payoff.
func newGoalID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// On the off chance the entropy source fails, surfacing a
		// non-unique fallback is worse than panicking. crypto/rand
		// errors are exotic enough this is acceptable.
		panic(fmt.Sprintf("goal: crypto/rand failed: %v", err))
	}
	return "g-" + hex.EncodeToString(buf[:])
}
