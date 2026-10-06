package gateway

/**
 * [INPUT]: the gateway's store and its existing assembly (assembleConfig,
 *          config.ResolveAgents, resolveAgentScopeOverrides, ensureAgentHome).
 * [OUTPUT]: gatewayConfigStore — the agentconfig.Store adapter — plus
 *           Gateway.resolvedAgentFor, the function the agent runtime uses to
 *           read one scope through the read cache.
 * [POS]: Adapter between the inner use case (internal/agentconfig) and this
 *        framework layer. The dependency points inward: agentconfig knows
 *        nothing about the gateway, and this file carries the only forward
 *        reference. resolveOneAgentConfig runs the same resolution the build
 *        path runs, for one agent, so the cache and the build cannot disagree.
 * [PROTOCOL]: On change, update this header, then check
 *        docs/fastagent/design/15-agent-config-consistency.md §4 and
 *        internal/agentconfig (the port it implements).
 */

import (
	"context"
	"fmt"

	"github.com/fastclaw-ai/fastclaw/internal/agentconfig"
	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// gatewayConfigStore implements agentconfig.Store.
type gatewayConfigStore struct{ g *Gateway }

func (s gatewayConfigStore) CurrentVersion(ctx context.Context) (agentconfig.Version, error) {
	return s.g.store.CurrentConfigEpoch(ctx)
}

func (s gatewayConfigStore) Resolve(ctx context.Context, scope agentconfig.Scope) (config.ResolvedAgent, error) {
	return s.g.resolveOneAgentConfig(ctx, scope)
}

// resolveOneAgentConfig returns one agent's resolved configuration. It reuses
// the build path's steps in the same order: assemble system ← user, apply env
// and defaults, resolve the agent entry (the store-first file loader supplies
// the authoritative MCP table and the file config), overlay the agent scope,
// then fix the home paths. It deliberately skips the parts of loadUserSpace
// that are not configuration — bindings, managers, skill hydration — because a
// read must not touch the object store.
func (g *Gateway) resolveOneAgentConfig(ctx context.Context, scope agentconfig.Scope) (config.ResolvedAgent, error) {
	if scope.UserID == "" || scope.AgentID == "" {
		return config.ResolvedAgent{}, fmt.Errorf("resolve agent config: user and agent IDs are required")
	}
	if g.store == nil {
		return config.ResolvedAgent{}, fmt.Errorf("resolve agent config: no store on this gateway")
	}
	cfg, err := assembleConfig(ctx, g.store, scope.UserID, "")
	if err != nil {
		return config.ResolvedAgent{}, fmt.Errorf("resolve agent config: assemble: %w", err)
	}
	config.LoadEnv().ApplyToConfig(cfg)
	config.ApplyDefaults(cfg)

	rec, err := g.store.GetAgent(ctx, scope.AgentID)
	if err != nil {
		return config.ResolvedAgent{}, fmt.Errorf("resolve agent config: load agent %s: %w", scope.AgentID, err)
	}
	resolved := config.ResolveAgents(cfg, []config.AgentEntry{{ID: rec.ID, UserID: rec.UserID, Name: rec.Name}}, makeStoreFirstAgentFileLoader(g.store))
	if len(resolved) == 0 {
		return config.ResolvedAgent{}, fmt.Errorf("resolve agent config: agent %s did not resolve", scope.AgentID)
	}
	rc := &resolved[0]
	resolveAgentScopeOverrides(ctx, g.store, rc)
	ensureAgentHome(*rc)
	return *rc, nil
}

// agentConfigCache builds the process-wide read cache once. The use case needs
// only the store, so lazy construction keeps boot order irrelevant.
func (g *Gateway) agentConfigCache() (*agentconfig.Resolve, error) {
	g.rcOnce.Do(func() {
		g.rcCache, g.rcErr = agentconfig.New(gatewayConfigStore{g: g}, agentconfig.NewMemoryMemo(0))
	})
	return g.rcCache, g.rcErr
}

// resolvedAgentFor is the read the agent runtime makes: the current
// configuration for one scope, never older than the counter the store reports.
// A counter read or a resolve that fails returns an error, never a silent
// older value (agentconfig I3).
func (g *Gateway) resolvedAgentFor(ctx context.Context, scope agentconfig.Scope) (config.ResolvedAgent, error) {
	uc, err := g.agentConfigCache()
	if err != nil {
		return config.ResolvedAgent{}, err
	}
	return uc.For(ctx, scope)
}

// AgentConfigCacheSnapshot reports the read cache's behaviour for the ops surface
// (docs/fastagent/design/15-agent-config-consistency.md §10: check p99, rebuild
// rate, version lag). The second value is false when the cache cannot be built,
// which is an unwired or store-less process: zeros would read as a healthy idle
// pod instead of a missing measurement.
//
// It builds the cache if no read has happened yet. That is deliberate: the
// construction is idempotent, and reading the field directly would race with
// the sync.Once that fills it.
func (g *Gateway) AgentConfigCacheSnapshot(ctx context.Context) (agentconfig.Snapshot, bool) {
	uc, err := g.agentConfigCache()
	if err != nil {
		return agentconfig.Snapshot{}, false
	}
	snapshot, err := uc.Snapshot(ctx)
	if err != nil {
		return agentconfig.Snapshot{}, false
	}
	return snapshot, true
}
