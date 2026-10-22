package tools

import (
	"context"
	"testing"
)

func TestAuthorizeJobAccess(t *testing.T) {
	owned := WithTurnScope(context.Background(), &TurnScope{SessionID: "s1", ChatterUserID: "u1"})
	otherSession := WithTurnScope(context.Background(), &TurnScope{SessionID: "s2", ChatterUserID: "u1"})
	otherChatter := WithTurnScope(context.Background(), &TurnScope{SessionID: "s1", ChatterUserID: "u2"})

	tests := []struct {
		name           string
		ctx            context.Context
		ownerSessionID string
		ownerChatterID string
		wantErr        bool
	}{
		{name: "legacy job without owner stays readable", ctx: context.Background()},
		{name: "scope missing is refused for an owned job", ctx: context.Background(), ownerSessionID: "s1", wantErr: true},
		{name: "same session and chatter is allowed", ctx: owned, ownerSessionID: "s1", ownerChatterID: "u1"},
		{name: "session mismatch is refused", ctx: otherSession, ownerSessionID: "s1", ownerChatterID: "u1", wantErr: true},
		{name: "chatter mismatch is refused", ctx: otherChatter, ownerSessionID: "s1", ownerChatterID: "u1", wantErr: true},
		{name: "session-only owner ignores chatter", ctx: otherChatter, ownerSessionID: "s1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := authorizeJobAccess(tt.ctx, tt.ownerSessionID, tt.ownerChatterID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("authorizeJobAccess() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestShellManagerStampsOwner(t *testing.T) {
	m := newShellManager()
	s, err := m.StartOwned("true", nil, "s1", "u1")
	if err != nil {
		t.Fatalf("StartOwned: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.ownerSessionID != "s1" || s.ownerChatterID != "u1" {
		t.Fatalf("owner = (%q, %q), want (s1, u1)", s.ownerSessionID, s.ownerChatterID)
	}
}
