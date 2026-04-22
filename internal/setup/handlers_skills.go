package setup

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
)

// --- Skills ---

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	homeDir, err := config.HomeDir()
	if err != nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}

	skillsDir := filepath.Join(homeDir, "skills")
	// Hydrate from object store first so pods that didn't handle the
	// original install still see the skill bundle. Pass the bundled skill
	// names as the keep-local list so an empty OSS response never causes
	// us to prune builtin skills. No-op when no object store is configured
	// (local mode) or nothing is mirrored yet.
	if s.workspaceStore != nil {
		if err := skills.HydrateSkillsDown(
			r.Context(), s.workspaceStore, skills.GlobalSkillOwner, skillsDir,
			agent.BundledSkillNames()...,
		); err != nil {
			slog.Warn("failed to hydrate global skills from object store", "error", err)
		}
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}

	var skills []map[string]string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		desc := ""
		skillPath := filepath.Join(skillsDir, name, "SKILL.md")
		if data, readErr := os.ReadFile(skillPath); readErr == nil {
			lines := strings.SplitN(string(data), "\n", 3)
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					desc = line
					break
				}
			}
		}
		skills = append(skills, map[string]string{
			"name":        name,
			"description": desc,
			"location":    filepath.Join(skillsDir, name),
			"type":        "skill",
		})
	}
	if skills == nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}
	jsonResponse(w, http.StatusOK, skills)
}

func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	homeDir, err := config.HomeDir()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	skillPath := filepath.Join(homeDir, "skills", name)
	if err := os.RemoveAll(skillPath); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Also remove from the object store so other pods drop it on their
	// next hydrate. Failure here shouldn't fail the delete — the local
	// copy is gone already and a stale remote copy will just re-appear
	// next reload (annoying but not dangerous).
	if s.workspaceStore != nil {
		if derr := skills.DeleteSkillUp(r.Context(), s.workspaceStore, skills.GlobalSkillOwner, name); derr != nil {
			slog.Warn("failed to remove global skill from object store", "skill", name, "error", derr)
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// handleListAgentSkills lists skills installed into an agent's own home
// directory (~/.fastclaw/agents/<id>/skills/). Loader "Layer 1" picks
// these up at the highest precedence — they're exclusive to the agent.
func (s *Server) handleListAgentSkills(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.canAccessAgent(callerFrom(r), id) {
		forbid(w, id)
		return
	}
	homePath, err := config.AgentHomeDir(id)
	if err != nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}
	skillsDir := filepath.Join(homePath, "skills")
	// Hydrate this agent's skills from object store on demand so replica
	// pods that haven't yet cached the bundle still list it in the UI.
	if s.workspaceStore != nil {
		if err := skills.HydrateSkillsDown(r.Context(), s.workspaceStore, id, skillsDir); err != nil {
			slog.Warn("failed to hydrate agent skills from object store",
				"agent", id, "error", err)
		}
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}

	var out []map[string]string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		desc := ""
		skillPath := filepath.Join(skillsDir, name, "SKILL.md")
		if data, readErr := os.ReadFile(skillPath); readErr == nil {
			for _, line := range strings.SplitN(string(data), "\n", 3) {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					desc = line
					break
				}
			}
		}
		out = append(out, map[string]string{
			"name":        name,
			"description": desc,
			"location":    filepath.Join(skillsDir, name),
			"type":        "skill",
		})
	}
	if out == nil {
		jsonResponse(w, http.StatusOK, []any{})
		return
	}
	jsonResponse(w, http.StatusOK, out)
}

// handleDeleteAgentSkill removes a skill from an agent's own home dir
// only. Global/shared skills are untouched.
func (s *Server) handleDeleteAgentSkill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	if !s.canAccessAgent(callerFrom(r), id) {
		forbid(w, id)
		return
	}
	homePath, err := config.AgentHomeDir(id)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	skillPath := filepath.Join(homePath, "skills", name)
	if err := os.RemoveAll(skillPath); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Delete from object store so other pods drop it on their next hydrate.
	if s.workspaceStore != nil {
		if derr := skills.DeleteSkillUp(r.Context(), s.workspaceStore, id, name); derr != nil {
			slog.Warn("failed to remove agent skill from object store",
				"agent", id, "skill", name, "error", derr)
		}
	}
	// Hot-reload the agent so the removed skill drops out of its context.
	if s.agentProvider != nil {
		if ag := s.agentProvider.AgentByID(id); ag != nil {
			ag.ReloadWorkspaceFiles()
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
