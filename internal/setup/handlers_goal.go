package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
)

// --- Per-agent /goal REST surface ---
//
// Mirrors the /goal slash command grammar at the HTTP layer so the
// dashboard (and any external automation) can drive the feature
// without going through a chat turn. Routes:
//
//   GET    /api/agents/{id}/goal?sessionKey=<k>  → fetch
//   POST   /api/agents/{id}/goal                 → action body
//   DELETE /api/agents/{id}/goal?sessionKey=<k>  → clear
//   GET    /api/agents/{id}/goals                → list goals across sessions
//
// The POST body's `action` field selects the operation; bundling
// create/pause/resume on one URL avoids minting a tree of routes
// for what are basically state transitions on a single resource.
//
// All routes go through requireAgentOwner so a caller can't peek at
// or mutate goals on agents they don't own.

// handleGetAgentGoal returns the active goal for one session, or
// 404 when no row exists. The dashboard hits this on every status
// poll so the path stays cheap: one indexed read, no joins.
func (s *Server) handleGetAgentGoal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.requireAgentOwner(w, r, id) == nil {
		return
	}
	sessionKey := r.URL.Query().Get("sessionKey")
	if sessionKey == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "sessionKey query parameter required"})
		return
	}
	st := goal.NewStoreAdapter(s.dataStore)
	g, err := st.GetGoalBySession(r.Context(), id, sessionKey)
	if errors.Is(err, goal.ErrNotFound) || g == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "no goal for this session"})
		return
	}
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"goal": g})
}

// handleListAgentGoals returns every goal row for the agent (across
// sessions). Powers a future dashboard panel that lists "active
// goals on this agent" without forcing the client to know every
// session_key up front.
func (s *Server) handleListAgentGoals(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, id)
	if rec == nil {
		return
	}
	// ListGoalsByOwner is keyed on owner_user_id (the agent owner).
	// Cap at 200 — pages of more than that should be paged via a
	// cursor parameter, but no caller has needed that yet.
	st := goal.NewStoreAdapter(s.dataStore)
	all, err := st.ListGoalsByOwner(r.Context(), rec.UserID, 200)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Filter to this agent — the by-owner index lists every agent the
	// owner has; we want only the named one.
	out := make([]*goal.Goal, 0, len(all))
	for _, g := range all {
		if g.AgentID == id {
			out = append(out, g)
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"goals": out})
}

