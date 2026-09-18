package tools

import "testing"

// A project session must collapse the session segment so file tools address the
// project root the dev server serves; outside a project the session segment
// stays, preserving per-chat isolation. The collapse is not decided here — it is
// workspace.WriteScope's rule (docs 10 §4 G23) — so this test pins the wiring
// between the registry's fields and that rule.
func TestScopeSessionIDCollapsesInsideAProject(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	r.SetSessionID("sess-123")

	if got := r.scopeSessionID(); got != "sess-123" {
		t.Fatalf("loose chat: want session segment preserved, got %q", got)
	}

	r.SetProjectID("proj-1")
	if got := r.scopeSessionID(); got != "" {
		t.Fatalf("project session: want empty session segment, got %q", got)
	}

	r.SetProjectID("")
	if got := r.scopeSessionID(); got != "sess-123" {
		t.Fatalf("after disabling: want session segment restored, got %q", got)
	}
}

func TestWsPathSubdirRedirect(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())

	// No subdir → passthrough.
	if got := r.wsPath("src/x.tsx"); got != "src/x.tsx" {
		t.Fatalf("no subdir: want passthrough, got %q", got)
	}

	r.SetCodingSubdir("app")
	cases := map[string]string{
		"src/x.tsx":     "app/src/x.tsx", // bare path gets prefixed
		"app/src/x.tsx": "app/src/x.tsx", // already-prefixed is idempotent
		"/src/x.tsx":    "app/src/x.tsx", // leading slash tolerated
		"app":           "app",           // the subdir itself
		"package.json":  "app/package.json",
	}
	for in, want := range cases {
		if got := r.wsPath(in); got != want {
			t.Fatalf("wsPath(%q): want %q, got %q", in, want, got)
		}
	}

	r.SetCodingSubdir("")
	if got := r.wsPath("src/x.tsx"); got != "src/x.tsx" {
		t.Fatalf("after disabling: want passthrough, got %q", got)
	}
}

func TestEffectiveUserIDFallback(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	r.SetOwnerUserID("owner-1")
	if got := r.EffectiveUserID(); got != "owner-1" {
		t.Fatalf("no chatter: want owner fallback, got %q", got)
	}
	r.SetChatterUserID("chatter-9")
	if got := r.EffectiveUserID(); got != "chatter-9" {
		t.Fatalf("with chatter: want chatter, got %q", got)
	}
}
