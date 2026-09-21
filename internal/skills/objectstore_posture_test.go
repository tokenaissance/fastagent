package skills

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// The skills-publish posture (change list B10 / obligation L7): a publish is an
// intentional re-publish, so it READS the version it is replacing and conditions
// the write on it — a concurrent publisher loses the race instead of leaving
// half of one skill tree next to half of another.
//
// The difference between the two postures is the whole point, and it is exactly
// what a shared helper would erase: attachments must NOT overwrite
// (`VersionAbsent`), a skill publish must (read-modify-write).
//
// Falsification (run for real): replace the expectation with `VersionAbsent`
// and the re-publish case below goes red — the second publish of a skill could
// not land at all.

type skillStore struct {
	mu       sync.Mutex
	versions map[string]string
	writes   map[string]string
	// Every write's expectation, so the test can read the posture instead of
	// inferring it from the outcome.
	expectations map[string][]workspace.Version
	conflictOn   map[string]bool
}

func newSkillStore(seed map[string]string) *skillStore {
	return &skillStore{
		versions:     seed,
		writes:       map[string]string{},
		expectations: map[string][]workspace.Version{},
		conflictOn:   map[string]bool{},
	}
}

func (s *skillStore) PutIfVersion(_ context.Context, _ /*agent*/, _ /*project*/, _ /*session*/, path string, r io.Reader, _ int64, _ string, expected workspace.Version) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expectations[path] = append(s.expectations[path], expected)
	if s.conflictOn[path] {
		return workspace.ErrVersionConflict
	}
	if current, exists := s.versions[path]; exists {
		if expected == workspace.VersionAbsent {
			return workspace.ErrVersionConflict
		}
		if expected != workspace.Version(current) {
			return workspace.ErrVersionConflict
		}
	} else if expected != workspace.VersionAbsent {
		return workspace.ErrVersionConflict
	}
	s.versions[path] = string(body)
	s.writes[path] = string(body)
	return nil
}

func (s *skillStore) Put(ctx context.Context, agentID, projectID, sessionID, path string, r io.Reader, size int64, contentType string) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versions[path] = string(body)
	s.writes[path] = string(body)
	return nil
}

func (s *skillStore) Get(_ context.Context, _, _, _, path string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.versions[path]
	if !ok {
		return nil, workspace.ErrNotFound
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func (s *skillStore) Stat(_ context.Context, _, _, _, path string) (*workspace.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.versions[path]
	if !ok {
		return nil, workspace.ErrNotFound
	}
	return &workspace.ObjectInfo{Path: path, Size: int64(len(body)), Version: workspace.Version(body)}, nil
}

func (s *skillStore) List(context.Context, string, string, string) ([]workspace.ObjectInfo, error) {
	return nil, nil
}
func (s *skillStore) Delete(context.Context, string, string, string, string) error { return nil }
func (s *skillStore) Move(context.Context, string, string, string, string, string) error {
	return nil
}
func (s *skillStore) SignedURL(context.Context, string, string, string, string, time.Duration) (string, error) {
	return "", nil
}

func writeSkillFile(t *testing.T, root, name, file, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestSkillPublishConditionsOnTheVersionItReplace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeSkillFile(t, root, "demo", "SKILL.md", "v1")

	key := buildKey("demo", "SKILL.md")
	store := newSkillStore(map[string]string{key: "v0"})

	if err := SyncSkillUp(ctx, store, "ag", "demo", root); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Read-modify-write: the first attempt carried the version that was there
	// ("v0"), not a create-only precondition — a skill publish is meant to land.
	if got := store.expectations[key]; len(got) != 1 || got[0] != workspace.Version("v0") {
		t.Fatalf("expectations = %v; want one write conditioned on the live version v0", got)
	}
	if got := store.writes[key]; got != "v1" {
		t.Fatalf("stored %q; want the published content", got)
	}

	// Publish again: the expectation follows the version that now exists.
	writeSkillFile(t, root, "demo", "SKILL.md", "v2")
	if err := SyncSkillUp(ctx, store, "ag", "demo", root); err != nil {
		t.Fatalf("re-publish: %v", err)
	}
	got := store.expectations[key]
	if len(got) != 2 || got[1] != workspace.Version("v1") {
		t.Fatalf("expectations = %v; want the second write conditioned on v1", got)
	}
}

func TestSkillPublishRefusesWhenTheVersionMoved(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeSkillFile(t, root, "demo", "SKILL.md", "v1")

	key := buildKey("demo", "SKILL.md")
	store := newSkillStore(map[string]string{key: "v0"})
	store.conflictOn[key] = true

	err := SyncSkillUp(ctx, store, "ag", "demo", root)
	if err == nil {
		t.Fatal("a publish whose version moved was reported as successful")
	}
	if !strings.Contains(err.Error(), "another publisher changed it") {
		t.Fatalf("error = %v; want the concurrent-publisher explanation", err)
	}
	if _, landed := store.writes[key]; landed {
		t.Fatal("the refused publish wrote anyway")
	}
	if !errors.Is(errors.Unwrap(err), workspace.ErrVersionConflict) && !strings.Contains(err.Error(), "re-run the publish") {
		t.Fatalf("error = %v; want it to name the conflict", err)
	}
}
