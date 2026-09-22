package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/domain"
)

// TestExpiredCredentialWithoutRefreshTokenDemandsReauthorization pins the
// difference between two failures that used to read the same way.
//
// A provider may grant only `authorization_code` and issue no refresh
// token at all. QuantConnect's authorization server does exactly that:
// its RFC 8414 metadata declares grant_types_supported =
// ["authorization_code"] and no scopes_supported (fetched 2026-09-22,
// https://www.quantconnect.com/.well-known/oauth-authorization-server),
// and the credential it issued carries no refresh_token and expires two
// hours after the exchange. Such a credential is one-shot: when the
// access token is gone, nothing in the store can bring it back, and the
// only remedy is a fresh authorization by the owner.
//
// "no refresh token stored" names the symptom and suggests a broken
// store; the caller needs the remedy. The sentinel is what lets the MCP
// manager tell "re-authorize this server" apart from "this server is
// down" without matching on prose.
func TestExpiredCredentialWithoutRefreshTokenDemandsReauthorization(t *testing.T) {
	ctx := context.Background()
	_, _, _, provider, _, _, tokens, _, _, _ := newFlow()

	key := domain.StoreKey("u1", "a1", "quantconnect")
	if err := tokens.Save(ctx, key, &domain.OAuthTokens{
		AccessToken: "at-one-shot",
		ExpiresAt:   time.Now().UTC().Add(-time.Minute),
		Issuer:      "https://www.quantconnect.com",
	}); err != nil {
		t.Fatalf("save one-shot credential: %v", err)
	}

	_, err := provider.AccessToken(ctx, RefreshInput{
		UserID: "u1", AgentID: "a1", ServerName: "quantconnect",
		ServerURL: "https://www.quantconnect.com/api/v2/mcp",
	})
	if !errors.Is(err, domain.ErrReauthRequired) {
		t.Fatalf("error = %v, want it to match domain.ErrReauthRequired", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "re-authoriz") {
		t.Fatalf("error %q must name the remedy (re-authorize the server)", err)
	}
}

// TestValidCredentialWithoutRefreshTokenIsStillUsed guards the other half
// of the same rule: a missing refresh token says nothing about the access
// token in hand. While that one is still inside its lifetime the server is
// usable, and a client that refuses it invents an outage.
func TestValidCredentialWithoutRefreshTokenIsStillUsed(t *testing.T) {
	ctx := context.Background()
	_, _, _, provider, _, _, tokens, _, exch, _ := newFlow()

	key := domain.StoreKey("u1", "a1", "quantconnect")
	if err := tokens.Save(ctx, key, &domain.OAuthTokens{
		AccessToken: "at-one-shot",
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("save one-shot credential: %v", err)
	}

	tok, err := provider.AccessToken(ctx, RefreshInput{
		UserID: "u1", AgentID: "a1", ServerName: "quantconnect",
		ServerURL: "https://www.quantconnect.com/api/v2/mcp",
	})
	if err != nil {
		t.Fatalf("access token: %v", err)
	}
	if tok != "at-one-shot" {
		t.Fatalf("access token = %q, want the stored one", tok)
	}
	if exch.refreshCount != 0 {
		t.Fatalf("refresh calls = %d, want 0: a live access token needs no renewal", exch.refreshCount)
	}
}
