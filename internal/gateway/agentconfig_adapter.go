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
	"log/slog"

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
	resolved := config.ResolveAgents(cfg, []config.AgentEntry{{ID: rec.ID, UserID: rec.UserID, Name: rec.Name}})
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

// noteConfigChange bumps the counter that every read compares against. It is
// the write side of the protocol, and it lives on the same choke points the
// cache invalidation already uses: a write that invalidates a cached space also
// makes every other replica's next read rebuild. Errors are logged, not
// swallowed: a bump that fails leaves readers trusting an entry the writer just
// replaced, which is the failure this protocol exists to remove.
func (g *Gateway) noteConfigChange(what string) {
	if g.store == nil {
		return
	}
	version, err := g.store.BumpConfigEpoch(context.Background())
	if err != nil {
		slog.Warn("config change was not stamped; readers may keep an older entry until the next write",
			"what", what, "error", err)
		return
	}
	slog.Debug("config epoch bumped", "what", what, "epoch", version)
}
