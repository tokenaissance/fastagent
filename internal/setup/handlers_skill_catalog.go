package setup

import (
	"net/http"

	"github.com/fastclaw-ai/fastclaw/internal/skills"
)

// handleAgentSkillCatalog serves GET /api/agent-skills/catalog?agent=<id>: the
// catalog the cloud MCP egress publishes for one agent.
//
// Three things are decided here, and deliberately nowhere else:
//
//   - who may read it: the same ownership rule every other agent resource uses
//     (the caller is the owner, or a super_admin). The cloud has already checked
//     the token's audience; this is the second half of the judgement, and the
//     stricter one wins.
//   - which layers it contains: skills.PublishableLayers, so the ranking and the
//     layer paths stay in one place rather than being re-derived here.
//   - what an unreadable layer means: the handler behind this returns 500 rather
//     than a partial catalog, because a partial catalog is indistinguishable from
//     "those skills do not exist".
func (s *Server) handleAgentSkillCatalog(w http.ResponseWriter, r *http.Request) {
	dirs, ok := s.skillDirsForRequest(w, r)
	if !ok {
		return
	}
	skills.CatalogHandler(dirs).ServeHTTP(w, r)
}
