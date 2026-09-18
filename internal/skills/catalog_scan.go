package skills

import (
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
// A layer directory that does not exist is not an error: layers are configuration,
// and an unconfigured layer is simply empty. That is different from a layer that
// exists and cannot be read, which is returned as an error to the caller so it can
// be reported rather than silently shrinking the catalog.
func ScanSkillDirs(dirs []string) ([]DiscoveredSkill, error) {
	var found []DiscoveredSkill
	var unreadable []string

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			unreadable = append(unreadable, dir)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			skillDir := filepath.Join(dir, entry.Name())
			manifest, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
			if err != nil {
				continue // not a skill directory; the loader makes the same call
			}
			files, err := readSkillFiles(skillDir)
			if err != nil {
				unreadable = append(unreadable, skillDir)
				continue
			}
			found = append(found, DiscoveredSkill{
				DirName:     entry.Name(),
				Frontmatter: parseFrontmatterMap(manifest),
				Files:       files,
			})
		}
	}

	if len(unreadable) > 0 {
		return found, errors.New("unreadable skill locations: " + strings.Join(unreadable, ", "))
	}
	return found, nil
}

func readSkillFiles(skillDir string) ([]CatalogFile, error) {
	var files []CatalogFile
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
		files = append(files, CatalogFile{
			Path:   filepath.ToSlash(rel),
			Digest: "sha256:" + hex.EncodeToString(sum[:]),
			Size:   len(content),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
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
