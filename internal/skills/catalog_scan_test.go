package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCatalogSkill(t *testing.T, root, dir, manifest string, extra map[string]string) {
	t.Helper()
	base := filepath.Join(root, dir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "SKILL.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range extra {
		full := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanSkillDirsReadsRawBytesAndFrontmatter(t *testing.T) {
	layer := t.TempDir()
	writeCatalogSkill(t, layer, "refunds", "---\nname: refunds\ndescription: process refunds\n---\nbody\n",
		map[string]string{"references/GUIDE.md": "guide\n"})
	writeCatalogSkill(t, layer, "broken", "# no frontmatter\n", nil)

	found, err := ScanSkillDirs([]string{layer, filepath.Join(layer, "does-not-exist")})
	if err != nil {
		t.Fatalf("a missing layer must not be an error: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("found %d skills; want 2 (the broken one is reported, not dropped)", len(found))
	}

	catalog := BuildCatalog(found)
	if len(catalog.Skills) != 1 {
		t.Fatalf("catalog = %+v; want only refunds", catalog)
	}
	skill := catalog.Skills[0]
	if skill.Path != "refunds" || skill.Frontmatter["description"] != "process refunds" {
		t.Fatalf("skill = %+v", skill)
	}
	if len(skill.Files) != 2 {
		t.Fatalf("files = %+v; want SKILL.md and the reference", skill.Files)
	}
	// raw bytes, path relative and slash-separated
	for _, file := range skill.Files {
		if strings.Contains(file.Path, "\\") {
			t.Fatalf("path not slash-separated: %q", file.Path)
		}
	}
	if string(skill.Files[1].Content) != "guide\n" {
		t.Fatalf("content = %q; want the file's exact bytes", skill.Files[1].Content)
	}
	if len(catalog.Unpublishable) != 1 || !strings.Contains(catalog.Unpublishable[0].Reason, "no name") {
		t.Fatalf("unpublishable = %+v; want the frontmatterless skill with a reason", catalog.Unpublishable)
	}
}
