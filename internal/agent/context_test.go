package agent

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// fakeMemoryStore is a minimal MemoryStore that backs onto a per-(user,
// agent, file) map. Lets tests exercise platform-fallback wiring without
// pulling in DBStore + Postgres.
type fakeMemoryStore struct {
	files map[string][]byte
}

func newFakeStore() *fakeMemoryStore {
	return &fakeMemoryStore{files: map[string][]byte{}}
}

func key(uid, agentID, filename string) string {
	return uid + "|" + agentID + "|" + filename
}

func (s *fakeMemoryStore) GetMemory(ctx context.Context, agentID string) (string, error) {
	uid := config.UserIDFromContext(ctx)
	if uid == "" {
		uid = config.DefaultUserID
	}
	return string(s.files[key(uid, agentID, "MEMORY.md")]), nil
}

func (s *fakeMemoryStore) SaveMemory(ctx context.Context, agentID, content string) error {
	uid := config.UserIDFromContext(ctx)
	if uid == "" {
		uid = config.DefaultUserID
	}
	s.files[key(uid, agentID, "MEMORY.md")] = []byte(content)
	return nil
}

func (s *fakeMemoryStore) GetWorkspaceFile(ctx context.Context, agentID, filename string) ([]byte, error) {
	uid := config.UserIDFromContext(ctx)
	if uid == "" {
		uid = config.DefaultUserID
	}
	if data, ok := s.files[key(uid, agentID, filename)]; ok {
		return data, nil
	}
	return nil, nil
}

func (s *fakeMemoryStore) SaveWorkspaceFile(ctx context.Context, agentID, filename string, data []byte) error {
	uid := config.UserIDFromContext(ctx)
	if uid == "" {
		uid = config.DefaultUserID
	}
	s.files[key(uid, agentID, filename)] = data
	return nil
}

// seedPlatform writes a file under the platform agent for inheritance tests.
func (s *fakeMemoryStore) seedPlatform(filename, content string) {
	s.files[key(config.DefaultUserID, PlatformAgentID, filename)] = []byte(content)
}

func newTestBuilder(store *fakeMemoryStore, userID, agentID string) *ContextBuilder {
	return &ContextBuilder{
		store:   store,
		userID:  userID,
		agentID: agentID,
	}
}

func newTestBuilderWithTemplate(store *fakeMemoryStore, userID, agentID, templateID string) *ContextBuilder {
	cb := newTestBuilder(store, userID, agentID)
	cb.templateID = templateID
	return cb
}

// seedTemplate writes a file under a template agent (templates ARE agents,
// stored at user_id=DefaultUserID). Mirrors how an admin or app would set
// up ThinkAny's per-function templates.
func (s *fakeMemoryStore) seedTemplate(templateID, filename, content string) {
	s.files[key(config.DefaultUserID, templateID, filename)] = []byte(content)
}

func TestLoadFile_PerAgentBeatsPlatform(t *testing.T) {
	store := newFakeStore()
	store.seedPlatform("SOUL.md", "platform soul")
	ctx := config.WithUserID(context.Background(), "alice")
	store.SaveWorkspaceFile(ctx, "agent-x", "SOUL.md", []byte("alice override"))

	got := newTestBuilder(store, "alice", "agent-x").loadFile("SOUL.md")
	if got != "alice override" {
		t.Fatalf("expected per-agent override to win, got %q", got)
	}
}

func TestLoadFile_FallsBackToPlatformWhenPerAgentEmpty(t *testing.T) {
	store := newFakeStore()
	store.seedPlatform("SOUL.md", "platform soul")
	// per-agent row exists but is empty (handleCreateAgent writes nil/empty)
	ctx := config.WithUserID(context.Background(), "alice")
	store.SaveWorkspaceFile(ctx, "agent-x", "SOUL.md", []byte(""))

	got := newTestBuilder(store, "alice", "agent-x").loadFile("SOUL.md")
	if got != "platform soul" {
		t.Fatalf("expected platform fallback, got %q", got)
	}
}

