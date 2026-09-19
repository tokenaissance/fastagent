package setup

import (
	"context"
	"encoding/json"
	"net/http"
)

// pendingWebTurn is a dashboard chat POST that has been accepted but whose turn
// has not started yet (it is queued behind another turn on the same session).
//
// The web UI offers "Edit"/"Cancel" on a queued message — Codex's queue
// preview offers the same via its edit-queued-message binding and interrupt.
// Withdrawing the message has to cancel this request's turn, so the cancel
// func lives here and the "started" bit (flipped by the agent's admission
// signal, not by an event from the shared session hub, which also carries
// other turns' events) decides whether withdrawal is still honest.
type pendingWebTurn struct {
	cancel  context.CancelFunc
	started bool
}

// chatTurnKey identifies one client's chat POST. turnID is chosen by the
// client so a second tab cannot cancel this tab's queued message by accident.
func chatTurnKey(uid, agentID, sessionID, turnID string) string {
	return uid + "|" + agentID + "|" + sessionID + "|" + turnID
}

func (s *Server) registerPendingTurn(key string, cancel context.CancelFunc) *pendingWebTurn {
	turn := &pendingWebTurn{cancel: cancel}
	s.pendingTurnsMu.Lock()
	defer s.pendingTurnsMu.Unlock()
	if s.pendingTurns == nil {
		s.pendingTurns = make(map[string]*pendingWebTurn)
	}
	s.pendingTurns[key] = turn
	return turn
}

func (s *Server) unregisterPendingTurn(key string) {
	s.pendingTurnsMu.Lock()
	defer s.pendingTurnsMu.Unlock()
	delete(s.pendingTurns, key)
}

// markPendingTurnStarted flips the turn past the point where it can be
// withdrawn.
func (s *Server) markPendingTurnStarted(key string) {
	s.pendingTurnsMu.Lock()
	defer s.pendingTurnsMu.Unlock()
	if turn, ok := s.pendingTurns[key]; ok {
		turn.started = true
	}
}

type chatCancelRequest struct {
	AgentID   string `json:"agentId"`
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId"`
}

// handleChatCancel stops a turn the user no longer wants.
//
// Two shapes, one endpoint (design X1–X7, docs/session-turn-integrity.md A4):
//
//   - still queued  → withdraw it here by canceling its request context; the
//     turn never starts and nothing reaches the session. 200 {"canceled": true}
//   - already running → stamp a cancel request on the session's LIVE lease row.
//     The holder may be another replica; it reads the request at its next
//     iteration boundary (next to the fence check) and stops with a σ. 200
//     {"canceled": true, "wasRunning": true, "isRunning": false}
//   - nothing at all → 200 {"canceled": false, "wasRunning": false}. Deleting
//     the old 409 "already_started" is the point: that answer told the caller
//     to "use the normal stop", which only detached the client's stream while
//     the server kept working — a stop button that did not stop anything.
//
// Idempotent by construction: withdrawing twice is one withdrawal, and stamping
// the cancel twice is one request on the same possession.
func (s *Server) handleChatCancel(w http.ResponseWriter, r *http.Request) {
	var req chatCancelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	ag := s.resolveAgent(r, req.AgentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	key := chatTurnKey(uid, ag.Name(), req.SessionID, req.TurnID)
	s.pendingTurnsMu.Lock()
	pending, found := s.pendingTurns[key]
	s.pendingTurnsMu.Unlock()

	// The use case lives in turn_cancel.go: this handler only resolves the
	// caller and maps the outcome onto HTTP.
	canceled, wasRunning, err := cancelTurn(r.Context(), s.dataStore, pending, found, uid, ag.Name(), req.SessionID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"canceled":   canceled,
		"wasRunning": wasRunning,
		"isRunning":  false,
	})
}
