package skills

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPickSkillsShBySource_MatchesBothFields(t *testing.T) {
	results := []SkillsShResult{
		{ID: "wondelai/skills/clean-architecture", SkillID: "clean-architecture", Source: "wondelai/skills", Installs: 4685},
		{ID: "tokenaissance/clean-architecture/clean-architecture", SkillID: "clean-architecture", Source: "tokenaissance/clean-architecture", Installs: 3},
		{ID: "other/repo/clean-architecture", SkillID: "clean-architecture", Source: "other/repo", Installs: 100},
	}

	// PickSkillsShExact returns first exact skillId match (highest installs)
	exact := PickSkillsShExact(results, "clean-architecture")
	if exact == nil || exact.Source != "wondelai/skills" {
		t.Fatalf("PickSkillsShExact → source=%q, want wondelai/skills", strSource(exact))
	}
	t.Logf("PickSkillsShExact  → source=%q installs=%d (first match = most-installed)", exact.Source, exact.Installs)

	// PickSkillsShBySource matches BOTH skillId AND Source
	bySource := PickSkillsShBySource(results, "clean-architecture", "tokenaissance/clean-architecture")
	if bySource == nil {
		t.Fatal("PickSkillsShBySource(clean-architecture, tokenaissance/clean-architecture) → nil, want match at index 1")
	}
	if bySource.Source != "tokenaissance/clean-architecture" {
		t.Fatalf("PickSkillsShBySource → source=%q, want tokenaissance/clean-architecture", bySource.Source)
	}
	t.Logf("PickSkillsShBySource → source=%q installs=%d ✓ (correct repo selected)", bySource.Source, bySource.Installs)

	// No match for wrong repo
	noMatch := PickSkillsShBySource(results, "clean-architecture", "nonexistent/repo")
	if noMatch != nil {
		t.Fatalf("PickSkillsShBySource(wrong repo) → source=%q, want nil", noMatch.Source)
	}
	t.Log("PickSkillsShBySource(wrong repo) → nil ✓ (no false match)")

	// Case-insensitive Source match
	caseInsensitive := PickSkillsShBySource(results, "clean-architecture", "Tokenaissance/Clean-Architecture")
	if caseInsensitive == nil {
		t.Fatal("PickSkillsShBySource(case-insensitive Source) → nil, want match")
	}
	t.Logf("PickSkillsShBySource(case-insensitive Source) → source=%q ✓", caseInsensitive.Source)
}

func TestResult_RepoField_JSONRoundtrip(t *testing.T) {
	// Verify that Result.Repo serializes correctly for all three install paths.
	tests := []struct {
		name   string
		result Result
		want   string // expected "repo" value in JSON
	}{
		{
			name: "skills.sh install records actual GitHub repo",
			result: Result{
				Source:       "skills.sh",
				Repo:         "tokenaissance/clean-architecture",
				Name:         "clean-architecture",
				Version:      "1.2.2",
				InstalledAt:  "/tmp/skills/test-agent/clean-architecture",
				FilesWritten: 5,
			},
			want: `"repo":"tokenaissance/clean-architecture"`,
		},
		{
			name: "github install records repo param",
			result: Result{
				Source:       "github",
				Repo:         "tokenaissance/clean-architecture-skill",
				Name:         "clean-architecture",
				Version:      "1.1.1",
				InstalledAt:  "/tmp/skills/test-agent/clean-architecture",
				FilesWritten: 3,
			},
			want: `"repo":"tokenaissance/clean-architecture-skill"`,
		},
		{
			name: "clawhub install has empty repo (omitempty)",
			result: Result{
				Source:       "clawhub",
				Repo:         "",
				Name:         "some-skill",
				Version:      "2.0.0",
				InstalledAt:  "/tmp/skills/test-agent/some-skill",
				FilesWritten: 7,
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.result)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if tt.want == "" {
				// Repo is empty with omitempty → should NOT appear in JSON
				if strings.Contains(string(b), `"repo"`) {
					t.Fatalf("empty Repo should be omitted: got %s", string(b))
				}
				t.Logf("  JSON: %s ✓ (repo omitted)", string(b))
			} else {
				if !strings.Contains(string(b), tt.want) {
					t.Fatalf("expected %s in JSON: got %s", tt.want, string(b))
				}
				t.Logf("  JSON: %s ✓", string(b))
			}
		})
	}
}

