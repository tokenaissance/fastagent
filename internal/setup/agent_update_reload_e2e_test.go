package setup

// A dashboard/API agent write must reach every replica, not just the pod that
// served the request.
//
// The model a chatter gets comes from the agent's resolved config, which each
// replica caches inside its UserSpace. PUT /api/agents/{id} used to call
// invalidateAgent (local only), so a switch made through the dashboard applied
// on one replica and left the others firing the previous model until their
// 30-minute idle eviction — the "I switched the model and nothing changed"
// report this pins shut.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/api"
)

// recordingAgentResolver mirrors cmd/fastclaw's apiResolver: it satisfies
// api.UserResolver and the narrow capability interfaces the setup handlers
// type-assert for (the same shape the real gateway resolver presents).
type recordingAgentResolver struct {
	invalidatedAgents []string
	invalidatedUsers  []string
	epochBumps        []string
	broadcasts        []string
}

func (r *recordingAgentResolver) UserSpaceFor(string) (*api.UserSpaceView, error) {
	return nil, nil
}
func (r *recordingAgentResolver) LocalAgentManager() *agent.Manager { return nil }
func (r *recordingAgentResolver) IsCloudMode() bool                 { return true }
func (r *recordingAgentResolver) InvalidateUser(userID string) {
	r.invalidatedUsers = append(r.invalidatedUsers, userID)
}
func (r *recordingAgentResolver) InvalidateAgent(agentID string) {
	r.invalidatedAgents = append(r.invalidatedAgents, agentID)
}
func (r *recordingAgentResolver) BumpAgentReloadEpoch(userID string) error {
	r.epochBumps = append(r.epochBumps, userID)
	return nil
}
func (r *recordingAgentResolver) BroadcastAgentReload(userID string) error {
	r.broadcasts = append(r.broadcasts, userID)
	return nil
}

func TestUpdateAgentModelPropagatesToOtherReplicas(t *testing.T) {
	s, uid, aid := setupFileUploadTest(t)
	resolver := &recordingAgentResolver{}
	s.userResolver = resolver

	req := httptest.NewRequest(http.MethodPut, "/api/agents/"+aid,
		strings.NewReader(`{"model":"deepseek/deepseek-v4-flash"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", aid)
	req = stampAuthAndUserID(req, uid)
	rec := httptest.NewRecorder()
	s.handleUpdateAgent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(resolver.invalidatedAgents) != 1 || resolver.invalidatedAgents[0] != aid {
		t.Fatalf("InvalidateAgent calls = %v, want [%s] (this pod's caches)", resolver.invalidatedAgents, aid)
	}
	// The two cross-replica markers: the DB epoch (polled by replicas without
	// Redis) and the Redis broadcast (instant). Without them a sibling replica
	// keeps the old model until idle eviction.
	if len(resolver.epochBumps) != 1 || resolver.epochBumps[0] != uid {
		t.Fatalf("epoch bumps = %v, want [%s]", resolver.epochBumps, uid)
	}
	if len(resolver.broadcasts) != 1 || resolver.broadcasts[0] != uid {
		t.Fatalf("broadcasts = %v, want [%s]", resolver.broadcasts, uid)
	}
}

// A resolver that offers no cross-replica hooks must degrade to the local
// invalidate instead of panicking — the type assertions are the seam.
func TestUpdateAgentModelWithoutCrossReplicaHooks(t *testing.T) {
	s, uid, aid := setupFileUploadTest(t)
	s.userResolver = localOnlyResolver{}

	req := httptest.NewRequest(http.MethodPut, "/api/agents/"+aid,
		strings.NewReader(`{"model":"deepseek/deepseek-v4-flash"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", aid)
	req = stampAuthAndUserID(req, uid)
	rec := httptest.NewRecorder()
	s.handleUpdateAgent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

// localOnlyResolver implements InvalidateAgent/InvalidateUser and nothing else.
type localOnlyResolver struct{}

func (localOnlyResolver) UserSpaceFor(string) (*api.UserSpaceView, error) { return nil, nil }
func (localOnlyResolver) LocalAgentManager() *agent.Manager              { return nil }
func (localOnlyResolver) IsCloudMode() bool                              { return false }
func (localOnlyResolver) InvalidateUser(string)                          {}
func (localOnlyResolver) InvalidateAgent(string)                         {}
