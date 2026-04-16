package setup

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// --- Agent Management ---

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadUserConfig(r)
	if err != nil {
		cfg = &config.Config{}
	}
	resolved := config.ResolveAgents(cfg)
	var agents []map[string]any
	for _, ra := range resolved {
		soul := ""
		soulPath := filepath.Join(ra.Workspace, "SOUL.md")
		if data, readErr := os.ReadFile(soulPath); readErr == nil {
			soul = string(data)
		}
		agents = append(agents, map[string]any{
			"id":                ra.ID,
			"model":             ra.Model,
			"workspace":         ra.Workspace,
			"maxTokens":         ra.MaxTokens,
			"temperature":       ra.Temperature,
			"maxToolIterations": ra.MaxToolIterations,
			"thinking":          ra.Thinking,
			"soul":              soul,
		})
	}
	if agents == nil {
		agents = []map[string]any{}
	}
	jsonResponse(w, http.StatusOK, agents)
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Soul  string `json:"soul"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.ID == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "id is required"})
		return
	}

	// Check for duplicate by checking if directory already exists
	agentDir, _ := config.AgentWorkspaceDir(req.ID)
	if _, err := os.Stat(agentDir); err == nil {
		jsonResponse(w, http.StatusConflict, map[string]any{"ok": false, "error": fmt.Sprintf("agent %q already exists", req.ID)})
		return
	}

	// Create agent workspace directory
	for _, dir := range []string{agentDir, filepath.Join(agentDir, "memory"), filepath.Join(agentDir, "sessions"), filepath.Join(agentDir, "skills")} {
		os.MkdirAll(dir, 0o755)
	}

	// Write SOUL.md
	if req.Soul != "" {
		os.WriteFile(filepath.Join(agentDir, "SOUL.md"), []byte(req.Soul), 0o644)
	} else {
		os.WriteFile(filepath.Join(agentDir, "SOUL.md"), []byte(fmt.Sprintf("# %s\n\nYou are a helpful AI agent.\n", req.ID)), 0o644)
	}

	// Write agent.json with model config
	agentCfg := config.AgentFileConfig{Model: req.Model}
	agentData, _ := json.MarshalIndent(agentCfg, "", "  ")
	os.WriteFile(filepath.Join(agentDir, "agent.json"), agentData, 0o644)

	// Touch the global config file to trigger gateway hot-reload (picks up new agent)
	if cfgPath, err := config.GlobalConfigPath(); err == nil {
		now := time.Now()
		os.Chtimes(cfgPath, now, now)
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Model string `json:"model"`
		Soul  string `json:"soul"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}

	// Update workspace files directly
	agentDir, _ := config.AgentWorkspaceDir(id)
	if _, err := os.Stat(agentDir); err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "agent not found"})
		return
	}

	if req.Soul != "" {
		os.WriteFile(filepath.Join(agentDir, "SOUL.md"), []byte(req.Soul), 0o644)
	}
	if req.Model != "" {
		agentCfg := config.AgentFileConfig{Model: req.Model}
		agentData, _ := json.MarshalIndent(agentCfg, "", "  ")
		os.WriteFile(filepath.Join(agentDir, "agent.json"), agentData, 0o644)
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agentDir, _ := config.AgentWorkspaceDir(id)

	// Remove the entire agent directory
	parent := filepath.Dir(agentDir) // ~/.fastclaw/agents/{id}
	if err := os.RemoveAll(parent); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
