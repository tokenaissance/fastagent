package setup

import (
	"context"
	"net/http"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// handleChatQueue returns the durable wait list for one session.
//
// The dashboard needs a snapshot, not only live `queued` events: a reload must
// render every waiting submission with its text, and the position must be the
// store's seq order rather than a pod-local count. Entries are already limited
// to this user's agent and session by the query.
func (s *Server) handleChatQueue(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agentId")
	sessionID := r.URL.Query().Get("sessionId")
	if agentID == "" || sessionID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "agentId and sessionId are required"})
		return
	}
	uid := s.effectiveUserID(r)
	if uid == "" {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	qs, ok := s.dataStore.(store.SessionQueueStore)
	if !ok {
		jsonResponse(w, http.StatusOK, map[string]any{"entries": []any{}})
		return
	}
	// Opportunistic cleanup keeps finished rows bounded without a separate
	// sweeper: only the reader of this session's queue pays for it.
	if cleaner, ok := s.dataStore.(interface {
		CleanupSessionQueue(ctx context.Context, olderThan time.Duration) (int64, error)
	}); ok {
		_, _ = cleaner.CleanupSessionQueue(r.Context(), time.Hour)
	}
	list, err := qs.ListSessionQueue(r.Context(), uid, agentID, sessionID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	now := time.Now()
	position := 0
	entries := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if e.State != store.SessionQueueQueued || !e.ExpiresAt.After(now) {
			continue
		}
		position++
		entries = append(entries, map[string]any{
			"turnId":    e.ID,
			"position":  position,
			"text":      e.Text,
			"expiresAt": e.ExpiresAt.UTC().Format(time.RFC3339),
			"createdAt": e.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"entries": entries})
}
