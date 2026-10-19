package setup

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/api"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type recordingControlPlane struct {
	users      []string
	agents     []string
	epochs     []string
	broadcasts []string
}

func (r *recordingControlPlane) UserSpaceFor(string) (*api.UserSpaceView, error) {
	return nil, nil
}
func (r *recordingControlPlane) LocalAgentManager() *agent.Manager { return nil }
func (r *recordingControlPlane) IsCloudMode() bool                 { return true }
func (r *recordingControlPlane) InvalidateUser(userID string)      { r.users = append(r.users, userID) }
func (r *recordingControlPlane) InvalidateAgent(agentID string)    { r.agents = append(r.agents, agentID) }
func (r *recordingControlPlane) ReloadAgents() error               { return nil }
func (r *recordingControlPlane) NotifySystemReload() error         { return nil }
func (r *recordingControlPlane) BumpAgentReloadEpoch(userID string) error {
	r.epochs = append(r.epochs, userID)
	return nil
}
func (r *recordingControlPlane) BroadcastAgentReload(userID string) error {
	r.broadcasts = append(r.broadcasts, userID)
	return nil
}
func (r *recordingControlPlane) ReloadSandbox() error   { return nil }
func (r *recordingControlPlane) ReloadTaskQueue() error { return nil }
func (r *recordingControlPlane) RegisterChannelFromConfig(store.ConfigRecord) error {
	return nil
}
func (r *recordingControlPlane) RegisterChannel(store.ChannelRecord) error { return nil }
func (r *recordingControlPlane) UnregisterChannel(string, string)          {}
func (r *recordingControlPlane) RemoveWorkspaceFile(context.Context, string, string, string, string) error {
	return nil
}

func TestSetUserResolverAdoptsControlPlane(t *testing.T) {
	cp := &recordingControlPlane{}
	s := &Server{}
	s.SetUserResolver(cp)
	s.notifyUserChanged("user-1")
	if len(cp.epochs) != 1 || cp.epochs[0] != "user-1" {
		t.Fatalf("epoch bumps = %v; want [user-1]", cp.epochs)
	}
	if len(cp.broadcasts) != 1 || cp.broadcasts[0] != "user-1" {
		t.Fatalf("broadcasts = %v; want [user-1]", cp.broadcasts)
	}
}
