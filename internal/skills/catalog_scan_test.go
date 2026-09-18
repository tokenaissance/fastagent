package skills

import (
	"os"
	"path/filepath"
	"sort"
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

// A skill inside another skill is a skill of its own: the extension says the enclosing
// path becomes its organizational prefix, and that the nested files remain ordinary
// supporting files of the enclosing skill. Both statements are testable here, and the
// second one is why the outer entry keeps listing the nested SKILL.md.
func TestScanSkillDirsPublishesNestedSkillsUnderTheirPrefix(t *testing.T) {
	layer := t.TempDir()
	writeCatalogSkill(t, layer, "acme", "---\nname: acme\n---\nouter\n", nil)
	writeCatalogSkill(t, layer, "acme/refunds", "---\nname: refunds\n---\ninner\n",
		map[string]string{"notes.md": "notes\n"})

	found, err := ScanSkillDirs([]string{layer})
	if err != nil {
		t.Fatal(err)
	}

	paths := make([]string, 0, len(found))
	for _, skill := range found {
		paths = append(paths, skill.Path)
	}
	sort.Strings(paths)
	if strings.Join(paths, ",") != "acme,acme/refunds" {
		t.Fatalf("paths = %v; want the enclosing skill and the nested one under its prefix", paths)
	}

	catalog := BuildCatalog(found)
	if len(catalog.Skills) != 2 || len(catalog.Unpublishable) != 0 {
		t.Fatalf("catalog = %+v; want both published", catalog)
	}
	if catalog.Skills[0].Path != "acme" || catalog.Skills[1].Path != "acme/refunds" {
		t.Fatalf("published paths = %q, %q", catalog.Skills[0].Path, catalog.Skills[1].Path)
	}

	// The enclosing skill still carries the nested manifest as supporting content.
	outer := catalog.Skills[0]
	var sawNestedManifest bool
	for _, f := range outer.Files {
		if f.Path == "refunds/SKILL.md" {
			sawNestedManifest = true
		}
	}
	if !sawNestedManifest {
		t.Fatalf("outer files = %+v; the nested manifest must still be supporting content", outer.Files)
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
	if want := file("references/GUIDE.md", "guide\n"); skill.Files[1].Digest != want.Digest || skill.Files[1].Size != want.Size {
		t.Fatalf("file = %+v; want the digest and size of the bytes on disk", skill.Files[1])
	}
	if len(catalog.Unpublishable) != 1 || !strings.Contains(catalog.Unpublishable[0].Reason, "no name") {
		t.Fatalf("unpublishable = %+v; want the frontmatterless skill with a reason", catalog.Unpublishable)
	}
}
