package scope

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// TestSaveSettingRejectsAgentScopeSandbox pins the write-side guard: an
// agent-scope sandbox row is never read by the runtime (the executor pool is
// built once from the system row), so accepting it only produced agents that
// demanded an executor nobody had created. The write must fail loudly instead
// of being stored and ignored.
func TestSaveSettingRejectsAgentScopeSandbox(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	err := SaveSetting(ctx, db, "", "agent-x", SandboxNamespace, map[string]interface{}{"enabled": true})
	if err == nil {
		t.Fatalf("agent-scope sandbox write was accepted; it is never read")
	}
	if !strings.Contains(err.Error(), "system/user-scope") {
		t.Fatalf("error should explain the scope rule, got: %v", err)
	}
	if rec, gerr := db.GetConfigByName(ctx, store.KindSetting, "", "agent-x", SandboxNamespace); gerr == nil && rec != nil {
		t.Fatalf("rejected write still landed: %+v", rec)
	}

	// The legacy (scope, scopeID) bridge goes through the same guard — this
	// is how the HTTP layer and the CLI reach it.
	if err := SaveSettingByScope(ctx, db, Agent, "agent-x", SandboxNamespace, map[string]interface{}{"enabled": true}); err == nil {
		t.Fatalf("SaveSettingByScope bypassed the agent-scope sandbox guard")
	}

	// System and user scope stay writable: those are the layers the gateway
	// actually reads (buildSystemSandboxPool / assembleConfig).
	if err := SaveSetting(ctx, db, "", "", SandboxNamespace, map[string]interface{}{"enabled": true}); err != nil {
		t.Fatalf("system-scope sandbox must still be writable: %v", err)
	}
	if err := SaveSetting(ctx, db, "u_owner", "", SandboxNamespace, map[string]interface{}{"enabled": true}); err != nil {
		t.Fatalf("user-scope sandbox must still be writable: %v", err)
	}
}

// TestAgentToolScopeStillWritable is the counterpart guard: tools.* at agent
// scope is NOT rejected, because the runtime now reads it (see
// gateway.toolConfigForAgent).
func TestAgentToolScopeStillWritable(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := SaveSetting(ctx, db, "", "agent-x", "tools.categories", map[string]interface{}{
		"web_search": map[string]interface{}{"primary": "searxng/default"},
	}); err != nil {
		t.Fatalf("agent-scope tools.categories must be writable: %v", err)
	}
	got := map[string]struct {
		Primary string `json:"primary"`
	}{}
	if err := ExactSetting(ctx, db, "tools.categories", "", "agent-x", &got); err != nil {
		t.Fatalf("ExactSetting: %v", err)
	}
	if got["web_search"].Primary != "searxng/default" {
		t.Fatalf("agent-scope tools row did not round trip: %+v", got)
	}
}