// goalActionBody is the unified shape POST /goal accepts. The
// `action` field discriminates create / pause / resume; the others
// are payload for that action. Validating per-action keeps the
// handler readable without one struct per verb.
type goalActionBody struct {
	Action      string `json:"action"`     // "create" | "pause" | "resume"
	SessionKey  string `json:"sessionKey"` // always required
	Objective   string `json:"objective,omitempty"`
	TokenBudget *int64 `json:"tokenBudget,omitempty"`
	// Routing tuple — required on create so continuation publishes
	// land in the right chat. The slash path takes these straight
	// from the inbound message; REST callers have to supply them
	// because there's no inbound to read from.
	Channel   string `json:"channel,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
}

// handlePostAgentGoal dispatches create / pause / resume on a
// session's goal. After a successful mutation it calls Trigger on
// the running GoalManager so the state change takes effect
// immediately rather than waiting for the next user turn.
func (s *Server) handlePostAgentGoal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, id)
	if rec == nil {
		return
	}
	var body goalActionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if body.SessionKey == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "sessionKey is required"})
		return
	}
	st := goal.NewStoreAdapter(s.dataStore)

	switch body.Action {
	case "create":
		s.goalActionCreate(w, r, st, id, rec.UserID, &body)
	case "pause":
		s.goalTransition(w, r, st, id, body.SessionKey, goal.StatusActive, goal.StatusPaused)
	case "resume":
		s.goalTransition(w, r, st, id, body.SessionKey, goal.StatusPaused, goal.StatusActive)
	default:
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("unknown action %q (want create | pause | resume)", body.Action),
		})
	}
}

func (s *Server) goalActionCreate(w http.ResponseWriter, r *http.Request, st goal.Store, agentID, ownerUserID string, body *goalActionBody) {
	if body.Objective == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "objective is required for action=create"})
		return
	}
	if body.TokenBudget != nil && *body.TokenBudget <= 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "tokenBudget must be positive when provided"})
		return
	}
	// Routing tuple is required so continuations land in the right
	// chat. Without channel/chatId the goal row sits there with no
	// way for GoalRuntime.maybeContinue to publish back — the
	// slash path stamps these from the inbound message; REST has
	// to supply them explicitly.
	if body.Channel == "" || body.ChatID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{
			"error": "channel and chatId are required so continuations can route back to the originating chat",
		})
		return
	}
	g := &goal.Goal{
		ID:          newRESTGoalID(),
		AgentID:     agentID,
		SessionKey:  body.SessionKey,
		OwnerUserID: ownerUserID,
		Channel:     body.Channel,
		AccountID:   body.AccountID,
		ChatID:      body.ChatID,
		ProjectID:   body.ProjectID,
		Objective:   body.Objective,
		Status:      goal.StatusActive,
		TokenBudget: body.TokenBudget,
	}
	if err := st.CreateGoal(r.Context(), g); err != nil {
		if errors.Is(err, goal.ErrAlreadyExists) {
			jsonResponse(w, http.StatusConflict, map[string]any{"error": "a goal already exists for this session"})
			return
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.triggerGoalRuntime(r.Context(), agentID, body.SessionKey)
	s.publishGoalEvent(ownerUserID, agentID, body.SessionKey, agent.EventGoalCreated, map[string]any{
		"goal": goalRecordView(g),
	})
	jsonResponse(w, http.StatusCreated, map[string]any{"goal": g})
}

func (s *Server) goalTransition(w http.ResponseWriter, r *http.Request, st goal.Store, agentID, sessionKey string, from, to goal.Status) {
	g, err := st.GetGoalBySession(r.Context(), agentID, sessionKey)
	if errors.Is(err, goal.ErrNotFound) || g == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "no goal for this session"})
		return
	}
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if g.Status != from {
		jsonResponse(w, http.StatusConflict, map[string]any{
			"error":  fmt.Sprintf("goal status is %q; cannot transition from %q", g.Status, from),
			"status": g.Status,
		})
		return
	}
	g.Status = to
	if err := st.UpdateGoal(r.Context(), g); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Resume should immediately fire a continuation; pause should
	// halt one mid-flight if any. triggerGoalRuntime does the right
	// thing for both because maybeContinue re-reads status under the
	// continuation lock.
	s.triggerGoalRuntime(r.Context(), agentID, sessionKey)
	reason := "external"
	switch {
	case from == goal.StatusActive && to == goal.StatusPaused:
		reason = "user_paused"
	case from == goal.StatusPaused && to == goal.StatusActive:
		reason = "user_resumed"
	}
	s.publishGoalEvent(g.OwnerUserID, agentID, sessionKey, agent.EventGoalStatusChanged, map[string]any{
		"goal":   goalRecordView(g),
		"status": string(g.Status),
		"reason": reason,
	})
	jsonResponse(w, http.StatusOK, map[string]any{"goal": g})
}

// handleDeleteAgentGoal removes the goal row + stops the runtime.
// Idempotent: deleting a non-existent goal returns 200 with a
// "nothing to delete" note, not 404 — callers using DELETE for
// cleanup shouldn't have to distinguish.
func (s *Server) handleDeleteAgentGoal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.requireAgentOwner(w, r, id) == nil {
		return
	}
	sessionKey := r.URL.Query().Get("sessionKey")
	if sessionKey == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "sessionKey query parameter required"})
		return
	}
	st := goal.NewStoreAdapter(s.dataStore)
	g, err := st.GetGoalBySession(r.Context(), id, sessionKey)
	if errors.Is(err, goal.ErrNotFound) || g == nil {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "deleted": false})
		return
	}
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := st.DeleteGoal(r.Context(), g.ID); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Stop the runtime so a future create on the same session_key
	// gets a fresh goroutine instead of reusing a stale one.
	if a := s.runningAgent(r, id); a != nil && a.GoalManager() != nil {
		a.GoalManager().StopSession(sessionKey)
	}
	s.publishGoalEvent(g.OwnerUserID, id, sessionKey, agent.EventGoalCleared, map[string]any{
		"goalId": g.ID,
	})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "deleted": true})
}

// triggerGoalRuntime wakes the per-session GoalRuntime for the
// in-flight session so the runtime sees the mutation immediately.
// No-ops when the agent isn't running locally (caller may have
// mutated via REST against an idle pod) — the next user turn on
// that session will pick the change up via the trigger hook.
func (s *Server) triggerGoalRuntime(_ context.Context, agentID, sessionKey string) {
	a := s.runningAgentByID(agentID)
	if a == nil || a.GoalManager() == nil {
		return
	}
	if gr := a.GoalManager().Ensure(sessionKey, a.Name(), a.OwnerUserID()); gr != nil {
		gr.Trigger()
	}
}

// runningAgent returns the per-caller agent instance (if the agent
// is hot in this user space). REST handlers use this to reach the
// GoalManager for live triggers. Returns nil when the caller has no
// agent loaded (different pod, agent not yet provisioned, etc.).
func (s *Server) runningAgent(r *http.Request, agentID string) *agent.Agent {
	h := s.resolveAgent(r, agentID)
	if h == nil {
		return nil
	}
	if a, ok := h.(*agent.Agent); ok {
		return a
	}
	return nil
}

// runningAgentByID is the request-less variant — used by hooks
// that don't have an *http.Request handy. Returns nil when no
// agent loaded under any user space matches the id.
func (s *Server) runningAgentByID(agentID string) *agent.Agent {
	if s.userResolver == nil {
		return nil
	}
	// Walk the resolver's known agents. The userResolver doesn't
	// expose a flat list, so this is best-effort: external triggers
	// fired from a pod that doesn't host the agent simply fall
	// through to "no-op". The next user turn on the right pod will
	// see the mutation via the trigger hook.
	prov, ok := s.userResolver.(AgentProvider)
	if !ok {
		return nil
	}
	h := prov.AgentByID(agentID)
	if h == nil {
		return nil
	}
	if a, ok := h.(*agent.Agent); ok {
		return a
	}
	return nil
}

// publishGoalEvent fans a goal lifecycle event onto the chat-event
// hub so every SSE subscriber for this (user, agent, session) sees
// it. REST mutations need this because they don't ride a HandleMessage
// turn — without an explicit publish here, the only signal the
// frontend would get is "the next GET /goal will return new state",
// which loses the live-update guarantee the slash path enjoys via
// emitEvent.
//
// No-ops when the hub hasn't been initialized (test rigs that didn't
// install one). seq=-1 because REST mutations don't persist to
// session_events — they're out-of-band UI signals, not transcript
// fragments worth replaying on reconnect.
func (s *Server) publishGoalEvent(userID, agentID, sessionKey, eventType string, data map[string]any) {
	if s.chatEvents == nil {
		return
	}
	s.chatEvents.Publish(userID, agentID, sessionKey, agent.EventEnvelope{
		Seq:   -1,
		Event: agent.ChatEvent{Type: eventType, Data: data},
	})
}

// goalRecordView projects a domain *goal.Goal into the wire shape
// the SSE payload carries. Kept in sync with agent.goalToView — the
// REST handlers can't reach that one (it's package-private in
// internal/agent) so the projection is duplicated here. If you add a
// field there, mirror it here.
func goalRecordView(g *goal.Goal) map[string]any {
	if g == nil {
		return nil
	}
	v := map[string]any{
		"id":              g.ID,
		"agentId":         g.AgentID,
		"sessionKey":      g.SessionKey,
		"objective":       g.Objective,
		"status":          string(g.Status),
		"tokensUsed":      g.TokensUsed,
		"timeUsedSeconds": g.TimeUsedSeconds,
		"iterations":      g.Iterations,
	}
	if g.TokenBudget != nil {
		v["tokenBudget"] = *g.TokenBudget
		if remaining, ok := g.RemainingTokens(); ok {
			v["remainingTokens"] = remaining
		}
	}
	return v
}

// newRESTGoalID mints a fresh opaque goal id. Same shape the slash
// + tool paths use; duplicated here so the setup package doesn't
// reach into either of those just for the helper.
func newRESTGoalID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failure is exotic enough this is acceptable.
		panic(fmt.Sprintf("setup: crypto/rand failed: %v", err))
	}
	return "g-" + hex.EncodeToString(buf[:])
}
