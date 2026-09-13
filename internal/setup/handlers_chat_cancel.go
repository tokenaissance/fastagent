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

// handleChatCancel withdraws a queue-waiting turn.
//
//   - 200 {"canceled": true}  — the turn was still queued and is now withdrawn
//   - 409 {"reason": "already_started"} — the turn began; use the normal stop
//     affordance (which only detaches this client's stream — the server keeps
//     running turns alive on purpose)
//   - 404 {"reason": "not_queued"} — nothing registered for that turn
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
	turn, ok := s.pendingTurns[key]
	started := ok && turn.started
	s.pendingTurnsMu.Unlock()

	switch {
	case !ok:
		jsonResponse(w, http.StatusNotFound, map[string]any{"reason": "not_queued"})
	case started:
		jsonResponse(w, http.StatusConflict, map[string]any{"reason": "already_started"})
	default:
		// Canceling the request context makes the agent's AcquireTurn return
		// false, so the turn never starts and nothing reaches the session.
		turn.cancel()
		jsonResponse(w, http.StatusOK, map[string]any{"canceled": true})
	}
}
