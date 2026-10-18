package tools

import (
	"context"
	"testing"
)

// A project session must collapse the session segment so file tools address the
// project root the dev server serves; outside a project the session segment
// stays, preserving per-chat isolation. The collapse is not decided here — it is
// workspace.WriteScope's rule (docs 10 §4 G23) — so this test pins the wiring
// between TurnScope and that rule.
func TestScopeSessionIDCollapsesInsideAProject(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	scope := &TurnScope{SessionID: "sess-123"}
	ctx := WithTurnScope(context.Background(), scope)

	if got := r.scopeSessionID(ctx); got != "sess-123" {
		t.Fatalf("loose chat: want session segment preserved, got %q", got)
	}

	scope.ProjectID = "proj-1"
	if got := r.scopeSessionID(ctx); got != "" {
		t.Fatalf("project session: want empty session segment, got %q", got)
	}

	scope.ProjectID = ""
	if got := r.scopeSessionID(ctx); got != "sess-123" {
		t.Fatalf("after disabling: want session segment restored, got %q", got)
	}
}

func TestWsPathSubdirRedirect(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	scope := &TurnScope{}
	ctx := WithTurnScope(context.Background(), scope)

	// No subdir → passthrough.
	if got := r.wsPath(ctx, "src/x.tsx"); got != "src/x.tsx" {
		t.Fatalf("no subdir: want passthrough, got %q", got)
	}

	scope.SetCodingSubdir("app")
	cases := map[string]string{
		"src/x.tsx":     "app/src/x.tsx", // bare path gets prefixed
		"app/src/x.tsx": "app/src/x.tsx", // already-prefixed is idempotent
		"/src/x.tsx":    "app/src/x.tsx", // leading slash tolerated
		"app":           "app",           // the subdir itself
		"package.json":  "app/package.json",
	}
	for in, want := range cases {
		if got := r.wsPath(ctx, in); got != want {
			t.Fatalf("wsPath(%q): want %q, got %q", in, want, got)
		}
	}

	scope.SetCodingSubdir("")
	if got := r.wsPath(ctx, "src/x.tsx"); got != "src/x.tsx" {
		t.Fatalf("after disabling: want passthrough, got %q", got)
	}
}

func TestEffectiveUserIDFallback(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	r.SetOwnerUserID("owner-1")

	if got := r.EffectiveUserID(WithTurnScope(context.Background(), &TurnScope{})); got != "owner-1" {
		t.Fatalf("no chatter: want owner fallback, got %q", got)
	}
	if got := r.EffectiveUserID(WithTurnScope(context.Background(), &TurnScope{ChatterUserID: "chatter-9"})); got != "chatter-9" {
		t.Fatalf("with chatter: want chatter, got %q", got)
	}
}
