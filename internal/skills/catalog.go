package skills

import (
	"fmt"
	"sort"
)

// CatalogSkill is one skill as the cloud MCP egress needs it: the path it must be
// served under, the frontmatter verbatim, and every file's raw bytes.
//
// Raw bytes, not parsed content, because the served entry carries a digest over
// exactly these bytes — any transformation here (substituting {baseDir}, trimming,
// normalising line endings) would make every host's verification fail.
//
// The path is computed here, not by the caller, because the layout rules live in
// this package (layout.go): a second copy of "where a skill lives and what it is
// called" on the cloud side is the defect the workspace formal record names.
type CatalogSkill struct {
	Path        string         `json:"path"`
	Frontmatter map[string]any `json:"frontmatter"`
	Files       []CatalogFile  `json:"files"`
}

// CatalogFile is one file of a skill. Encoding []byte in JSON yields base64, which
// is what the cloud side decodes before hashing.
type CatalogFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

// CatalogProblem records a skill that exists but cannot be published, and why.
//
// This half of the payload is the point of the contract: without it the cloud can
// only say "no such skill", and the extension forbids hosts from concluding that
// from an absent or partial listing. A skill whose directory name disagrees with
// its frontmatter name lands here rather than being silently renamed at read time.
type CatalogProblem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Catalog is the whole answer of the internal endpoint the cloud consumes.
type Catalog struct {
	Skills        []CatalogSkill   `json:"skills"`
	Unpublishable []CatalogProblem `json:"unpublishable"`
}

// DiscoveredSkill is what a layer scan found: the directory it lives in, the name
// its SKILL.md declares, the parsed frontmatter, and its files' raw bytes.
type DiscoveredSkill struct {
	DirName     string
	Frontmatter map[string]any
	Files       []CatalogFile
}

// BuildCatalog applies the publishability rule — the served path's final segment
// is the declared name, so a skill whose directory and frontmatter disagree cannot
// be served under either name without lying to the host.
//
// It sorts by path so two calls over the same disk produce byte-identical answers;
// a host that sees the order shift may read it as a content change.
func BuildCatalog(discovered []DiscoveredSkill) Catalog {
	catalog := Catalog{Skills: []CatalogSkill{}, Unpublishable: []CatalogProblem{}}

	for _, skill := range discovered {
		name, _ := skill.Frontmatter["name"].(string)
		switch {
		case name == "":
			catalog.Unpublishable = append(catalog.Unpublishable, CatalogProblem{
				Path: skill.DirName, Reason: "frontmatter declares no name",
			})
			continue
		case name != skill.DirName:
			catalog.Unpublishable = append(catalog.Unpublishable, CatalogProblem{
				Path:   skill.DirName,
				Reason: fmt.Sprintf("directory name %q does not match the declared name %q", skill.DirName, name),
			})
			continue
		case len(skill.Files) == 0:
			catalog.Unpublishable = append(catalog.Unpublishable, CatalogProblem{
				Path: skill.DirName, Reason: "no files were read",
			})
			continue
		}

		files := append([]CatalogFile(nil), skill.Files...)
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		catalog.Skills = append(catalog.Skills, CatalogSkill{
			Path:        name,
			Frontmatter: skill.Frontmatter,
			Files:       files,
		})
	}

	sort.Slice(catalog.Skills, func(i, j int) bool { return catalog.Skills[i].Path < catalog.Skills[j].Path })
	sort.Slice(catalog.Unpublishable, func(i, j int) bool { return catalog.Unpublishable[i].Path < catalog.Unpublishable[j].Path })
	return catalog
}
