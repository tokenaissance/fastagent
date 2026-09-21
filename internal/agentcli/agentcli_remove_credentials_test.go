package agentcli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth"
	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/domain"
	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/port"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// `fastclaw agents rm` is the second way an agent row disappears. It does
// not go through the HTTP handler, so it has to run the credential sweep
// itself — an agent whose row is gone has no reader for its stored MCP
// credentials (every call site resolves the agent first), so a missing
// sweep here leaks silently while the dashboard path stays clean. This is
// the delivery-point witness for that second path, over the real store and
// the real OAuth bootstrap.
func TestRemoveForgetsStoredMcpCredentials(t *testing.T) {
	st := freshStore(t)
	db, ok := st.(*store.DBStore)
	if !ok {
		t.Fatalf("store is %T, want *DBStore", st)
	}
	b, err := oauth.Init("agentcli-test-secret", oauth.Options{DB: db})
	if err != nil {
		t.Fatalf("oauth init: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()

	const ownerID = "u_cli_owner"
	for _, rec := range []*store.AgentRecord{
		{ID: "agt-cli-victim", UserID: ownerID, Name: "cli-victim", Config: map[string]any{}, CreatedAt: now, UpdatedAt: now},
		{ID: "agt-cli-sibling", UserID: ownerID, Name: "cli-sibling", Config: map[string]any{}, CreatedAt: now, UpdatedAt: now},
	} {
		if err := st.SaveAgent(ctx, rec); err != nil {
			t.Fatalf("save agent %s: %v", rec.ID, err)
		}
	}

	victimKey := domain.StoreKey(ownerID, "agt-cli-victim", "quandora")
	siblingKey := domain.StoreKey(ownerID, "agt-cli-sibling", "quandora")
	for _, key := range []string{victimKey, siblingKey} {
		if err := b.Tokens.Save(ctx, key, &domain.OAuthTokens{
			AccessToken: "at", RefreshToken: "rt", ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatalf("save credential %s: %v", key, err)
		}
	}

	if _, err := Remove(ctx, st, "cli-victim"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := b.Tokens.Load(ctx, victimKey); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("credential of the removed agent: Load err = %v, want ErrNotFound", err)
	}
	if _, err := b.Tokens.Load(ctx, siblingKey); err != nil {
		t.Errorf("credential of the surviving agent: Load err = %v, want it untouched", err)
	}
}
