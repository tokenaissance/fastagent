package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/api"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/daemon"
	"github.com/fastclaw-ai/fastclaw/internal/gateway"
	"github.com/fastclaw-ai/fastclaw/internal/setup"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// apiResolver adapts *gateway.Gateway to api.UserResolver. The bridge lives
// here (in main) so the api package doesn't need to import gateway.
type apiResolver struct {
	gw *gateway.Gateway
}

func (a *apiResolver) UserSpaceFor(userID string) (*api.UserSpaceView, error) {
	sp, err := a.gw.UserSpaceFor(userID)
	if err != nil {
		return nil, err
	}
	return &api.UserSpaceView{
		UserID: sp.UserID,
		Agents: sp.Agents,
		Config: sp.Config,
	}, nil
}

func (a *apiResolver) LocalAgentManager() *agent.Manager { return a.gw.AgentManager() }
func (a *apiResolver) IsCloudMode() bool                 { return a.gw.IsCloudMode() }

func main() {
	rootCmd := &cobra.Command{
		Use:   "fastclaw",
		Short: "FastClaw - Lightweight AI Agent Framework",
		// No args = default to gateway (so double-click on Windows works)
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGateway(18953)
		},
	}

	rootCmd.AddCommand(gatewayCmd())
	rootCmd.AddCommand(agentCmd())
	rootCmd.AddCommand(skillCmd())
	rootCmd.AddCommand(sessionCmd())
	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(upgradeCmd())
	rootCmd.AddCommand(doctorCmd())
	rootCmd.AddCommand(backupCmd())
	rootCmd.AddCommand(resetCmd())
	rootCmd.AddCommand(pluginCmd())
	rootCmd.AddCommand(providerCmd())
	rootCmd.AddCommand(sandboxCmd())
	rootCmd.AddCommand(policyCmd())
	rootCmd.AddCommand(daemonCmd())
	rootCmd.AddCommand(userCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func gatewayCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Start the FastClaw gateway (loads all agents)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGateway(port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 18953, "port for setup wizard / web UI")
	return cmd
}

func runGateway(port int) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Boot with whatever config we can find — fastclaw.json is optional
	// since #4 made the configs row in the store the source of truth.
	// When neither file nor store has anything, the gateway still starts;
	// the web UI's /onboard page configures product fields against the
	// running gateway, which writes to the store and hot-reloads in
	// place. Storage / sandbox infra defaults come from env.toml or
	// FASTCLAW_* env vars (resolving to SQLite at ~/.fastclaw/fastclaw.db
	// when nothing is set — see store.New).
	cfg, err := config.Load()
	if err != nil {
		switch {
		case hasInfraEnv():
			slog.Info("no fastclaw.json found, bootstrapping from env")
		default:
			slog.Info("no config found; web UI will run onboarding flow",
				"url", fmt.Sprintf("http://localhost:%d", port))
		}
		cfg = &config.Config{}
	}

	// Env vars win over JSON for infra fields — see ApplyToConfig. This
	// is what makes FASTCLAW_STORAGE_DSN / FASTCLAW_OBJECT_STORE_* / etc.
	// actually take effect at runtime.
	config.LoadEnv().ApplyToConfig(cfg)

	slog.Info("starting gateway")

	// Install bundled skills (if not already present)
	agent.InstallBundledSkills()

	// Write PID file for daemon management
	if err := daemon.WritePIDFile(); err != nil {
		slog.Warn("failed to write PID file", "error", err)
	}
	defer daemon.RemovePIDFile()

	gw, err := gateway.New(cfg)
	if err != nil {
		return fmt.Errorf("create gateway: %w", err)
	}

	// Start web UI server alongside gateway
	gwCfg := &cfg.Gateway
	if gwCfg.Port > 0 {
		port = gwCfg.Port
	}

	webSrv := setup.NewServer(port, nil)
	webSrv.SetAgentProvider(&agentProviderAdapter{mgr: gw.AgentManager(), gw: gw})
	webSrv.SetTaskQueue(gw.TaskQueue())
	webSrv.SetGatewayConfig(gwCfg)
	webSrv.SetUserResolver(&apiResolver{gw: gw})
	webSrv.SetStore(gw.Store())
	webSrv.SetWorkspaceStore(gw.Workspace())
	webSrv.SetUsageMeter(gw.Usage())

	// Set up OpenAI-compatible API and WebSocket gateway. The user registry
	// (apikeys.json) is loaded unconditionally — empty in fresh local installs
	// and harmless either way, but required for the admin UI's API Keys page
	// and for any agent binding flow. The cloud-vs-local distinction only
	// shows up downstream in canAccessAgent / data partitioning.
	gatewayToken := cfg.Gateway.Auth.Token
	userReg, regErr := users.Load(gw.Store())
	if regErr != nil {
		slog.Warn("failed to load user registry", "error", regErr)
		userReg = nil
	} else {
		slog.Info("user registry loaded", "apikeys", userReg.Count(), "mode", cfg.Gateway.Mode)
	}
	apiSrv := api.NewServer(&apiResolver{gw: gw}, gatewayToken, userReg, gwCfg)
	webSrv.SetAPIServer(apiSrv)
	webSrv.SetAuth(gatewayToken, userReg)

	// Agent ↔ API key bindings. Empty by default, every agent admin-owned
	// until something points at it.
	if bindings, err := users.LoadBindings(gw.Store()); err == nil {
		webSrv.SetAgentBindings(bindings)
	} else {
		slog.Warn("failed to load agent bindings", "error", err)
	}

	// Migrate legacy ~/.fastclaw/apikeys.json + agent-bindings.json into the
	// store on first startup. Idempotent: only fires if the store is empty
	// AND the legacy files exist. Logs each imported entry once and renames
	// the legacy files to *.migrated.bak so a second run won't redo the work.
	migrateLegacyAuthFiles(gw.Store(), userReg)

	bindMode := gwCfg.Bind
	if bindMode == "" {
		bindMode = "loopback"
	}
	authMode := gwCfg.Auth.Mode
	if authMode == "" {
		authMode = "token"
	}
	slog.Info("gateway API enabled",
		"port", port,
		"bind", bindMode,
		"auth", authMode,
		"chatCompletions", gwCfg.HTTP.Endpoints.ChatCompletions.Enabled,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := webSrv.Run(ctx); err != nil {
			slog.Error("web server error", "error", err)
		}
	}()

	url := fmt.Sprintf("http://localhost:%d", port)
	slog.Info("web UI available", "url", url)
	// Auto-open the browser on a fresh install (no providers, no agents).
	// Used to be runSetupWizard's job; now runGateway handles onboarding
	// itself — same UX, one less process.
	if len(cfg.Providers) == 0 {
		go openBrowser(url)
	}

	return gw.Run()
}

