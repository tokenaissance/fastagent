package setup

import (
	"log/slog"
	"net/http"

	"github.com/fastclaw-ai/fastclaw/internal/skills"
)

// skillDirsForRequest is the one gate both skill endpoints pass through, so neither
// can drift from the other. It carries the three decisions:
//
//   - who may read: the same ownership rule as every other agent resource, so the
//     cloud's token check (audience) and this one (ownership) are two halves of one
//     judgement, with the stricter winning;
//   - which layers: skills.PublishableLayers - the platform library, then the
//     agent's own skills. The per-chatter layer is excluded by design: it belongs to
//     whoever is chatting, and these tokens carry no chatter identity;
//   - what an unreadable layer means: the handlers behind this answer 500 rather
//     than a partial view, because a view missing skills is indistinguishable from
//     "those skills do not exist".
func (s *Server) skillDirsForRequest(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	agentID := r.URL.Query().Get("agent")
	if agentID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "agent is required"})
		return nil, false
	}
	if s.requireAgentOwner(w, r, agentID) == nil {
		return nil, false
	}

	layers, err := skills.PublishableLayers(agentID)
	if err != nil {
		slog.Warn("skills: cannot resolve layers", "agent", agentID, "error", err)
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return nil, false
	}

	dirs := make([]string, 0, len(layers))
	for _, layer := range layers {
		dirs = append(dirs, layer.Dir)
	}
	return dirs, true
}

// handleAgentSkillFile serves GET /api/agent-skills/file?agent=&skill=&path=: one
// file's bytes, on demand, and only for paths the catalog lists.
func (s *Server) handleAgentSkillFile(w http.ResponseWriter, r *http.Request) {
	dirs, ok := s.skillDirsForRequest(w, r)
	if !ok {
		return
	}
	skills.SkillFileHandler(dirs).ServeHTTP(w, r)
}
