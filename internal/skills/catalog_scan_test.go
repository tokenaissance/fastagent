package skills

import (
	"crypto/sha256"
	"encoding/hex"
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

// A skill that leans on {baseDir} is published, and the diagnostic says so: the
// runtime substitutes the token when this agent loads the skill, but the MCP egress
// cannot (the digest covers the bytes as they are), so a connected client reads the
// literal text. Refusing the skill instead would break the use that works.
func TestScanAndCatalogReportTheBaseDirTokenAsAWarning(t *testing.T) {
	layer := t.TempDir()
	writeCatalogSkill(t, layer, "runner", "---\nname: runner\n---\n\nRun {baseDir}/scripts/go.sh\n",
		map[string]string{"scripts/go.sh": "echo hi\n"})
	writeCatalogSkill(t, layer, "plain", "---\nname: plain\n---\n\nNothing to substitute.\n", nil)
	// A skill that is refused anyway must not also collect a warning: the refusal is
	// the actionable fact, and two messages for one skill is noise.
	writeCatalogSkill(t, layer, "orphan-dir", "---\nname: orphan\n---\n\n{baseDir}\n", nil)

	found, err := ScanSkillDirs([]string{layer})
	if err != nil {
		t.Fatal(err)
	}
	catalog := BuildCatalog(found)

	if len(catalog.Warnings) != 1 {
		t.Fatalf("warnings = %+v; want exactly one (the published skill that uses the token)", catalog.Warnings)
	}
	warning := catalog.Warnings[0]
	if warning.Path != "runner" || warning.Code != CodeBaseDirToken {
		t.Fatalf("warning = %+v", warning)
	}
	if len(warning.Files) != 1 || warning.Files[0] != "SKILL.md" {
		t.Fatalf("warning files = %v; want SKILL.md", warning.Files)
	}
	// The sentence that travels with the code has to name where the substitution
	// happens: it is localized by code on the cloud side and read verbatim by anyone
	// without that table, and it is the only part of this warning a reader of the MCP
	// answer sees beside "a client sees something else". A sentence that says the
	// substitution covers the whole skill is the false half of this fact.
	if !strings.Contains(warning.Reason, "SKILL.md") {
		t.Fatalf("reason = %q; it must say the substitution happens in SKILL.md, not across the skill", warning.Reason)
	}

	// The bytes stay raw, and the digest still describes them: the warning is the
	// only thing that changes.
	var runner DiscoveredSkill
	for _, skill := range found {
		if skill.Path == "runner" {
			runner = skill
		}
	}
	if len(runner.Files) != 2 {
		t.Fatalf("runner files = %+v", runner.Files)
	}
	for _, f := range runner.Files {
		if f.Path == "SKILL.md" {
			want := sha256.Sum256([]byte("---\nname: runner\n---\n\nRun {baseDir}/scripts/go.sh\n"))
			if f.Digest != "sha256:"+hex.EncodeToString(want[:]) {
				t.Fatalf("digest = %q; the scan must hash the raw bytes it hands out", f.Digest)
			}
		}
	}
}

// The warning states a difference between two readers, so it fires on the file where
// that difference exists. `{baseDir}` in a bundled script is substituted by nobody —
// this agent reads the literal token there too — so announcing "a client reads
// something else" for it would be a statement no reader can act on, delivered to both
// consumers (the dashboard panel and the MCP answer). Refusing to say it is not the
// same as not knowing it: the scan still reports the carrier, which is what the file
// list of a manifest warning is for.
func TestBaseDirTokenInABundledFileIsNotAReaderDifference(t *testing.T) {
	layer := t.TempDir()
	writeCatalogSkill(t, layer, "runner", "---\nname: runner\n---\n\nRun the bundled script.\n",
		map[string]string{"scripts/run.sh": "set -e\necho {baseDir}/go.sh\n"})

	found, err := ScanSkillDirs([]string{layer})
	if err != nil {
		t.Fatal(err)
	}
	catalog := BuildCatalog(found)

	if len(catalog.Skills) != 1 || catalog.Skills[0].Path != "runner" {
		t.Fatalf("catalog = %+v; the skill is published — the token is not a refusal either", catalog)
	}
	if len(catalog.Warnings) != 0 {
		t.Fatalf("warnings = %+v; a token only a script carries is solved by no reader, so there is no difference to announce",
			catalog.Warnings)
	}
	for _, skill := range found {
		if skill.Path != "runner" {
			continue
		}
		if len(skill.BaseDirFiles) != 1 || skill.BaseDirFiles[0] != "scripts/run.sh" {
			t.Fatalf("BaseDirFiles = %v; the scan must still measure every carrier", skill.BaseDirFiles)
		}
	}
}

// A manifest warning lists every carrier, not only the manifest, and it does so in the
// same answer: the sentence "in every file listed" is only checkable if the list is the
// measurement rather than the trigger.
func TestBaseDirWarningListsEveryCarrierNotOnlyTheManifest(t *testing.T) {
	layer := t.TempDir()
	writeCatalogSkill(t, layer, "runner", "---\nname: runner\n---\n\nRun {baseDir}/scripts/go.sh\n",
		map[string]string{"scripts/go.sh": "echo {baseDir}/helper.sh\n"})

	found, err := ScanSkillDirs([]string{layer})
	if err != nil {
		t.Fatal(err)
	}
	catalog := BuildCatalog(found)
	if len(catalog.Warnings) != 1 {
		t.Fatalf("warnings = %+v; want one", catalog.Warnings)
	}
	files := catalog.Warnings[0].Files
	var sawManifest, sawScript bool
	for _, f := range files {
		sawManifest = sawManifest || f == "SKILL.md"
		sawScript = sawScript || f == "scripts/go.sh"
	}
	if !sawManifest || !sawScript {
		t.Fatalf("warning files = %v; want both carriers", files)
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
