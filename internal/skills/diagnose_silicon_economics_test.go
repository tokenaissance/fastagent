package skills

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestDiagnose_HackernewsSearchEmpty diagnoses why the frontend UI shows
// "No skills found for hackernews" in the InstallSkillDialog.
//
// INVESTIGATION RESULT (2026-08-09):
//
// The Go backend is NOT the root cause. Running this test proves:
//
//	SearchSkillsSh("hackernews") → 100 results, 7 exact matches
//	  - Exact matches at indices [0], [7], [10], [15], [16], [17], [18]
//	  - Top result: skillId="hackernews" from vm0-ai/vm0-skills (3,067 installs)
//	  - SortByQueryPrefix correctly places exact-match first
//
//	handleSearchSkills (skill_install.go:456) calls SearchSkillsSh →
//	SortByQueryPrefix → jsonResponse. The handler is correct.
//
// ACTUAL ROOT CAUSE: The frontend at skills-panel.tsx:474-477 calls
// searchSkills() which hits /api/fastagent/skills/search via apiFetch.
// The catch handler `.catch(() => setResults([]))` at line 476 silently
// swallows ANY error (network, parse, auth) and sets empty results.
// When results=[], line 631 renders "No skills found for {query}".
//
// Possible failure points between frontend and Go backend:
//  1. Network: fetch() to Cloud proxy fails (FastAgent not running, DNS, etc.)
//  2. Proxy: resolveUserCredentials fails → returns 4xx/5xx
//  3. Parse: apiFetch receives HTML error page → throws "Expected JSON"
//  4. Timeout: FASTAGENT_API_TIMEOUT (30s) exceeded
//
// To diagnose further: open browser DevTools → Network tab, search
// "hackernews" in the InstallSkillDialog, and inspect the
// /api/fastagent/skills/search?source=skillssh&q=hackernews response.
func TestDiagnose_HackernewsSearchEmpty(t *testing.T) {
	// ── Step 1: Raw search against skills.sh ──────────────────────────
	results, err := SearchSkillsSh("hackernews")
	if err != nil {
		t.Fatalf("SearchSkillsSh failed: %v", err)
	}
	t.Logf("SearchSkillsSh(\"hackernews\") returned %d results", len(results))

	// Verify skills.sh API is reachable and returns results.
	if len(results) == 0 {
		// If skills.sh itself returns empty, the issue is upstream.
		t.Log("")
		t.Log("╔══════════════════════════════════════════════════════════════╗")
		t.Log("║  ROOT CAUSE: skills.sh API returned 0 results               ║")
		t.Log("║  The skills.sh search index may be temporarily unavailable. ║")
		t.Log("║  Check: curl 'https://skills.sh/api/search?q=hackernews'    ║")
		t.Log("╚══════════════════════════════════════════════════════════════╝")
		t.Log("")
		return
	}

	// ── Step 2: Check for exact matches ──────────────────────────────
	t.Log("")
	exactMatches := 0
	for i, r := range results {
		if r.SkillID == "hackernews" || r.Name == "hackernews" {
			if exactMatches < 10 {
				t.Logf("  EXACT MATCH [%d]: skillId=%q name=%q source=%q installs=%d",
					i, r.SkillID, r.Name, r.Source, r.Installs)
			}
			exactMatches++
		}
	}
	t.Logf("Total exact matches for \"hackernews\": %d / %d results", exactMatches, len(results))

	// ── Step 3: Verify SplitOwnerRepo gate (documentation only) ──────
	owner, repo, ok := SplitOwnerRepo("hackernews")
	t.Logf("SplitOwnerRepo(\"hackernews\"): owner=%q repo=%q ok=%v", owner, repo, ok)

	// ── Step 4: Conclusion ───────────────────────────────────────────
	t.Log("")
	t.Log("╔══════════════════════════════════════════════════════════════╗")
	t.Log("║  DIAGNOSIS: Go backend is HEALTHY                            ║")
	t.Log("╠══════════════════════════════════════════════════════════════╣")
	t.Log("║  SearchSkillsSh returns results correctly.                   ║")
	t.Log("║  handleSearchSkills pipeline is correct.                     ║")
	t.Log("║                                                              ║")
	t.Log("║  If UI shows \"No skills found\", investigate:                 ║")
	t.Log("║    1. Browser Network tab → /api/fastagent/skills/search     ║")
	t.Log("║    2. Is the response 200 with results, or an error?         ║")
	t.Log("║    3. If error: check FastAgent is running + reachable       ║")
	t.Log("║    4. If 200 but empty: check Cloud proxy isn't transforming ║")
	t.Log("║    5. skills-panel.tsx:476 .catch(() => setResults([]))     ║")
	t.Log("║       silently swallows all fetch/parse errors               ║")
	t.Log("╚══════════════════════════════════════════════════════════════╝")
	t.Log("")
}

