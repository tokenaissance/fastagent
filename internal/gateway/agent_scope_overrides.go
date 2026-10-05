package gateway

/**
 * [INPUT]: the store (agent-scope rows and provider rows) plus one resolved agent.
 * [OUTPUT]: resolveAgentScopeOverrides — layers the agent-scope rows onto that
 *           agent in place: agents.defaults (model, limits, prompt mode, split
 *           replies, autoPersist) and the agent-scope provider keys.
 * [POS]: One of the two steps that turn system+user config into a resolved agent.
 *        loadUserSpace (every agent of a user) and resolveOneAgentConfig (one
 *        agent, for the read cache) both call it, so the two paths cannot drift.
 *        The rule itself belongs in internal/config eventually. This file is the
 *        framework-layer home it has today.
 * [PROTOCOL]: On change, update this header, then check
 *        docs/fastagent/design/15-agent-config-consistency.md §4.
 */

import (
	"context"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// resolveAgentScopeOverrides applies the agent-scope overlay to one resolved agent.
func resolveAgentScopeOverrides(ctx context.Context, st store.Store, rc *config.ResolvedAgent) {
	var agentOverride config.AgentDefaults
	if err := scope.ExactSetting(ctx, st, "agents.defaults", "", rc.ID, &agentOverride); err == nil {
		// Owner path: base is already system←user (ResolveAgents merged it
		// above), there is no owner row to layer (this IS the owner's own
		// space) and no viewer pin. So the agent row is the only overlay the
		// model can get — and it wins, which is the rule the settings page's
		// agent-context write depends on.
		rc.Model = resolveModel(rc.Model, "", agentOverride.Model, "")
		if agentOverride.MaxTokens > 0 {
			rc.MaxTokens = agentOverride.MaxTokens
		}
		if agentOverride.Temperature > 0 {
			rc.Temperature = agentOverride.Temperature
		}
		if agentOverride.MaxToolIterations > 0 {
			rc.MaxToolIterations = agentOverride.MaxToolIterations
		}
		if agentOverride.MaxToolIterationContinues != nil {
			rc.MaxToolIterationContinues = *agentOverride.MaxToolIterationContinues
		}
		if agentOverride.MaxParallelToolCalls > 0 {
			rc.MaxParallelToolCalls = agentOverride.MaxParallelToolCalls
		}
		if agentOverride.Thinking != "" {
			rc.Thinking = agentOverride.Thinking
		}
		if agentOverride.PolicyPreset != "" {
			rc.PolicyPreset = agentOverride.PolicyPreset
		}
		if agentOverride.PromptMode != "" {
			rc.PromptMode = agentOverride.PromptMode
		}
		// Per-agent WeChat split-replies — pointer semantics so
		// "absent" (no row, or row without the key) is distinct
		// from "explicitly false". Non-nil from the row means the
		// operator made a deliberate choice; nil falls through to
		// system WeChatCfg.SplitReplies later in NewAgentWithFullCfg.
		if agentOverride.SplitReplies != nil {
			v := *agentOverride.SplitReplies
			rc.SplitReplies = &v
		}
		// Per-agent autoPersist — same pointer semantics. Non-nil
		// here overrides the system/user memory.autoPersist.enabled
		// for this agent specifically. Used most by chatbot-mode
		// personas where the LLM can't write_file directly so the
		// background distill pass is the only persistence path.
		if agentOverride.AutoPersist != nil {
			v := *agentOverride.AutoPersist
			rc.AutoPersist = &v
		}
	}
	// Same story for providers: assembleConfig was called with
	// agentID="" so cfg.Providers (now in rc.Providers) only
	// carries system+user rows. Without this, a per-agent
	// provider key (e.g. an agent-scoped OpenRouter credential)
	// is invisible to providerForAgent, which falls back to the
	// shared provider — chat fires the agent's chosen model id
	// at the wrong base URL and gets a 400 from the wrong vendor.
	if agentProvs, err := scope.AgentScopeProviders(ctx, st, rc.ID); err == nil {
		for k, v := range agentProvs {
			if rc.Providers == nil {
				rc.Providers = make(map[string]config.ProviderConfig)
			}
			rc.Providers[k] = v
		}
	}
}
