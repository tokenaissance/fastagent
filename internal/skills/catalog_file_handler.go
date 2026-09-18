package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// SkillFileHandler serves one file of one skill, and only files the catalog lists.
//
// The whitelist is not a parameter but a fresh scan of the same layer directories
// the catalog is built from: the request names a skill and a relative path, and both
// have to exist in that scan. That is what keeps this from being an arbitrary file
// read — a path like ../../etc/passwd is not in the scan, so it is not servable,
// regardless of how it is encoded.
//
// The bytes are re-hashed before they go out. A file that changed after the scan
// would otherwise be served under a digest that no longer describes it, and the
// host's verification would fail with no way to tell staleness from tampering.
func SkillFileHandler(dirs []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}

		skill := r.URL.Query().Get("skill")
		relPath := r.URL.Query().Get("path")
		if skill == "" || relPath == "" {
			http.Error(w, "skill and path are required", http.StatusBadRequest)
			return
		}

		discovered, err := ScanSkillDirs(dirs)
		if err != nil {
			slog.Warn("skill file: incomplete scan", "error", err)
			http.Error(w, "skills could not be read completely: "+err.Error(), http.StatusInternalServerError)
			return
		}

		listed, ok := findListedFile(discovered, skill, relPath)
		if !ok {
			http.NotFound(w, r)
			return
		}

		root, ok := resolveSkillDir(dirs, skill)
		if !ok {
			http.NotFound(w, r)
			return
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(listed.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}

		sum := sha256.Sum256(content)
		if got := "sha256:" + hex.EncodeToString(sum[:]); got != listed.Digest {
			// The catalog's entry is stale. Serving these bytes would hand the host
			// content its own verification rejects; say so and let it re-list.
			slog.Warn("skill file: content changed since the scan", "skill", skill, "path", listed.Path)
			http.Error(w, "content changed since the catalog was built; request the catalog again",
				http.StatusConflict)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(content); err != nil {
			slog.Warn("skill file: write failed", "skill", skill, "path", listed.Path, "error", err)
		}
	})
}

func findListedFile(discovered []DiscoveredSkill, skill, relPath string) (CatalogFile, bool) {
	want := strings.TrimPrefix(filepath.ToSlash(relPath), "/")
	for _, candidate := range discovered {
		if candidate.DirName != skill {
			continue
		}
		for _, file := range candidate.Files {
			if file.Path == want {
				return file, true
			}
		}
	}
	return CatalogFile{}, false
}

func resolveSkillDir(dirs []string, skill string) (string, bool) {
	if skill == "" || strings.ContainsAny(skill, `/\`) || skill == "." || skill == ".." {
		return "", false
	}
	for _, dir := range dirs {
		candidate := filepath.Join(dir, skill)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}
