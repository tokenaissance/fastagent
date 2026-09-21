package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ScanSkillDirs reads every skill under the given layer directories and returns
// what was found, so BuildCatalog can decide what may be published.
//
// It reads the bytes it will hand out and nothing else: no {baseDir} substitution,
// no trimming, no newline normalisation. The entry the cloud serves carries a
// digest over exactly these bytes, so a transformation here would be a guaranteed
// verification failure at every host.
//
// The walk is recursive, and a skill's Path is its directory relative to the layer
// it was found in. That is what makes a nested skill addressable: the extension says
// the enclosing skill's path becomes part of the nested one's organizational prefix
// (`acme/refunds`), and until it is addressed by that path a nested skill is only a
// file inside its parent - published, but impossible to read as a skill of its own.
//
// A layer directory that does not exist is not an error: layers are configuration,
// and an unconfigured layer is simply empty. That is different from a layer that
// exists and cannot be read, which is returned as an error to the caller so it can
// be reported rather than silently shrinking the catalog.
func ScanSkillDirs(dirs []string) ([]DiscoveredSkill, error) {
	var found []DiscoveredSkill
	var unreadable []string

	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			unreadable = append(unreadable, dir)
			continue
		}
		walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				return nil
			}
			manifest, readErr := os.ReadFile(filepath.Join(path, skillManifestName))
			if readErr != nil {
				// Not a skill directory. Keep walking: a skill may be nested deeper,
				// and this directory may be the organizational prefix on the way to it.
				return nil
			}
			rel, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				return relErr
			}
			files, baseDirHits, filesErr := readSkillFiles(path)
			if filesErr != nil {
				unreadable = append(unreadable, path)
				return nil
			}
			found = append(found, DiscoveredSkill{
				Path:         filepath.ToSlash(rel),
				DirName:      entry.Name(),
				Frontmatter:  parseFrontmatterMap(manifest),
				Files:        files,
				BaseDirFiles: baseDirHits,
			})
			return nil
		})
		if walkErr != nil {
			unreadable = append(unreadable, dir)
		}
	}

	if len(unreadable) > 0 {
		return found, errors.New("unreadable skill locations: " + strings.Join(unreadable, ", "))
	}
	return found, nil
}

// The token the runtime replaces at load time. Spelled once here and once in
// internal/agent, which is why it is a named constant on this side.
const baseDirToken = "{baseDir}"

// The one file the runtime substitutes that token in: `loadSkillContent` and the
// `load_skill` tool both read exactly this file (internal/agent/skills.go,
// internal/agent/tools/load_skill.go), and nothing substitutes a bundled script or
// an asset. So this name is also the trigger for the diagnostic that says "the agent
// resolves it, a client does not": in any other file the token is literal for every
// reader, and a warning claiming a difference there would be false (see BuildCatalog).
// Named once, read by the scan and by that trigger.
const skillManifestName = "SKILL.md"

// readSkillFiles returns each file's metadata and, alongside it, every file whose
// content carries the literal `{baseDir}`.
//
// This endpoint must serve raw bytes — substituting here would break every digest —
// and it does: the runtime substitutes the token when it loads a skill for the agent's
// own use, and a client connected over MCP reads the literal text. Which files that
// difference is *stated* for is decided by BuildCatalog (only the manifest is
// substituted); what the scan owes it is the measurement — which files carry the token
// — and the scan rides the read that was already happening for the hash.
func readSkillFiles(skillDir string) ([]CatalogFile, []string, error) {
	var files []CatalogFile
	var baseDirHits []string
	err := filepath.WalkDir(skillDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(skillDir, path)
		if relErr != nil {
			return relErr
		}
		// Hash here, where the bytes are: the digest has to cover exactly what was
		// read, so computing it anywhere downstream would mean sending the bytes to
		// be able to compute it.
		sum := sha256.Sum256(content)
		relSlash := filepath.ToSlash(rel)
		files = append(files, CatalogFile{
			Path:   relSlash,
			Digest: "sha256:" + hex.EncodeToString(sum[:]),
			Size:   len(content),
		})
		if bytes.Contains(content, []byte(baseDirToken)) {
			baseDirHits = append(baseDirHits, relSlash)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return files, baseDirHits, nil
}

// parseFrontmatterMap returns the SKILL.md frontmatter as the map the contract
// carries verbatim. A file without usable frontmatter yields an empty map, which
// BuildCatalog then refuses for having no name — the reason travels to the caller
// instead of the skill disappearing.
func parseFrontmatterMap(manifest []byte) map[string]any {
	text := strings.TrimPrefix(string(manifest), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return map[string]any{}
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := yaml.Unmarshal([]byte(rest[:end]), &out); err != nil {
		return map[string]any{}
	}
	return out
}
