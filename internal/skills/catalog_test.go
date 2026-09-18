package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// file builds the metadata form of a file: content in, digest and size out, which
// is what travels to the cloud.
func file(path, content string) CatalogFile {
	sum := sha256.Sum256([]byte(content))
	return CatalogFile{Path: path, Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: len(content)}
}

func TestBuildCatalogPublishesOnlyMatchingNames(t *testing.T) {
	catalog := BuildCatalog([]DiscoveredSkill{
		{DirName: "refunds", Frontmatter: map[string]any{"name": "refunds"}, Files: []CatalogFile{file("SKILL.md", "body")}},
		{DirName: "pdf-tools", Frontmatter: map[string]any{"name": "pdf-processing"}, Files: []CatalogFile{file("SKILL.md", "body")}},
		{DirName: "mystery", Frontmatter: map[string]any{}, Files: []CatalogFile{file("SKILL.md", "body")}},
		{DirName: "empty", Frontmatter: map[string]any{"name": "empty"}},
	})

	if len(catalog.Skills) != 1 || catalog.Skills[0].Path != "refunds" {
		t.Fatalf("skills = %+v; want only refunds", catalog.Skills)
	}
	if len(catalog.Unpublishable) != 3 {
		t.Fatalf("unpublishable = %+v; want three problems", catalog.Unpublishable)
	}
	// Every refusal says why: that is the half of the contract the cloud cannot
	// reconstruct on its own.
	for _, problem := range catalog.Unpublishable {
		if problem.Reason == "" {
			t.Fatalf("problem for %q has no reason", problem.Path)
		}
	}
}

func TestBuildCatalogIsStableAndSortsFiles(t *testing.T) {
	build := func() Catalog {
		return BuildCatalog([]DiscoveredSkill{
			{DirName: "b", Frontmatter: map[string]any{"name": "b"}, Files: []CatalogFile{
				file("SKILL.md", "b"), file("references/GUIDE.md", "g"),
			}},
			{DirName: "a", Frontmatter: map[string]any{"name": "a"}, Files: []CatalogFile{file("SKILL.md", "a")}},
		})
	}
	first, second := build(), build()
	if strings.Join([]string{first.Skills[0].Path, first.Skills[1].Path}, ",") != "a,b" {
		t.Fatalf("order = %v; want a,b", []string{first.Skills[0].Path, first.Skills[1].Path})
	}
	if first.Skills[1].Files[0].Path != "SKILL.md" {
		t.Fatalf("files not sorted: %+v", first.Skills[1].Files)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatalf("same input produced different payloads:\n%s\n%s", a, b)
	}
}