// agentProviderAdapter adapts agent.Manager to setup.AgentProvider.
type agentProviderAdapter struct {
	mgr *agent.Manager
	gw  *gateway.Gateway
}

func (a *agentProviderAdapter) AllAgents() []setup.AgentHandle {
	agents := a.mgr.All()
	result := make([]setup.AgentHandle, len(agents))
	for i, ag := range agents {
		result[i] = ag
	}
	return result
}

func (a *agentProviderAdapter) AgentByID(id string) setup.AgentHandle {
	ag := a.mgr.AgentByID(id)
	if ag == nil {
		return nil
	}
	return ag
}

func (a *agentProviderAdapter) ReloadAgents() error {
	return a.gw.ReloadAgents()
}

// hasInfraEnv reports whether the environment carries enough infra config
// to run without a fastclaw.json. Used by runGateway to skip the setup
// wizard in container/K8s deployments where JSON doesn't exist but env is
// comprehensive.
//
// The gate is loose on purpose: one of these vars being set strongly
// implies "this is a container deploy, don't prompt the user". Missing
// ones (e.g. token when mode=local) are fine because ApplyToConfig still
// populates them from defaults.
func hasInfraEnv() bool {
	for _, k := range []string{
		"FASTCLAW_MODE",
		"FASTCLAW_AUTH_TOKEN",
		"FASTCLAW_STORAGE_TYPE",
		"FASTCLAW_STORAGE_DSN",
		"FASTCLAW_OBJECT_STORE_TYPE",
	} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	cmd.Run()
}

// migrateLegacyAuthFiles imports ~/.fastclaw/apikeys.json and
// ~/.fastclaw/agent-bindings.json into the Store on first boot, then
// renames them to *.migrated.bak so subsequent restarts skip the work.
//
// Idempotent. The DBStore path needs this on the first SaaS deployment to
// pick up keys originally created in local-file mode; FileStore deployments
// just continue reading the same file path the Store points at, so this
// migration is effectively a no-op there (table-already-populated check
// keeps it from looping forever).
//
// Tokens in the legacy file were stored in plaintext under a `key` field.
// We hash them on import so the at-rest format upgrades for free.
func migrateLegacyAuthFiles(st store.Store, _ *users.Registry) {
	if st == nil {
		return
	}
	home, err := config.HomeDir()
	if err != nil {
		return
	}

	migrateLegacyAPIKeys(st, filepath.Join(home, "apikeys.json"))
	migrateLegacyBindings(st, filepath.Join(home, "agent-bindings.json"))
	migrateLegacyAgentJSON(st)
}

