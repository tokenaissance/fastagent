package skills

import (
	"strings"
	"testing"
)

// TestDiagnose_CleanArchitecture_InstallResolution traces the full resolution
// chain for "clean-architecture" — searching skills.sh, matching by skillId
// and Source, and showing which entry would be installed.
func TestDiagnose_CleanArchitecture_InstallResolution(t *testing.T) {
	// Step 1: Live skills.sh search for "clean-architecture"
	results, err := SearchSkillsSh("clean-architecture")
	if err != nil {
		t.Fatalf("SearchSkillsSh failed: %v", err)
	}
	t.Logf("SearchSkillsSh(\"clean-architecture\") → %d results", len(results))

	// Step 2: List all exact skillId matches
	t.Log("")
	t.Log("── All exact skillId matches ──")
	exactMatches := 0
	for i, r := range results {
		if r.SkillID == "clean-architecture" {
			t.Logf("  [%d] id=%q source=%q installs=%d", i, r.ID, r.Source, r.Installs)
			exactMatches++
		}
	}
	t.Logf("Exact skillId matches: %d / %d", exactMatches, len(results))
	if exactMatches == 0 {
		t.Log("SKIP: no clean-architecture entries in skills.sh index")
		return
	}

	// Step 3: Old behavior — PickSkillsShExact (always returns first match)
	t.Log("")
	t.Log("── Old behavior: PickSkillsShExact ──")
	oldPick := PickSkillsShExact(results, "clean-architecture")
	if oldPick != nil {
		t.Logf("  PickSkillsShExact → source=%q installs=%d id=%q", oldPick.Source, oldPick.Installs, oldPick.ID)
	}

	// Step 4: New behavior — PickSkillsShBySource with repo hint
	t.Log("")
	t.Log("── New behavior: PickSkillsShBySource(repo hint) ──")
	testRepos := []string{
		"tokenaissance/clean-architecture",
		"tokenaissance/clean-architecture-skill",
		"wondelai/skills",
	}

	for _, repo := range testRepos {
		match := PickSkillsShBySource(results, "clean-architecture", repo)
		if match != nil {
			t.Logf("  repo=%q → MATCH source=%q installs=%d id=%q", repo, match.Source, match.Installs, match.ID)
		} else {
			t.Logf("  repo=%q → NO MATCH", repo)
		}
	}

	// Step 5: Simulate the actual install path
	t.Log("")
	t.Log("── Simulated install path ──")
	clickedRepo := "tokenaissance/clean-architecture" // what the user clicked
	if bySource := PickSkillsShBySource(results, "clean-architecture", clickedRepo); bySource != nil {
		// Verify the picked repo is actually tokenaissance
		if strings.EqualFold(bySource.Source, clickedRepo) {
			t.Logf("PASS: clicking %q installs from %q (correct)", clickedRepo, bySource.Source)
		} else {
			t.Logf("BUG: clicking %q but matched source=%q (mismatch)", clickedRepo, bySource.Source)
		}
	} else {
		// No source match — falls back to PickSkillsShExact
		fallback := PickSkillsShExact(results, "clean-architecture")
		if fallback != nil {
			t.Logf("FAIL: clicking %q → no Source match → falls back to %s/%s (WRONG)", clickedRepo, fallback.Source, fallback.SkillID)
		} else {
			t.Logf("FAIL: clicking %q → no Source match, no fallback", clickedRepo)
		}
	}

	// Step 6: Check if the frontend is sending the correct repo
	t.Log("")
	t.Log("── Frontend integration check ──")
	t.Log("Verify skills-panel.tsx handleInstall sends:")
	t.Log("  source: 'skillssh'")
	t.Log("  name: r.skillId")
	t.Log("  repo: r.source  ← must be the selected result's source field")
	t.Log("")
	t.Log("Check browser Network tab → POST /api/fastagent/skills/install")
	t.Log("Body should contain repo matching the clicked entry's Source field.")
}