func TestLoadFile_FallsBackToPlatformWhenPerAgentMissing(t *testing.T) {
	store := newFakeStore()
	store.seedPlatform("IDENTITY.md", "platform identity")
	// no per-agent row at all

	got := newTestBuilder(store, "alice", "agent-x").loadFile("IDENTITY.md")
	if got != "platform identity" {
		t.Fatalf("expected platform fallback, got %q", got)
	}
}

func TestLoadFile_NonInheritableNeverFallsBack(t *testing.T) {
	store := newFakeStore()
	// USER.md is per-(user, agent) only — should NOT inherit from platform
	// even when seeded there. A second user reading agent-x's USER.md
	// must not see whatever a platform admin happened to put there.
	store.seedPlatform("USER.md", "leaked profile")

	got := newTestBuilder(store, "alice", "agent-x").loadFile("USER.md")
	if got != "" {
		t.Fatalf("USER.md must not inherit; got %q", got)
	}
}

func TestLoadFile_PlatformIsScopedToDefaultUser(t *testing.T) {
	store := newFakeStore()
	store.seedPlatform("SOUL.md", "the real platform soul")
	// Even if user "alice" somehow had a row at (alice, __platform__, SOUL.md),
	// the platform read should target (DefaultUserID, __platform__, ...).
	ctx := config.WithUserID(context.Background(), "alice")
	store.SaveWorkspaceFile(ctx, PlatformAgentID, "SOUL.md", []byte("alice's fake platform"))

	got := newTestBuilder(store, "alice", "agent-x").loadFile("SOUL.md")
	if got != "the real platform soul" {
		t.Fatalf("platform read crossed user scope, got %q", got)
	}
}

// --- template inheritance (#8) ---

func TestLoadFile_FallsBackToTemplateBeforePlatform(t *testing.T) {
	store := newFakeStore()
	store.seedPlatform("SOUL.md", "platform soul")
	store.seedTemplate("translator-tmpl", "SOUL.md", "translator template soul")
	// per-agent SOUL is empty/missing, but templateID is set

	got := newTestBuilderWithTemplate(store, "alice", "alice-translator", "translator-tmpl").
		loadFile("SOUL.md")
	if got != "translator template soul" {
		t.Fatalf("expected template fallback before platform, got %q", got)
	}
}

func TestLoadFile_PerAgentBeatsTemplateAndPlatform(t *testing.T) {
	store := newFakeStore()
	store.seedPlatform("SOUL.md", "platform soul")
	store.seedTemplate("translator-tmpl", "SOUL.md", "translator template")
	ctx := config.WithUserID(context.Background(), "alice")
	store.SaveWorkspaceFile(ctx, "alice-translator", "SOUL.md", []byte("alice override"))

	got := newTestBuilderWithTemplate(store, "alice", "alice-translator", "translator-tmpl").
		loadFile("SOUL.md")
	if got != "alice override" {
		t.Fatalf("per-agent override should still win, got %q", got)
	}
}

func TestLoadFile_TemplateMissesFallsToPlatform(t *testing.T) {
	store := newFakeStore()
	store.seedPlatform("IDENTITY.md", "platform identity")
	// templateID set, but template has no IDENTITY.md row — should
	// fall through to the platform layer rather than returning empty.

	got := newTestBuilderWithTemplate(store, "alice", "alice-x", "missing-tmpl").
		loadFile("IDENTITY.md")
	if got != "platform identity" {
		t.Fatalf("expected platform fallback when template lacks the file, got %q", got)
	}
}

func TestLoadFile_TemplateNotConsultedForNonInheritable(t *testing.T) {
	store := newFakeStore()
	// Even if a template happened to seed USER.md (it shouldn't, but be
	// defensive), per-(user, agent) state must never inherit from a
	// platform-shared row.
	store.seedTemplate("evil-tmpl", "USER.md", "leaked profile from template")

	got := newTestBuilderWithTemplate(store, "alice", "alice-x", "evil-tmpl").
		loadFile("USER.md")
	if got != "" {
		t.Fatalf("USER.md must not inherit from template, got %q", got)
	}
}
