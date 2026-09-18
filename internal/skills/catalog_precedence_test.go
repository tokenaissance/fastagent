package skills

import (
	"strings"
	"testing"
)

// The runtime merges layers by name, with a later layer winning. The catalog must
// agree, or the same skill name reaches hosts twice with different digests.
func TestBuildCatalogResolvesSameNameAcrossLayers(t *testing.T) {
	low := DiscoveredSkill{Layer: "managed", DirName: "refunds",
		Frontmatter: map[string]any{"name": "refunds"}, Files: []CatalogFile{file("SKILL.md", "old")}}
	high := DiscoveredSkill{Layer: "agent", DirName: "refunds",
		Frontmatter: map[string]any{"name": "refunds"}, Files: []CatalogFile{file("SKILL.md", "new")}}

	catalog := BuildCatalog([]DiscoveredSkill{low, high})

	if len(catalog.Skills) != 1 {
		t.Fatalf("skills = %+v; want one refunds", catalog.Skills)
	}
	if got := string(catalog.Skills[0].Files[0].Content); got != "new" {
		t.Fatalf("published content = %q; want the higher layer's bytes", got)
	}
	if len(catalog.Unpublishable) != 1 ||
		!strings.Contains(catalog.Unpublishable[0].Reason, "overridden") {
		t.Fatalf("unpublishable = %+v; the overridden copy must be reported", catalog.Unpublishable)
	}
}

// Order is the only thing that decides precedence, so reversing the input must
// reverse the outcome: if it did not, something else would be ranking layers.
func TestBuildCatalogPrecedenceFollowsInputOrder(t *testing.T) {
	a := DiscoveredSkill{DirName: "refunds", Frontmatter: map[string]any{"name": "refunds"}, Files: []CatalogFile{file("SKILL.md", "first")}}
	b := DiscoveredSkill{DirName: "refunds", Frontmatter: map[string]any{"name": "refunds"}, Files: []CatalogFile{file("SKILL.md", "second")}}

	forward := BuildCatalog([]DiscoveredSkill{a, b})
	backward := BuildCatalog([]DiscoveredSkill{b, a})
	if string(forward.Skills[0].Files[0].Content) != "second" || string(backward.Skills[0].Files[0].Content) != "first" {
		t.Fatalf("precedence did not follow input order")
	}
}
