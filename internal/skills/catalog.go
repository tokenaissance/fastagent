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

// CatalogFile is one file of a skill: its path, the SHA-256 of its raw bytes, and
// its size. The bytes themselves do not travel here.
//
// That is a Worker constraint, not a taste: the cloud egress runs on Cloudflare
// Workers, whose per-request CPU and memory are small. Inlining every file of every
// skill into one response would make "list this agent's skills" cost as much as
// reading all of them, and a 16 MiB skill is legal. Content is fetched per file, on
// demand, which is also what lazy retrieval in the extension asks of a host.
type CatalogFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
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
	DirName string
	// Path is the skill's directory relative to its layer, slash-separated: `refunds`,
	// or `acme/refunds` for a skill nested inside another. It is what the entry's URI
	// ends in and what the file endpoint is keyed by, so the two cannot disagree about
	// which skill a request means.
	Path string
	// Layer names where the skill was found. It is carried for diagnostics only:
	// precedence is expressed by the order the caller supplies, never by comparing
	// these names, which would put the layer ranking in two places.
	Layer       string
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

	// Precedence is the caller's order: the caller supplies layers from lowest to
	// highest, exactly as the runtime merges them (a later layer overrides an
	// earlier one by name). Publishing both would put one URI on the wire with two
	// digest sets, which a host cannot reconcile - so the loser is recorded here
	// rather than dropped in silence. Comparing layer *names* to decide this is
	// deliberately avoided: the ranking would then exist in two places.
	winner := make(map[string]int, len(discovered))
	for i, skill := range discovered {
		if name, _ := skill.Frontmatter["name"].(string); name != "" && name == skill.DirName {
			winner[name] = i
		}
	}
	kept := make([]DiscoveredSkill, 0, len(discovered))
	for i, skill := range discovered {
		name, _ := skill.Frontmatter["name"].(string)
		if name != "" && name == skill.DirName && winner[name] != i {
			catalog.Unpublishable = append(catalog.Unpublishable, CatalogProblem{
				Path:   name,
				Reason: "overridden by a higher-precedence layer",
			})
			continue
		}
		kept = append(kept, skill)
	}
	discovered = kept

	for _, skill := range discovered {
		name, _ := skill.Frontmatter["name"].(string)
		switch {
		case name == "":
			catalog.Unpublishable = append(catalog.Unpublishable, CatalogProblem{
				Path: skill.Path, Reason: "frontmatter declares no name",
			})
			continue
		case name != skill.DirName:
			catalog.Unpublishable = append(catalog.Unpublishable, CatalogProblem{
				Path:   skill.Path,
				Reason: fmt.Sprintf("directory name %q does not match the declared name %q", skill.DirName, name),
			})
			continue
		case len(skill.Files) == 0:
			catalog.Unpublishable = append(catalog.Unpublishable, CatalogProblem{
				Path: skill.Path, Reason: "no files were read",
			})
			continue
		}

		files := append([]CatalogFile(nil), skill.Files...)
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		catalog.Skills = append(catalog.Skills, CatalogSkill{
			// The path, not the name: for a flat skill they are the same string, and for
			// a nested one the path is the only spelling that can be addressed (the
			// entry's URI and the file endpoint both key on it).
			Path:        skill.Path,
			Frontmatter: skill.Frontmatter,
			Files:       files,
		})
	}

	sort.Slice(catalog.Skills, func(i, j int) bool { return catalog.Skills[i].Path < catalog.Skills[j].Path })
	sort.Slice(catalog.Unpublishable, func(i, j int) bool { return catalog.Unpublishable[i].Path < catalog.Unpublishable[j].Path })
	return catalog
}
