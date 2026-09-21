package skills

import (
	"fmt"
	"sort"
)

// The stable codes that travel with a problem or a warning.
//
// A consumer that shows these to a person needs to say them in that person's
// language, and matching on the human sentence would break the next time someone
// improves the wording. The sentence still travels: it is what a caller without a
// translation table (a log, a script, a CLI) shows as-is.
const (
	CodeShadowedByLayer = "shadowed_by_layer"
	CodeNoName          = "no_name"
	CodeNameMismatch    = "name_mismatch"
	CodeNoFiles         = "no_files"
	CodeBaseDirToken    = "base_dir_token"
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
	// Code is the same fact as Reason, in a form a caller can switch on. The reason
	// is written for a human reading the pod's logs; a product that shows it to its
	// own users has to be able to say it in their language, and matching on prose
	// would break the moment someone improves the wording.
	Code string `json:"code"`
}

// CatalogWarning records a skill that IS published, but whose meaning changes on the
// way out.
//
// Separate from CatalogProblem on purpose: "we refused to publish this" and "we
// published this and it will not behave the way you wrote it" are two different
// states, and a single list would make them look like one.
type CatalogWarning struct {
	Path   string   `json:"path"`
	Code   string   `json:"code"`
	Reason string   `json:"reason"`
	Files  []string `json:"files,omitempty"`
}

// Catalog is the whole answer of the internal endpoint the cloud consumes.
type Catalog struct {
	Skills        []CatalogSkill   `json:"skills"`
	Unpublishable []CatalogProblem `json:"unpublishable"`
	Warnings      []CatalogWarning `json:"warnings"`
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
	// BaseDirFiles names the skill's files whose text carries the literal
	// `{baseDir}`. Populated by the scan, which is the only place that has the bytes.
	BaseDirFiles []string
}

// BuildCatalog applies the publishability rule — the served path's final segment
// is the declared name, so a skill whose directory and frontmatter disagree cannot
// be served under either name without lying to the host.
//
// It sorts by path so two calls over the same disk produce byte-identical answers;
// a host that sees the order shift may read it as a content change. That promise is
// about all three lists, including the warnings: they are built in scan order, and
// scan order is (layer, then lexical within a layer), which is not path order once
// there is more than one layer.
func BuildCatalog(discovered []DiscoveredSkill) Catalog {
	catalog := Catalog{Skills: []CatalogSkill{}, Unpublishable: []CatalogProblem{}, Warnings: []CatalogWarning{}}

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
				// The path, not the name: it is the skill's identity from the layer to the
				// wire, the same string the published entry carries — and for a nested skill
				// the leaf name is not unique (`a/refunds` and `b/refunds` are two skills).
				// The name remains the fallback for a caller that supplied no path, so the
				// diagnostic is never empty.
				Path:   shadowedPath(skill, name),
				Reason: "overridden by a higher-precedence layer",
				Code:   CodeShadowedByLayer,
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
				Path: skill.Path, Reason: "frontmatter declares no name", Code: CodeNoName,
			})
			continue
		case name != skill.DirName:
			catalog.Unpublishable = append(catalog.Unpublishable, CatalogProblem{
				Path:   skill.Path,
				Reason: fmt.Sprintf("directory name %q does not match the declared name %q", skill.DirName, name),
				Code:   CodeNameMismatch,
			})
			continue
		case len(skill.Files) == 0:
			catalog.Unpublishable = append(catalog.Unpublishable, CatalogProblem{
				Path: skill.Path, Reason: "no files were read", Code: CodeNoFiles,
			})
			continue
		}

		// Published, but part of its text will not mean what the author wrote: the
		// runtime substitutes {baseDir} for the agent's own use, and this endpoint
		// cannot (the digest covers the bytes as they are).
		if len(skill.BaseDirFiles) > 0 {
			catalog.Warnings = append(catalog.Warnings, CatalogWarning{
				Path:   skill.Path,
				Code:   CodeBaseDirToken,
				Reason: "{baseDir} is replaced when this agent loads the skill, but not over MCP: a connected client sees the literal text",
				Files:  append([]string(nil), skill.BaseDirFiles...),
			})
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
	sort.Slice(catalog.Warnings, func(i, j int) bool { return catalog.Warnings[i].Path < catalog.Warnings[j].Path })
	return catalog
}

// shadowedPath names a copy that lost the layer precedence. The path is what the
// entry would have been served under, so it is what a reader can look for in the
// list; the declared name is only a fallback for input that carries no path.
func shadowedPath(skill DiscoveredSkill, name string) string {
	if skill.Path != "" {
		return skill.Path
	}
	return name
}