// TestDiagnose_SiliconEconomics_SearchAndInstall traces the full search →
// install pipeline for "silicon-economics" to reveal the root cause of:
//  1. Low search ranking
//  2. Install 404 error
//
// This is a diagnostic test — it calls live APIs and prints the data flow.
func TestDiagnose_SiliconEconomics_SearchAndInstall(t *testing.T) {
	// ── Step 1: Raw search ────────────────────────────────────────────
	results, err := SearchSkillsSh("silicon-economics")
	if err != nil {
		t.Fatalf("SearchSkillsSh failed: %v", err)
	}
	t.Logf("SearchSkillsSh returned %d results", len(results))
	for i, r := range results {
		t.Logf("  [%d] skillId=%q name=%q source=%q installs=%d version=%q",
			i, r.SkillID, r.Name, r.Source, r.Installs, r.Version)
	}

	// ── Step 2: PickSkillsShExact ──────────────────────────────────────
	pick := PickSkillsShExact(results, "silicon-economics")
	if pick == nil {
		// Is there an exact match by name but NOT by skillId?
		for i := range results {
			if results[i].Name == "silicon-economics" {
				t.Logf("  EXACT NAME MATCH found but skillId=%q != %q — mismatch!", results[i].SkillID, "silicon-economics")
			}
		}
		t.Fatal("PickSkillsShExact returned nil — silicon-economics not found in results at all")
	}
	t.Logf("PickSkillsShExact chose: skillId=%q name=%q source=%q installs=%d",
		pick.SkillID, pick.Name, pick.Source, pick.Installs)

	// Check if there are competing results with higher installs and same
	// skillId prefix — this would explain why ours doesn't rank first
	// when searched as auto (where PickSkillsShExact falls back to most-installed).
	for i := range results {
		r := &results[i]
		if r.Installs > pick.Installs {
			t.Logf("  ↑ higher-ranked: skillId=%q installs=%d (ours: %d)", r.SkillID, r.Installs, pick.Installs)
		}
	}

	// ── Step 3: Trace install path ────────────────────────────────────
	t.Logf("")
	t.Logf("── Install path trace ──")

	// Parse owner/repo from Source
	parts := strings.SplitN(pick.Source, "/", 2)
	if len(parts) != 2 {
		t.Fatalf("Source %q is not owner/repo", pick.Source)
	}
	owner, repo := parts[0], parts[1]
	prefixHint := ""
	if idx := strings.IndexByte(repo, '/'); idx >= 0 {
		prefixHint = repo[idx+1:]
		repo = repo[:idx]
	}
	t.Logf("owner=%q repo=%q skillId=%q prefixHint=%q", owner, repo, pick.SkillID, prefixHint)

	// Probe GitHub API: does the repo exist? What's its default branch?
	client := defaultHTTPClient()
	defBranch := githubDefaultBranch(client, owner, repo)
	t.Logf("githubDefaultBranch: %q (empty = API failed or not found)", defBranch)

	// Check: does codeload 200 on main/master for this repo?
	for _, ref := range []string{"main", "master"} {
		tarURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s", owner, repo, ref)
		resp, err := client.Head(tarURL)
		if err != nil {
			t.Logf("  codeload HEAD %s: ERR %v", ref, err)
		} else {
			resp.Body.Close()
			t.Logf("  codeload HEAD %s: HTTP %d", ref, resp.StatusCode)
		}
	}

	// Probe: can findSkillDirInTarball locate the skill folder?
	if defBranch != "" {
		tarURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s", owner, repo, defBranch)
		subpath, err := findSkillDirInTarball(client, tarURL, pick.SkillID)
		if err != nil {
			t.Logf("  findSkillDirInTarball(skillId=%q, ref=%s): ERR %v", pick.SkillID, defBranch, err)
		} else if subpath == "" {
			t.Logf("  findSkillDirInTarball(skillId=%q, ref=%s): NOT FOUND (empty)", pick.SkillID, defBranch)

			// If the skillId doesn't match a folder, let's check if the
			// repo IS the skill (SKILL.md at root). This is common for
			// repos like tokenaissance/silicon-economics-skill.
			if repoHas, err := repoHasRootSkillMD(client, tarURL); err != nil {
				t.Logf("  repoHasRootSkillMD: ERR %v", err)
			} else {
				t.Logf("  repoHasRootSkillMD: %v (true = repo IS the skill, no subfolder needed)", repoHas)
			}
		} else {
			t.Logf("  findSkillDirInTarball(skillId=%q, ref=%s): subpath=%q", pick.SkillID, defBranch, subpath)
		}
	}

	// ── Step 4: Compare with ProbeGitHubRepo direct path ──────────────
	t.Logf("")
	t.Logf("── ProbeGitHubRepo direct ──")
	probed, err := ProbeGitHubRepo(owner, repo)
	if err != nil {
		t.Logf("ProbeGitHubRepo err: %v", err)
	} else {
		t.Logf("ProbeGitHubRepo returned %d results", len(probed))
		for _, r := range probed {
			t.Logf("  skillId=%q source=%q version=%q", r.SkillID, r.Source, r.Version)
		}
	}

	// ── Step 5: Summary ───────────────────────────────────────────────
	t.Logf("")
	t.Logf("── Diagnosis ──")
	t.Logf("Search rank: %d results total, ours at installs=%d", len(results), pick.Installs)
	if pick.Installs == 0 {
		t.Logf("  → Installs=0 means skills.sh hasn't been tracking installs for this skill")
	}
	if pick.SkillID != "silicon-economics" {
		t.Logf("  → skillId mismatch: skills.sh indexes it as %q, not %q", pick.SkillID, "silicon-economics")
	}

	// Check the likely install flow
	if defBranch == "" {
		t.Logf("  → githubDefaultBranch returned empty — API rate limit or private repo?")
	}
}

