package setup

import (
	"context"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// ControlPlane is the composition-root contract for the side effects a
// control-plane write needs: cache invalidation, cross-replica reload markers,
// hot channel registration, and live sandbox file removal.
//
// The production resolver is checked against this interface at compile time
// (cmd/fastclaw/main.go). Setup handlers still keep narrow fallbacks for test
// resolvers, but a production resolver can no longer silently lose one of
// these capabilities.
type ControlPlane interface {
	InvalidateUser(userID string)
	InvalidateAgent(agentID string)
	ReloadAgents() error
	NotifySystemReload() error
	BumpAgentReloadEpoch(userID string) error
	BroadcastAgentReload(userID string) error
	ReloadSandbox() error
	ReloadTaskQueue() error
	RegisterChannelFromConfig(rec store.ConfigRecord) error
	RegisterChannel(rec store.ChannelRecord) error
	UnregisterChannel(channelType, accountID string)
	RemoveWorkspaceFile(ctx context.Context, agentID, projectID, sessionID, storePath string) error
}

// controlPlane returns the explicit contract when the resolver provides it.
// A nil result means the caller is an embedded/test resolver and should use
// its legacy narrow assertion.
func (s *Server) controlPlane() ControlPlane {
	if s.cp != nil {
		return s.cp
	}
	if cp, ok := s.userResolver.(ControlPlane); ok {
		return cp
	}
	return nil
}