// migrateLegacyAgentJSON promotes per-agent overrides that were written to
// `workspace_files['agent.json']` (the pre-store layout) into the
// `agents.config` column where the runtime loader now reads them. Skips
// agents whose `agents.config` is already populated — repeat runs and
// concurrent writes through the new code path stay correct. The old
// workspace_files row is left in place; it's harmless and serves as a
// breadcrumb for anyone debugging an older deploy.
func migrateLegacyAgentJSON(st store.Store) {
	if st == nil {
		return
	}
	ctx := context.Background()
	agents, err := st.ListAgents(ctx)
	if err != nil {
		return
	}
	migrated := 0
	for _, ag := range agents {
		if len(ag.Config) > 0 {
			continue // already on the new layout
		}
		data, err := st.GetWorkspaceFile(ctx, ag.ID, "agent.json")
		if err != nil || len(data) == 0 {
			continue
		}
		var asMap map[string]interface{}
		if err := json.Unmarshal(data, &asMap); err != nil {
			slog.Warn("legacy agent.json parse failed", "agent", ag.ID, "error", err)
			continue
		}
		ag.Config = asMap
		ag.UpdatedAt = time.Now().UTC()
		if err := st.SaveAgent(ctx, &ag); err != nil {
			slog.Warn("legacy agent.json import failed", "agent", ag.ID, "error", err)
			continue
		}
		migrated++
	}
	if migrated > 0 {
		slog.Info("migrated legacy agent.json into agents.config", "count", migrated)
	}
}

func migrateLegacyAPIKeys(st store.Store, path string) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		slog.Warn("legacy apikeys read failed", "error", err)
		return
	}
	// Skip if the store already has rows — we've migrated before, or the
	// operator is running both file + DB modes simultaneously and wants the
	// DB rows to win.
	if existing, err := st.ListAPIKeys(context.Background()); err == nil && len(existing) > 0 {
		return
	}
	var legacy []struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		Key       string    `json:"key"`
		CreatedAt time.Time `json:"createdAt"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		slog.Warn("legacy apikeys parse failed", "error", err)
		return
	}
	imported := 0
	for _, ak := range legacy {
		if ak.ID == "" || ak.Key == "" {
			continue
		}
		sum := sha256.Sum256([]byte(ak.Key))
		prefix := ak.Key
		if len(prefix) > 10 {
			prefix = prefix[:10]
		}
		rec := &store.APIKeyRecord{
			ID:        ak.ID,
			Name:      ak.Name,
			KeyHash:   hex.EncodeToString(sum[:]),
			KeyPrefix: prefix,
			CreatedAt: ak.CreatedAt,
		}
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = time.Now().UTC()
		}
		if err := st.CreateAPIKey(context.Background(), rec); err != nil {
			slog.Warn("legacy apikey import failed", "id", ak.ID, "error", err)
			continue
		}
		imported++
	}
	if imported > 0 {
		slog.Info("migrated legacy apikeys.json into store", "count", imported)
	}
	// Rename so we don't redo the work on next boot.
	_ = os.Rename(path, path+".migrated.bak")
}

func migrateLegacyBindings(st store.Store, path string) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		slog.Warn("legacy bindings read failed", "error", err)
		return
	}
	if existing, err := st.ListAgentBindings(context.Background()); err == nil && len(existing) > 0 {
		return
	}
	var legacy map[string]string
	if err := json.Unmarshal(data, &legacy); err != nil {
		slog.Warn("legacy bindings parse failed", "error", err)
		return
	}
	imported := 0
	for agentID, ownerID := range legacy {
		if agentID == "" || ownerID == "" {
			continue
		}
		if err := st.SetAgentBinding(context.Background(), agentID, ownerID); err != nil {
			slog.Warn("legacy binding import failed", "agent", agentID, "error", err)
			continue
		}
		imported++
	}
	if imported > 0 {
		slog.Info("migrated legacy agent-bindings.json into store", "count", imported)
	}
	_ = os.Rename(path, path+".migrated.bak")
}

