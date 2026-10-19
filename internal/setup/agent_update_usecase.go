package setup

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

var errInvalidPromptMode = errors.New("promptMode must be one of: agent, chatbot, customize")

// updateAgentRequest is the dashboard/API patch body for one agent. Pointer
// fields distinguish "leave unchanged" from "clear this override".
type updateAgentRequest struct {
	Name              string                            `json:"name,omitempty"`
	Description       *string                           `json:"description,omitempty"`
	Model             *string                           `json:"model,omitempty"`
	IsPublic          *bool                             `json:"isPublic,omitempty"`
	ShareModelConfig  *bool                             `json:"shareModelConfig,omitempty"`
	PromptMode        *string                           `json:"promptMode,omitempty"`
	SplitReplies      *bool                             `json:"splitReplies,omitempty"`
	SplitRepliesReset bool                              `json:"splitRepliesReset,omitempty"`
	AutoPersist       *bool                             `json:"autoPersist,omitempty"`
	AutoPersistReset  bool                              `json:"autoPersistReset,omitempty"`
	SharedIdentity    *bool                             `json:"sharedIdentity,omitempty"`
	Plugins           map[string]bool                   `json:"plugins,omitempty"`
	PluginsReset      bool                              `json:"pluginsReset,omitempty"`
	MCPServers        map[string]config.MCPServerConfig `json:"mcpServers,omitempty"`
	MCPServersReset   bool                              `json:"mcpServersReset,omitempty"`
}

// updateAgentConfig owns the whole write sequence: agent row, MCP rows,
// agents.defaults, plugins.enabled, shared identity, then cache invalidation
// and cross-replica reload. The HTTP handler only decodes, authorizes and
// renders.
func (s *Server) updateAgentConfig(
	r *http.Request,
	rec *store.AgentRecord,
	req updateAgentRequest,
) (*store.AgentRecord, error) {
	if req.Name != "" {
		rec.Name = req.Name
	}
	if req.Description != nil {
		if rec.Config == nil {
			rec.Config = map[string]interface{}{}
		}
		if *req.Description == "" {
			delete(rec.Config, "description")
		} else {
			rec.Config["description"] = *req.Description
		}
	}
	if req.IsPublic != nil {
		rec.IsPublic = *req.IsPublic
	}
	if req.ShareModelConfig != nil {
		if rec.Config == nil {
			rec.Config = map[string]interface{}{}
		}
		if *req.ShareModelConfig {
			delete(rec.Config, "shareModelConfig")
		} else {
			rec.Config["shareModelConfig"] = false
		}
	}
	if req.MCPServersReset || req.MCPServers != nil {
		if err := s.dataStore.ReplaceMCPServers(r.Context(), rec.ID, req.MCPServers); err != nil {
			return rec, fmt.Errorf("replace mcp servers: %w", err)
		}
	}
	if err := s.dataStore.SaveAgent(r.Context(), rec); err != nil {
		return rec, fmt.Errorf("save agent: %w", err)
	}

	defaultsPatch := map[string]interface{}{}
	if req.Model != nil {
		if model := strings.TrimSpace(*req.Model); model == "" {
			defaultsPatch["model"] = nil
		} else {
			defaultsPatch["model"] = model
		}
	}
	if req.PromptMode != nil {
		switch mode := strings.TrimSpace(*req.PromptMode); mode {
		case "":
			defaultsPatch["promptMode"] = nil
		case config.PromptModeAgent, config.PromptModeChatbot, config.PromptModeCustomize:
			defaultsPatch["promptMode"] = mode
		default:
			return rec, errInvalidPromptMode
		}
	}
	if req.SplitRepliesReset {
		defaultsPatch["splitReplies"] = nil
	} else if req.SplitReplies != nil {
		defaultsPatch["splitReplies"] = *req.SplitReplies
	}
	if req.AutoPersistReset {
		defaultsPatch["autoPersist"] = nil
	} else if req.AutoPersist != nil {
		defaultsPatch["autoPersist"] = *req.AutoPersist
	}
	if err := s.applyAgentScopeDefaultsPatch(r, rec.ID, defaultsPatch); err != nil {
		return rec, fmt.Errorf("save agent defaults: %w", err)
	}
	if req.PluginsReset || req.Plugins != nil {
		if err := s.applyAgentScopePluginsPatch(r, rec.ID, req.Plugins, req.PluginsReset); err != nil {
			return rec, fmt.Errorf("save agent plugins: %w", err)
		}
	}
	if req.SharedIdentity != nil {
		channels, _ := s.dataStore.ListChannels(r.Context(), rec.UserID, rec.ID)
		for i := range channels {
			if channels[i].SharedIdentity != *req.SharedIdentity {
				channels[i].SharedIdentity = *req.SharedIdentity
				_ = s.dataStore.SaveChannel(r.Context(), &channels[i])
			}
		}
	}
	s.notifyAgentChanged(rec.UserID, rec.ID)
	return rec, nil
}
