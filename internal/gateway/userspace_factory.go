package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/agentconfig"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/plugin"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	coderuntime "github.com/fastclaw-ai/fastclaw/internal/runtime"
	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// userSpaceFactory assembles one user's runtime in named steps. The registry
// still owns lazy loading, TTL, eviction and invalidation; the factory owns
// only the construction order.
type userSpaceFactory struct {
	userID            string
	mb                *bus.MessageBus
	st                store.Store
	ws                workspace.Store
	meter             usage.Meter
	quotaStore        usage.QuotaStore
	systemSandboxPool sandbox.ExecutorPool
	pluginMgr         *plugin.Manager
	projectRuntime    *coderuntime.Manager
	notifyAgent       func(userID, agentID string)
	rcProvider        func(ctx context.Context, scope agentconfig.Scope) (config.ResolvedAgent, error)
}

type userSpaceBuild struct {
	cfg          *config.Config
	prov         provider.Provider
	agentRecords []store.AgentRecord
	entries      []config.AgentEntry
	resolved     []config.ResolvedAgent
	agentMgr     *agent.Manager
}

func (f *userSpaceFactory) Build(ctx context.Context) (*UserSpace, error) {
	if f.userID == "" {
		return nil, fmt.Errorf("loadUserSpace: userID required")
	}
	if f.st == nil {
		return nil, fmt.Errorf("loadUserSpace: store required")
	}
	build := &userSpaceBuild{}
	if err := f.resolveConfig(ctx, build); err != nil {
		return nil, err
	}
	build.prov = newProviderFromConfig(build.cfg)
	if err := f.loadAgents(ctx, build); err != nil {
		return nil, err
	}
	f.bindChannels(ctx, build)
	f.resolveAgents(ctx, build)
	f.hydrateSkills(ctx, build)
	if err := f.buildManager(ctx, build); err != nil {
		return nil, err
	}
	f.bindToolChains(ctx, build)
	pool := f.attachSandbox(build)
	f.attachRuntime(build)
	f.registerPlugins(ctx, build)
	slog.Info("loaded user space", "user", f.userID, "agents", build.agentMgr.Names())
	return &UserSpace{
		UserID:         f.userID,
		Config:         build.cfg,
		Provider:       build.prov,
		Agents:         build.agentMgr,
		SandboxPool:    pool,
		PluginMgr:      f.pluginMgr,
		ProjectRuntime: f.projectRuntime,
	}, nil
}

func (f *userSpaceFactory) resolveConfig(ctx context.Context, build *userSpaceBuild) error {
	cfg, err := assembleConfig(ctx, f.st, f.userID, "")
	if err != nil {
		return fmt.Errorf("assemble config: %w", err)
	}
	config.LoadEnv().ApplyToConfig(cfg)
	config.ApplyDefaults(cfg)
	build.cfg = cfg
	return nil
}

func (f *userSpaceFactory) loadAgents(ctx context.Context, build *userSpaceBuild) error {
	records, err := f.st.ListAgents(ctx, f.userID)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}
	build.agentRecords = records
	build.entries = make([]config.AgentEntry, 0, len(records))
	for _, ar := range records {
		build.entries = append(build.entries, config.AgentEntry{ID: ar.ID, UserID: ar.UserID, Name: ar.Name})
	}
	return nil
}

func (f *userSpaceFactory) bindChannels(ctx context.Context, build *userSpaceBuild) {
	build.cfg.Bindings = append(
		build.cfg.Bindings,
		bindingsFromChannelRows(ctx, f.st, f.userID, build.agentRecords)...,
	)
}

func (f *userSpaceFactory) resolveAgents(ctx context.Context, build *userSpaceBuild) {
	build.resolved = config.ResolveAgents(
		build.cfg, build.entries, makeStoreFirstAgentFileLoader(f.st))
	for i := range build.resolved {
		rc := &build.resolved[i]
		resolveAgentScopeOverrides(ctx, f.st, rc)
		ensureAgentHome(*rc)
	}
}

func (f *userSpaceFactory) hydrateSkills(ctx context.Context, build *userSpaceBuild) {
	if f.ws == nil {
		return
	}
	for i := range build.resolved {
		rc := &build.resolved[i]
		if err := skills.HydrateSkillsDown(
			ctx, f.ws, rc.ID, filepath.Join(rc.Home, "skills"),
		); err != nil {
			slog.Warn("skill hydrate failed", "agent", rc.ID, "error", err)
		}
	}
	if dir, err := globalSkillsDirPath(); err == nil {
		if err := skills.HydrateSkillsDown(
			ctx, f.ws, skills.GlobalSkillOwner, dir, agent.BundledSkillNames()...,
		); err != nil {
			slog.Warn("global skill hydrate failed", "error", err)
		}
	}
}

func (f *userSpaceFactory) buildManager(ctx context.Context, build *userSpaceBuild) error {
	opts := managerOptions(
		build.cfg, f.userID, f.st, f.ws, f.meter, f.quotaStore, f.notifyAgent)
	if f.rcProvider != nil {
		opts = append(opts, agent.WithAgentConfigProvider(f.rcProvider))
	}
	if f.ws != nil {
		opts = append(opts, agent.WithWorkspaceStore(f.ws))
	}
	if f.meter != nil {
		opts = append(opts, agent.WithMeter(f.meter))
	}
	if f.quotaStore != nil {
		opts = append(opts, agent.WithQuotaStore(f.quotaStore))
	}
	mgr, err := agent.NewManager(build.resolved, build.prov, f.mb, opts...)
	if err != nil {
		return fmt.Errorf("create agent manager for user %q: %w", f.userID, err)
	}
	build.agentMgr = mgr
	return nil
}

func (f *userSpaceFactory) bindToolChains(ctx context.Context, build *userSpaceBuild) {
	agentIDs := make([]string, 0, len(build.agentMgr.All()))
	for _, ag := range build.agentMgr.All() {
		agentIDs = append(agentIDs, ag.Name())
	}
	overlays := toolOverlaysForAgents(ctx, f.st, f.userID, agentIDs)
	for _, ag := range build.agentMgr.All() {
		registerAgentToolChains(
			applyToolOverlay(build.cfg, overlays[ag.Name()]),
			[]*agent.Agent{ag},
			makeStoreFirstAgentFileLoader(f.st),
		)
	}
}

func (f *userSpaceFactory) attachSandbox(build *userSpaceBuild) sandbox.ExecutorPool {
	return attachSandboxToAgents(
		f.systemSandboxPool, f.userID, build.resolved, build.agentMgr)
}

func (f *userSpaceFactory) attachRuntime(build *userSpaceBuild) {
	if f.projectRuntime == nil {
		return
	}
	for _, ag := range build.agentMgr.All() {
		ag.SetProjectRuntime(f.projectRuntime)
	}
}

func (f *userSpaceFactory) registerPlugins(ctx context.Context, build *userSpaceBuild) {
	if f.pluginMgr == nil {
		return
	}
	for _, ag := range build.agentMgr.All() {
		registerHookPluginsForAgent(ctx, f.pluginMgr, f.st, ag)
	}
}
