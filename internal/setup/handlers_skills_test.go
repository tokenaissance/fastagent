package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/skills"
)

// writeSidecar is a test helper that writes a .fastagent-install.json
// sidecar file into dir, matching what writeInstallMetadata does in prod.
func writeSidecar(t *testing.T, dir, repo string) {
	t.Helper()
	if repo == "" {
		return
	}
	data := []byte(`{"repo":"` + repo + `"}`)
	if err := os.WriteFile(filepath.Join(dir, ".fastagent-install.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadInstallRepo_ReadsSidecarFile(t *testing.T) {
	dir := t.TempDir()

	// No sidecar → ("", nil)
	repo, err := skills.ReadInstallRepo(dir)
	if err != nil {
		t.Fatalf("unexpected error for dir with no sidecar: %v", err)
	}
	if repo != "" {
		t.Fatalf("expected empty repo for dir with no sidecar, got %q", repo)
	}

	writeSidecar(t, dir, "tokenaissance/skills")

	repo, err = skills.ReadInstallRepo(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo != "tokenaissance/skills" {
		t.Fatalf("expected %q, got %q", "tokenaissance/skills", repo)
	}
}

func TestReadInstallRepo_BadJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, ".fastagent-install.json"),
		[]byte("not json"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	_, err := skills.ReadInstallRepo(dir)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestScanSkillsDir_PopulatesSourceFromSidecar(t *testing.T) {
	dir := t.TempDir()

	skillDir := filepath.Join(dir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\ndescription: A test skill\n---\n# my-skill\n\nDoes things."),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writeSidecar(t, skillDir, "owner/repo")

	results := scanSkillsDir(dir)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r["name"] != "my-skill" {
		t.Errorf("name: expected %q, got %q", "my-skill", r["name"])
	}
	if r["description"] != "A test skill" {
		t.Errorf("description: expected %q, got %q", "A test skill", r["description"])
	}
	if r["source"] != "owner/repo" {
		t.Errorf("source: expected %q, got %q", "owner/repo", r["source"])
	}
}

func TestScanSkillsDir_SourceAbsentWithoutSidecar(t *testing.T) {
	dir := t.TempDir()

	skillDir := filepath.Join(dir, "no-source-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte("# no-source-skill\n\nNo sidecar here."),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	results := scanSkillsDir(dir)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if _, ok := r["source"]; ok {
		t.Errorf("source key should be absent when sidecar is missing, got %v", r["source"])
	}
}

func TestScanSkillsDir_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	results := scanSkillsDir(dir)
	if results != nil {
		t.Fatalf("expected nil for empty dir, got %v", results)
	}
}

func TestScanSkillsDir_IgnoresFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	results := scanSkillsDir(dir)
	if results != nil {
		t.Fatalf("expected nil when dir has only files, got %v", results)
	}
}
