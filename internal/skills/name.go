package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// A skill has two candidate names: the directory it sits in, and the `name` its
// author wrote in SKILL.md frontmatter. The directory name comes from the
// install *request* (a ClawHub slug, a repo name, a zip's top folder); the
// frontmatter name comes from the author. They drift, and today the runtime
// indexes by directory while the Agent Skills spec — and therefore the
// Skills-over-MCP extension, whose URIs must end in the frontmatter name —
// indexes by frontmatter.
//
// This file makes the frontmatter name normative at ingest, where the fix is
// cheap: the directory is renamed to match, before anything mirrors the skill to
// the object store (the store key is built from the name, so renaming later
// would leave two names in play). Nothing about the file bytes changes, so
// digests, relative references and sidecar files travel with the directory.

// skillNameRE is the Agent Skills naming rule: lowercase alphanumerics in
// hyphen-separated groups. A name outside it cannot be a served skill's URI
// segment, so it is rejected at ingest rather than repaired silently.
var skillNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// maxSkillNameLen matches the Agent Skills bound; longer names are invalid even
// though the filesystem would accept them.
const maxSkillNameLen = 64

// ValidateSkillName reports whether name is a legal Agent Skills name.
func ValidateSkillName(name string) error {
	if name == "" {
		return errors.New("name is empty")
	}
	if len(name) > maxSkillNameLen {
		return fmt.Errorf("name is %d characters (max %d)", len(name), maxSkillNameLen)
	}
	if !skillNameRE.MatchString(name) {
		return fmt.Errorf("name %q must match %s", name, skillNameRE.String())
	}
	return nil
}

// ReadSkillName returns the name declared by <skillDir>/SKILL.md frontmatter.
// The second result explains why no name came back (missing file, missing
// frontmatter, invalid name) so callers can surface it instead of guessing.
// It never fails hard: a skill without a usable name is a fact to report, not
// an error to raise on a read path.
func ReadSkillName(skillDir string) (string, string) {
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		return "", "SKILL.md unreadable: " + err.Error()
	}
	name, err := frontmatterName(data)
	if err != nil {
		return "", err.Error()
	}
	if err := ValidateSkillName(name); err != nil {
		return "", "frontmatter name invalid: " + err.Error()
	}
	return name, ""
}

// frontmatterName extracts the `name` field from a SKILL.md's YAML frontmatter.
func frontmatterName(data []byte) (string, error) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", errors.New("SKILL.md has no YAML frontmatter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", errors.New("SKILL.md frontmatter is not closed")
	}
	var probe struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal([]byte(rest[:end]), &probe); err != nil {
		return "", fmt.Errorf("SKILL.md frontmatter is not valid YAML: %w", err)
	}
	return strings.TrimSpace(probe.Name), nil
}

// FinalizeInstallDir aligns a freshly extracted skill directory with the name
// its author declared. dest is targetDir/<requested>; on success the returned
// name is what every later step (object-store key, listing, load_skill) must
// use, and renamedFrom is non-empty only when the directory actually moved.
//
// A skill with no usable frontmatter name keeps its requested directory: that
// state is reported by ReadSkillName's reason to whoever can act on it, and
// refusing the install here would break skills the runtime still loads fine.
// A name that collides with an already installed skill is refused outright —
// renaming onto it would silently replace someone else's skill.
func FinalizeInstallDir(targetDir, requested, dest string) (name, renamedFrom string, err error) {
	canonical, _ := ReadSkillName(dest)
	if canonical == "" || canonical == requested {
		return requested, "", nil
	}
	target := filepath.Join(targetDir, canonical)
	if target != dest {
		if _, statErr := os.Stat(target); statErr == nil {
			return "", "", fmt.Errorf(
				"skill %q declares name %q, which is already installed; remove it first or rename the incoming skill",
				requested, canonical)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", "", statErr
		}
		if err := os.Rename(dest, target); err != nil {
			return "", "", fmt.Errorf("align %q with its declared name %q: %w", requested, canonical, err)
		}
	}
	return canonical, requested, nil
}

// LocalDirMatchesRemote answers "does this local skill directory correspond to a
// skill the object store still holds", allowing for the directory and the
// declared name to disagree. Hydrate prunes local directories that are absent
// from the remote listing; without this check a skill installed by an older pod
// under its request name would be deleted on a newer pod that had aligned it.
func LocalDirMatchesRemote(rootDir, dirName string, remote map[string]bool) bool {
	if remote[dirName] {
		return true
	}
	if name, _ := ReadSkillName(filepath.Join(rootDir, dirName)); name != "" && remote[name] {
		return true
	}
	return false
}
