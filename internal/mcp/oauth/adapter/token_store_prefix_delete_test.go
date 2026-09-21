package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/domain"
	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/port"
)

// The delete edges (agent deletion, user deletion) sweep by key prefix.
// Two properties make that safe, and both are asserted here because both
// have a way to go wrong silently:
//
//  1. Scope — the sweep takes every server of the named identity and
//     nothing else. A too-wide sweep destroys authorizations the user
//     must then redo; a too-narrow one leaves the leak this change is
//     about. Sibling agents and other users are the oracles.
//  2. Escaping — the IDs are PathEscaped, which leaves "_" alone, and
//     "_" is a LIKE wildcard. "u_1" would then also match "uX1": deleting
//     one user's agent would delete a different user's credentials. The
//     look-alike keys below are the falsifier for that.
func seedCredentials(t *testing.T, s port.TokenStore, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if err := s.Save(context.Background(), k, &domain.OAuthTokens{
			AccessToken: "at-" + k, RefreshToken: "rt", ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
}

func mustExist(t *testing.T, s port.TokenStore, key string) {
	t.Helper()
	if _, err := s.Load(context.Background(), key); err != nil {
		t.Fatalf("credential %s should still exist: %v", key, err)
	}
}

func mustBeGone(t *testing.T, s port.TokenStore, key string) {
	t.Helper()
	if _, err := s.Load(context.Background(), key); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("credential %s should be gone, Load returned err=%v", key, err)
	}
}

func TestDBTokenStoreDeleteByAgentScopedAndEscaped(t *testing.T) {
	a, _ := openSharedDB(t)
	s := &DBTokenStore{DB: a.DB(), Dialect: a.Dialect(), Crypt: newCrypt(t)}
	ctx := context.Background()

	victim1 := domain.StoreKey("u_1", "agent_1", "quandora")
	victim2 := domain.StoreKey("u_1", "agent_1", "notion")
	sibling := domain.StoreKey("u_1", "agent_2", "quandora")
	lookalikeUser := domain.StoreKey("uX1", "agent_1", "quandora")
	lookalikeAgent := domain.StoreKey("u_1", "agentX1", "quandora")
	seedCredentials(t, s, victim1, victim2, sibling, lookalikeUser, lookalikeAgent)

	if err := s.DeleteByAgent(ctx, "u_1", "agent_1"); err != nil {
		t.Fatalf("DeleteByAgent: %v", err)
	}

	mustBeGone(t, s, victim1)
	mustBeGone(t, s, victim2)
	mustExist(t, s, sibling)
	mustExist(t, s, lookalikeUser)
	mustExist(t, s, lookalikeAgent)
}

func TestDBTokenStoreDeleteByUserScopedAndEscaped(t *testing.T) {
	a, _ := openSharedDB(t)
	s := &DBTokenStore{DB: a.DB(), Dialect: a.Dialect(), Crypt: newCrypt(t)}
	ctx := context.Background()

	first := domain.StoreKey("u_1", "a1", "quandora")
	second := domain.StoreKey("u_1", "a2", "notion")
	lookalike := domain.StoreKey("uX1", "a1", "quandora")
	seedCredentials(t, s, first, second, lookalike)

	if err := s.DeleteByUser(ctx, "u_1"); err != nil {
		t.Fatalf("DeleteByUser: %v", err)
	}

	mustBeGone(t, s, first)
	mustBeGone(t, s, second)
	mustExist(t, s, lookalike)
}

// The file store is the no-DB fallback; it deletes directories instead of
// rows, so an empty ID would widen the sweep to the whole user (or, one
// level up, to every user) rather than matching nothing. It must refuse.
func TestFileTokenStorePrefixDeletes(t *testing.T) {
	root := t.TempDir()
	s := &FileTokenStore{Root: root, Crypt: newCrypt(t)}
	ctx := context.Background()

	victim := domain.StoreKey("u_1", "agent_1", "quandora")
	sibling := domain.StoreKey("u_1", "agent_2", "quandora")
	lookalike := domain.StoreKey("uX1", "agent_1", "quandora")
	seedCredentials(t, s, victim, sibling, lookalike)

	if err := s.DeleteByAgent(ctx, "u_1", "agent_1"); err != nil {
		t.Fatalf("DeleteByAgent: %v", err)
	}
	mustBeGone(t, s, victim)
	mustExist(t, s, sibling)
	mustExist(t, s, lookalike)

	// A half-specified identity must not silently become the parent scope.
	if err := s.DeleteByAgent(ctx, "u_1", ""); err == nil {
		t.Fatal("DeleteByAgent with an empty agentID should refuse")
	}
	if err := s.DeleteByUser(ctx, ""); err == nil {
		t.Fatal("DeleteByUser with an empty userID should refuse")
	}
	if _, err := os.Stat(filepath.Join(root, "oauth", "u_1", "agent_2", "quandora.json")); err != nil {
		t.Fatalf("the refused sweeps must not have deleted anything: %v", err)
	}

	if err := s.DeleteByUser(ctx, "u_1"); err != nil {
		t.Fatalf("DeleteByUser: %v", err)
	}
	mustBeGone(t, s, sibling)
	mustExist(t, s, lookalike)
}
