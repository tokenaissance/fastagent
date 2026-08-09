package skills

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"net/http"
	"strings"
)

// InstallFromGitHubRepo installs a skill folder from a public GitHub repo
// identified by "owner/repo". If skillName is empty, the repo itself is
// assumed to be the skill (tarball root is extracted into
// targetDir/<repo>/). Otherwise it looks up the skill folder (at any depth)
// inside the repo and extracts it to targetDir/<skillName>/.
func InstallFromGitHubRepo(repo, skillName, targetDir string) (*Result, error) {
	repo = normalizeGitHubRepo(repo)
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("repo must be owner/repo, got %q", repo)
	}
	owner, name := parts[0], parts[1]

	client := defaultHTTPClient()
	var lastErr error
	for _, ref := range []string{"main", "master"} {
		tarURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s", owner, name, ref)

		subpath := ""
		dest := ""
		installedName := skillName

		if skillName == "" {
			// Whole-repo skill: extract the tarball top into targetDir/<name>.
			installedName = name
			dest = fmt.Sprintf("%s/%s", strings.TrimRight(targetDir, "/"), installedName)
		} else {
			found, err := findSkillDirInTarball(client, tarURL, skillName)
			if err != nil {
				lastErr = err
				continue
			}
			if found == "" {
				// Not found as a subdirectory — the repo itself may
				// be the skill (SKILL.md at root, common for repos
				// like tokenaissance/fastagent-meta-skill).
				if repoIsSkill, _ := repoHasRootSkillMD(client, tarURL); repoIsSkill {
					installedName = skillName
					subpath = ""
					dest = fmt.Sprintf("%s/%s", strings.TrimRight(targetDir, "/"), skillName)
				} else {
					lastErr = fmt.Errorf("skill %q not found in %s/%s@%s", skillName, owner, name, ref)
					continue
				}
			} else {
				subpath = found
				dest = fmt.Sprintf("%s/%s", strings.TrimRight(targetDir, "/"), skillName)
			}
		}

		n, err := extractSubpath(client, tarURL, subpath, dest)
		if err != nil {
			lastErr = err
			continue
		}
		if n == 0 {
			lastErr = fmt.Errorf("extracted no files from %s", tarURL)
			continue
		}
		version := readSkillVersionFromDir(dest)
		if version == "" {
			version = latestGitHubRelease(client, owner, name)
		}
		if version == "" {
			version = ref
		}
		return &Result{
			Source:       "github",
			Name:         installedName,
			Version:      version,
			InstalledAt:  dest,
			FilesWritten: n,
		}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no main or master branch on %s", repo)
	}
	return nil, lastErr
}

// repoHasRootSkillMD quickly probes a GitHub tarball to check whether
// SKILL.md exists at the repo root (after the top-level archive dir). This
// is the "repo is the skill" pattern (e.g. tokenaissance/fastagent-meta-skill).
func repoHasRootSkillMD(client *http.Client, tarURL string) (bool, error) {
	resp, err := client.Get(tarURL)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return false, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		slash := strings.IndexByte(hdr.Name, '/')
		if slash < 0 {
			continue
		}
		// After stripping the top-level archive dir, we want exactly "SKILL.md".
		if hdr.Name[slash+1:] == "SKILL.md" && hdr.Typeflag == tar.TypeReg {
			return true, nil
		}
	}
	return false, nil
}

// normalizeGitHubRepo strips common wrapper prefixes/suffixes so callers can
// pass things like "https://github.com/owner/repo.git" directly.
func normalizeGitHubRepo(repo string) string {
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "http://github.com/")
	repo = strings.TrimPrefix(repo, "github.com/")
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.Trim(repo, "/")
	return repo
}
