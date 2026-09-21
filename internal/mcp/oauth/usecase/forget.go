package usecase

import (
	"context"

	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/port"
)

// ForgetIdentity drops stored credentials whose owner row is gone — the
// cleanup half of deleting an agent or a user.
//
// It removes the local credential only. The provider-side grant is
// deliberately left in place: revoking it needs a discovery round-trip
// to a third party and cannot be undone, the same ruling `mcp remove`
// follows (docs/mcp-oauth-design.md §13.6).
//
// The rows it deletes are already unreachable before it runs — every
// reader (status, servers list, token provider, refresh) resolves the
// agent row first, so a deleted agent's credentials answer 403. That
// makes this hygiene, not a correctness gate: it stops the credential
// table from growing a tail of rows nothing can ever read or name, and
// callers therefore report a failure as a warning instead of failing
// the deletion, which has already happened.
type ForgetIdentity struct {
	Tokens port.TokenStore
}

// Agent removes every server credential of one agent identity.
func (uc *ForgetIdentity) Agent(ctx context.Context, userID, agentID string) error {
	return uc.Tokens.DeleteByAgent(ctx, userID, agentID)
}

// User removes every credential of a user, across all their agents.
func (uc *ForgetIdentity) User(ctx context.Context, userID string) error {
	return uc.Tokens.DeleteByUser(ctx, userID)
}
