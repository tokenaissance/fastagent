package gateway

import (
	"context"
	"log/slog"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// InvalidateUser drops a user's cached UserSpace so the next access reloads
// it from the DB. Called by admin handlers after agent / provider /
// channel writes so changes take effect without a process restart.
func (g *Gateway) InvalidateUser(userID string) {
	if g.users == nil || userID == "" {
		return
	}
	g.users.invalidate(userID)
	slog.Info("user space invalidated; will reload on next access", "user", userID)
}

// ReloadAgents is kept on Gateway for callers (admin API after agent CRUD)
// that want to force a refresh of every loaded space. The new model lazy-
// loads on every auth, so the practical effect is just dropping caches.
func (g *Gateway) ReloadAgents() error {
	if g.users == nil {
		return nil
	}
	for _, sp := range g.users.all() {
		g.users.invalidate(sp.UserID)
	}
	slog.Info("hot-reload: invalidated all loaded user spaces")
	return nil
}

// reloadAgentForUser is a finer-grained invalidate used by setup handlers
// after a single user mutates their own agents.
func (g *Gateway) reloadAgentForUser(_ context.Context, userID string) {
	g.InvalidateUser(userID)
}

// RegisterChannelFromConfig hot-starts a channel adapter for a freshly-
// saved configs row without restarting the process. Called by the
// dashboard's per-agent channel handlers after a successful save so a
// new Telegram bot starts polling immediately. Idempotent at the
// chanMgr level — a re-save of the same accountID swaps the adapter.
func (g *Gateway) RegisterChannelFromConfig(rec store.ConfigRecord) error {
	if g.chanMgr == nil || g.bus == nil {
		return nil
	}
	return registerChannelInstance(rec, g.bus, g.chanMgr, true)
}

// UnregisterChannel removes a channel from the routing table. Note:
// the bot's polling goroutine is left to die when the root ctx ends —
// see channels.Manager.Unregister for why. Inbound messages stop
// routing to the agent the moment the binding row is deleted.
func (g *Gateway) UnregisterChannel(channelType, accountID string) {
	if g.chanMgr == nil {
		return
	}
	g.chanMgr.Unregister(channelType, accountID)
}