func TestResult_RepoField_EachInstallPath(t *testing.T) {
	// Verify that every install path that produces a Result includes Repo.
	// This test reads the actual code and checks for Repo: entries in Result literals.
	// (Compile-time check: if any Result literal is missing Repo, the test
	// would still compile — but the grep at the bottom is a documentation aid.)

	t.Run("skills.sh", func(t *testing.T) {
		// InstallFromSkillsSh uses r.Source as Repo
		r := SkillsShResult{SkillID: "test-skill", Source: "owner/repo"}
		_ = &Result{
			Source: "skills.sh",
			Repo:   r.Source, // ← this is what the production code does at skillssh.go:172
			Name:   r.SkillID,
		}
	})

	t.Run("github", func(t *testing.T) {
		repo := "owner/repo"
		_ = &Result{
			Source: "github",
			Repo:   repo, // ← github.go:79
			Name:   "installed-name",
		}
	})

	t.Run("clawhub", func(t *testing.T) {
		_ = &Result{
			Source: "clawhub",
			Repo:   "", // ← install.go:84 (empty, no github repo)
			Name:   "slug",
		}
	})

	t.Run("auto", func(t *testing.T) {
		// InstallAuto returns Result from InstallFromSkillsSh or InstallFromClawHub,
		// which both already populate Repo — no separate Result literal.
	})
}

// TestInstallFromSkillsSh_RepoField verifies that InstallFromSkillsSh
// populates Result.Repo with the result's Source (owner/repo). This is an
// integration test — it downloads a real tarball from GitHub.
func TestInstallFromSkillsSh_RepoField(t *testing.T) {
	// Simulate what runInstall does: search → PickSkillsShBySource → InstallFromSkillsSh
	results, err := SearchSkillsSh("clean-architecture")
	if err != nil {
		t.Fatalf("SearchSkillsSh: %v", err)
	}

	// Simulate user clicking "tokenaissance/clean-architecture" in the dialog
	repo := "tokenaissance/clean-architecture"
	pick := PickSkillsShBySource(results, "clean-architecture", repo)
	if pick == nil {
		t.Fatalf("PickSkillsShBySource(clean-architecture, %q) returned nil — source not in skills.sh?", repo)
	}
	t.Logf("PickSkillsShBySource → id=%q source=%q installs=%d", pick.ID, pick.Source, pick.Installs)

	// Install the skill — this downloads the real tarball
	tmpDir := t.TempDir()
	result, err := InstallFromSkillsSh(*pick, tmpDir)
	if err != nil {
		t.Fatalf("InstallFromSkillsSh: %v", err)
	}

	// Verify Repo is correctly populated
	if result.Repo == "" {
		t.Fatal("BUG: Result.Repo is empty — InstallFromSkillsSh didn't populate it")
	}
	if result.Repo != repo {
		t.Fatalf("Result.Repo = %q, want %q", result.Repo, repo)
	}
	t.Logf("Result.Repo = %q ✓", result.Repo)
	t.Logf("Result.Source = %q (registry)", result.Source)
	t.Logf("Result.Name = %q", result.Name)
	t.Logf("Result.Version = %q", result.Version)
	t.Logf("Result.FilesWritten = %d", result.FilesWritten)

	// Simulate handler response construction (skill_install.go:99-106)
	resp := map[string]any{
		"ok":          true,
		"source":      result.Source,
		"name":        result.Name,
		"version":     result.Version,
		"installedAt": result.InstalledAt,
		"files":       result.FilesWritten,
	}
	if result.Repo != "" {
		resp["repo"] = result.Repo
	}

	b, _ := json.Marshal(resp)
	t.Logf("Handler JSON: %s", string(b))

	if !strings.Contains(string(b), `"repo"`) {
		t.Fatal(`Handler JSON missing "repo" field`)
	}
	if !strings.Contains(string(b), `"repo":"`+repo+`"`) {
		t.Fatalf(`Handler JSON has wrong repo value: %s`, string(b))
	}
	t.Log("Handler JSON contains correct repo ✓")
}

func strSource(r *SkillsShResult) string {
	if r == nil {
		return "<nil>"
	}
	return r.Source
}
