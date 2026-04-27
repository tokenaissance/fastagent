package setup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// readIdentityFile returns the bytes of one of an agent's identity files
// (SOUL.md, BOOTSTRAP.md, agent.json, …). It always goes through the
// configured Store so file-mode and Postgres-mode deployments stay consistent.
//
// When no Store is configured — only in tests or during early startup — the
// function falls back to reading the agent's home dir directly. Production
// always has one (gateway sets it unconditionally).
func (s *Server) readIdentityFile(ctx context.Context, agentID, filename string) ([]byte, error) {
	if s.dataStore != nil {
		return s.dataStore.GetWorkspaceFile(ctx, agentID, filename)
	}
	homePath, err := config.AgentHomeDir(agentID)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(homePath, filename))
}

// writeIdentityFile persists one of an agent's identity files via the Store.
// Same Store→filesystem fallback as readIdentityFile.
//
// For Postgres-backed stores this is the only durable write — the filesystem
// copy goes away on pod restart, which is the whole point of stateless pods.
// For file stores the Store writes to ~/.fastclaw/agents/<id>/agent/<name>
// which is where the agent runtime reads from, so file-mode behavior is
// unchanged.
func (s *Server) writeIdentityFile(ctx context.Context, agentID, filename string, data []byte) error {
	if s.dataStore != nil {
		return s.dataStore.SaveWorkspaceFile(ctx, agentID, filename, data)
	}
	homePath, err := config.AgentHomeDir(agentID)
	if err != nil {
		return err
	}
	target := filepath.Join(homePath, filename)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

// deleteIdentityFile removes the DB override row (or FS file in store-less
// mode). Used by the Customize UI's "revert to base" action — once the
// override is gone, ContextBuilder.loadFile falls through to the FS base
// shipped with the agent definition.
func (s *Server) deleteIdentityFile(ctx context.Context, agentID, filename string) error {
	if s.dataStore != nil {
		return s.dataStore.DeleteWorkspaceFile(ctx, agentID, filename)
	}
	homePath, err := config.AgentHomeDir(agentID)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(homePath, filename))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// loadAgentFileConfig returns an agent's per-agent overrides (model,
// skills, providers, …). The source of truth is the `agents.config`
// column; legacy installs that wrote to a `workspace_files` row named
// "agent.json" are read as a fallback so an upgrade doesn't lose their
// data — the migration in main.go promotes that data into `agents.config`
// on first boot.
func (s *Server) loadAgentFileConfig(ctx context.Context, agentID string) (*config.AgentFileConfig, error) {
	if s.dataStore != nil {
		if rec, err := s.dataStore.GetAgent(ctx, agentID); err == nil && rec != nil && len(rec.Config) > 0 {
			blob, merr := json.Marshal(rec.Config)
			if merr == nil {
				var cfg config.AgentFileConfig
				if uerr := json.Unmarshal(blob, &cfg); uerr == nil {
					return &cfg, nil
				}
			}
		}
	}
	// Legacy fallback: pre-migration installs persisted agent.json as a
	// workspace file. Treat empty content the same as missing.
	if data, err := s.readIdentityFile(ctx, agentID, "agent.json"); err == nil && len(data) > 0 {
		var cfg config.AgentFileConfig
		if err := json.Unmarshal(data, &cfg); err == nil {
			return &cfg, nil
		}
	}
	return &config.AgentFileConfig{}, nil
}

// saveAgentFileConfig persists per-agent overrides into `agents.config`,
// the column that backs the runtime AgentFileConfigLoader. The row is
// upserted via SaveAgent which preserves the existing Name / Model /
// CreatedAt fields when present (we read them first), so admins can
// PATCH-style update individual sections without clobbering siblings.
func (s *Server) saveAgentFileConfig(ctx context.Context, agentID string, cfg *config.AgentFileConfig) error {
	if s.dataStore == nil {
		// Store-less mode (tests, embedded wizard) — keep the legacy file
		// path so existing flows that read agent.json from FS still work.
		data, _ := json.MarshalIndent(cfg, "", "  ")
		return s.writeIdentityFile(ctx, agentID, "agent.json", data)
	}
	rec, err := s.dataStore.GetAgent(ctx, agentID)
	if err != nil || rec == nil {
		rec = &store.AgentRecord{ID: agentID, Name: agentID}
	}
	// Round-trip through JSON so the AgentRecord.Config map matches the
	// AgentFileConfig schema exactly (including omitempty semantics).
	blob, _ := json.Marshal(cfg)
	var asMap map[string]interface{}
	if err := json.Unmarshal(blob, &asMap); err != nil {
		return err
	}
	rec.Config = asMap
	// Mirror the model field on the agents row so list endpoints (which
	// read directly from `agents.model`) stay in sync without a join.
	if cfg.Model != "" {
		rec.Model = cfg.Model
	}
	rec.UpdatedAt = time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = rec.UpdatedAt
	}
	return s.dataStore.SaveAgent(ctx, rec)
}

// isStoreNotFound recognises the "file not found" signal across Store
// implementations. FileStore returns os.ErrNotExist; DBStore (database/sql)
// returns sql.ErrNoRows; occasionally we get a wrapped string form. Covering
// all three keeps the UI's "tab is empty" behavior consistent regardless of
// backend.
func isStoreNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "no rows in result set") || strings.Contains(msg, "no such file")
}
