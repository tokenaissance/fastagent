package users

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// defaultAgentBootstrap returns the seed identity files for a freshly
// provisioned user's "default" agent. Shared by FS and DB code paths so
// onboarding produces identical content regardless of backend.
func defaultAgentBootstrap(userID string) map[string]string {
	return map[string]string{
		"SOUL.md":     fmt.Sprintf("# %s\n\nYou are a helpful AI agent for user %s.\n", userID, userID),
		"IDENTITY.md": fmt.Sprintf("# Identity\n\nYou are %s's personal FastClaw agent.\n", userID),
		"AGENTS.md":   "# Agent Capabilities\n\nDescribe what this agent can do.\n",
		"MEMORY.md":   "# Memory\n\nLong-term memory for this agent.\n",
	}
}

// ProvisionWorkspaceInStore seeds a new user's default agent into the
// supplied store — the DB-primary path used by cloud deployments. No
// filesystem writes; the caller's request context carries the user_id
// that the store layer scopes by. Safe to call multiple times.
func ProvisionWorkspaceInStore(ctx context.Context, st store.Store, userID string) error {
	if st == nil {
		return ProvisionWorkspace(userID)
	}
	const agentID = "default"
	const defaultModel = "gpt-4o"

	if existing, err := st.GetAgent(ctx, agentID); err == nil && existing != nil {
		return nil
	}

	now := time.Now().UTC()
	if err := st.SaveAgent(ctx, &store.AgentRecord{
		ID:        agentID,
		Name:      agentID,
		Model:     defaultModel,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("save agent: %w", err)
	}
	for name, content := range defaultAgentBootstrap(userID) {
		if err := st.SaveWorkspaceFile(ctx, agentID, name, []byte(content)); err != nil {
			return fmt.Errorf("save %s: %w", name, err)
		}
	}
	agentJSON, _ := json.MarshalIndent(config.AgentFileConfig{Model: defaultModel}, "", "  ")
	if err := st.SaveWorkspaceFile(ctx, agentID, "agent.json", agentJSON); err != nil {
		return fmt.Errorf("save agent.json: %w", err)
	}
	return nil
}

// ProvisionWorkspace is the legacy filesystem-based path. Still used when
// no store is available (early bootstrap, tests). Cloud callers should
// use ProvisionWorkspaceInStore.
//
// Safe to call multiple times — skips if already provisioned.
func ProvisionWorkspace(userID string) error {
	userDir, err := config.EnsureUserDir(userID)
	if err != nil {
		return err
	}

	cfgPath := filepath.Join(userDir, "fastclaw.json")
	if _, err := os.Stat(cfgPath); err == nil {
		return nil // already provisioned
	}

	cfg := config.Config{
		Providers: map[string]config.ProviderConfig{},
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Model:             "gpt-4o",
				MaxTokens:         8192,
				Temperature:       0.7,
				MaxToolIterations: 20,
			},
		},
		Channels: map[string]config.ChannelConfig{},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		return err
	}

	// Seed the default agent workspace.
	agentDir := filepath.Join(userDir, "agents", "default", "agent")
	for _, sub := range []string{"", "memory", "sessions", "skills"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0o755); err != nil {
			return err
		}
	}
	agentJSON, _ := json.MarshalIndent(config.AgentFileConfig{Model: "gpt-4o"}, "", "  ")
	if err := os.WriteFile(filepath.Join(agentDir, "agent.json"), agentJSON, 0o644); err != nil {
		return err
	}
	for name, content := range defaultAgentBootstrap(userID) {
		os.WriteFile(filepath.Join(agentDir, name), []byte(content), 0o644)
	}
	return nil
}
