package config

/**
 * [INPUT]: one agents.defaults row (the agent-scope override the CLI and the
 *          dashboard write) and one resolved agent.
 * [OUTPUT]: ModelFor — the model precedence chain; ApplyAgentDefaults — the
 *           non-model fields of that row applied in place; MergeAgentProviders —
 *           agent-scope provider rows merged over the resolved ones.
 * [POS]: Entities. These are the resolution rules, and they are pure: the caller
 *        reads the row and passes it in. Both gateway call sites (the owner path
 *        and the chatter path) call these, so the two cannot drift. Before,
 *        each site carried its own copy of the field list and a comment asking
 *        the next reader to keep them aligned.
 * [PROTOCOL]: On change, update this header, then check
 *        docs/fastagent/design/15-agent-config-consistency.md §11.1 (the layer
 *        table) and the callers in internal/gateway.
 */

// ModelFor answers "which model will this agent actually run", given the
// layer values a caller has gathered. It exists because this contract is
// load-bearing and was previously encoded in statement order twice (the owner's own
// space and a foreign viewer's lazy attach) plus a prose table in
// docs/configs-kv-scope-adaptation.md. Nothing failed if the order changed.
//
// Order, most specific last:
//
//	base     system ← the CALLER's user row (already merged by
//	         assembleConfig/ResolveAgents, so the caller's own choice is in here)
//	ownerRow the agent OWNER's user-scope model — a foreign viewer with sharing
//	         on runs with the credentials the owner intended
//	agentRow the agent-scope model — the agent's own configuration, which wins
//	         over both user layers for everyone except the case below
//	pinRow   the VIEWER's own explicit user-scope model, pinned last for a
//	         foreign viewer ("MY tokens, MY model")
//
// An empty string means "this layer has no row"; callers pass "" for layers
// that do not apply to them (the owner's own space passes no ownerRow and no
// pinRow — its user row is already the base).
//
// Note on the pin: EnsureAgent applies it unconditionally today (it is only
// non-empty when the viewer set an explicit row), and this function preserves
// that rather than second-guessing it. Whether a NON-foreign caller should get
// the same pin is a separate product decision, not a refactor.
func ModelFor(base, ownerRow, agentRow, pinRow string) string {
	model := base
	if ownerRow != "" {
		model = ownerRow
	}
	if agentRow != "" {
		model = agentRow
	}
	if pinRow != "" {
		model = pinRow
	}
	return model
}

// ApplyAgentDefaults overlays the non-model fields of one agents.defaults row
// onto rc. It returns the row's model instead of writing it, because the agent
// scope is not always the winner: a chatter's explicit pin outranks it. The
// caller places the returned value with ModelFor.
//
// Integer fields use "0 means no opinion" and string fields use "empty means no
// opinion". The two pointer fields carry the distinction the ints cannot:
// SplitReplies and AutoPersist may be explicitly false, which is different from
// a row that never set them.
func ApplyAgentDefaults(rc *ResolvedAgent, row AgentDefaults) (model string) {
	if row.MaxTokens > 0 {
		rc.MaxTokens = row.MaxTokens
	}
	if row.Temperature > 0 {
		rc.Temperature = row.Temperature
	}
	if row.MaxToolIterations > 0 {
		rc.MaxToolIterations = row.MaxToolIterations
	}
	if row.MaxToolIterationContinues != nil {
		rc.MaxToolIterationContinues = *row.MaxToolIterationContinues
	}
	if row.MaxParallelToolCalls > 0 {
		rc.MaxParallelToolCalls = row.MaxParallelToolCalls
	}
	if row.Thinking != "" {
		rc.Thinking = row.Thinking
	}
	if row.PolicyPreset != "" {
		rc.PolicyPreset = row.PolicyPreset
	}
	if row.PromptMode != "" {
		rc.PromptMode = row.PromptMode
	}
	// Per-agent WeChat split replies. A non-nil value is a deliberate operator
	// choice. Nil falls through to the system WeChatCfg.SplitReplies later.
	if row.SplitReplies != nil {
		v := *row.SplitReplies
		rc.SplitReplies = &v
	}
	// Per-agent autoPersist, same pointer semantics. Nil falls through to the
	// system MemoryCfg.AutoPersist.Enabled.
	if row.AutoPersist != nil {
		v := *row.AutoPersist
		rc.AutoPersist = &v
	}
	return row.Model
}

// MergeAgentProviders overlays agent-scope provider rows over the resolved map.
// assembleConfig runs with agentID="" for the build path, so the resolved map
// carries system and user rows only. Without this overlay a per-agent
// credential is invisible, and providerForAgent falls back to the shared
// provider: the agent's chosen model id then goes to the wrong base URL.
func MergeAgentProviders(rc *ResolvedAgent, agentProviders map[string]ProviderConfig) {
	for k, v := range agentProviders {
		if rc.Providers == nil {
			rc.Providers = make(map[string]ProviderConfig)
		}
		rc.Providers[k] = v
	}
}