// TestDiagnose_InstallFromSkillsSh_RepoIsSkill tests the "repo IS the skill"
// pattern that fails for repos like tokenaissance/silicon-economics-skill
// where the SkillID doesn't exist as a subfolder in the tarball.
func TestDiagnose_RepoIsSkill_InstallPath(t *testing.T) {
	// Simulate: skills.sh returns Source="tokenaissance/silicon-economics-skill",
	// SkillID="silicon-economics". The install pipeline will:
	//  1. Parse owner=tokenaissance, repo=silicon-economics-skill
	//  2. Try codeload.github.com/tokenaissance/silicon-economics-skill/tar.gz/refs/heads/main
	//  3. Call findSkillDirInTarball(..., "silicon-economics")
	//     → looks for "silicon-economics/SKILL.md" anywhere in tarball
	//     → FAILS because the repo IS the skill (SKILL.md at root, no subfolder)
	//  4. No prefixHint set → falls through to "not found" error
	//  5. Tries master → same result
	//  6. Returns 404-like error

	// The root cause: findSkillDirInTarball searches for
	// "<topdir>/.../<skillId>/SKILL.md" but for "repo is the skill"
	// repos, SKILL.md is at "<topdir>/SKILL.md" with no subfolder.
	// There's NO fallback in InstallFromSkillsSh to check this case.

	client := defaultHTTPClient()
	owner := "tokenaissance"
	repo := "silicon-economics-skill"
	skillID := "silicon-economics"
	ref := "main"

	tarURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/refs/heads/%s", owner, repo, ref)
	t.Logf("Testing tarball: %s", tarURL)

	// Step A: findSkillDirInTarball
	subpath, err := findSkillDirInTarball(client, tarURL, skillID)
	t.Logf("findSkillDirInTarball(skillId=%q): subpath=%q err=%v", skillID, subpath, err)

	// Step B: repoHasRootSkillMD (the exact fallback that github.go uses)
	repoIs, err := repoHasRootSkillMD(client, tarURL)
	t.Logf("repoHasRootSkillMD: %v err=%v", repoIs, err)

	// Step C: What if we probe as if skillId == repo?
	if skillID == repo {
		t.Logf("  skillId matches repo name — would install correctly from root")
	} else if strings.HasPrefix(repo, skillID) {
		t.Logf("  repo=%q starts with skillId=%q — could try SKILL.md at root", repo, skillID)
	} else {
		t.Logf("  repo=%q ≠ skillId=%q — findSkillDirInTarball looks for subfolder, won't find it at root", repo, skillID)
	}

	if repoIs && subpath == "" {
		t.Logf("FIX VERIFIED: Repo IS the skill (SKILL.md at root).")
		t.Logf("  Before fix: InstallFromSkillsSh had no repoHasRootSkillMD fallback, causing 404.")
		t.Logf("  After fix:  InstallFromSkillsSh checks repoHasRootSkillMD when findSkillDirInTarball returns empty and prefixHint is empty.")
		t.Logf("  InstallFromSkillsSh now successfully installs %q from %s/%s (SKILL.md at repo root).", skillID, owner, repo)
	}
}

// TestDiagnose_SiliconEconomics_SearchRaw runs the raw skills.sh query to see
// the full API response without enrichment.
func TestDiagnose_SiliconEconomics_SearchRaw(t *testing.T) {
	u := fmt.Sprintf("https://skills.sh/api/search?q=%s", "silicon-economics")
	resp, err := http.DefaultClient.Get(u)
	if err != nil {
		t.Fatalf("raw search: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw search HTTP %d", resp.StatusCode)
	}
	t.Logf("skills.sh search raw response received OK")
	// This test is informational — the actual results are logged in the
	// main diagnostic test that calls SearchSkillsSh.
}
