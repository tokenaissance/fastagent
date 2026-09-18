package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, dir, frontmatter string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# skill\n"
	if frontmatter != "" {
		body = "---\n" + frontmatter + "\n---\n" + body
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findItem(t *testing.T, report ReconcileReport, dirName string) ReconcileItem {
	t.Helper()
	for _, item := range report.Items {
		if item.DirName == dirName {
			return item
		}
	}
	t.Fatalf("no report item for %q", dirName)
	return ReconcileItem{}
}

// A dry run must decide without touching the disk: the whole point of the
// command is that an operator can see the plan for production data first.
func TestReconcileDryRunRenamesNothing(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "pdf-tools"), "name: pdf-processing\ndescription: extract and fill PDFs")

	report, err := ReconcileDir(root, GlobalSkillOwner, "managed", true)
	if err != nil {
		t.Fatal(err)
	}
	item := findItem(t, report, "pdf-tools")
	if item.Action != ActionRename || item.DeclaredName != "pdf-processing" {
		t.Fatalf("action = %q, declared = %q; want rename to pdf-processing", item.Action, item.DeclaredName)
	}
	if _, err := os.Stat(filepath.Join(root, "pdf-tools", "SKILL.md")); err != nil {
		t.Fatalf("dry run moved the directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pdf-processing")); !os.IsNotExist(err) {
		t.Fatal("dry run created the target directory")
	}
	// The report is what an operator acts on: it must name the other two stores.
	if item.RemoteKeyPrefix != "_global/skills/pdf-tools/" || item.RemoteKeyTarget != "_global/skills/pdf-processing/" {
		t.Fatalf("object-store worklist = %q -> %q", item.RemoteKeyPrefix, item.RemoteKeyTarget)
	}
	if len(item.ConfigPaths) == 0 || item.ConfigPaths[0] != "skills.entries.pdf-tools" {
		t.Fatalf("config worklist = %v; want skills.entries.pdf-tools first", item.ConfigPaths)
	}
}

func TestReconcileApplyRenamesAndKeepsContent(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "pdf-tools"), "name: pdf-processing\ndescription: extract and fill PDFs")
	if err := os.WriteFile(filepath.Join(root, "pdf-tools", "reference.md"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := ReconcileDir(root, GlobalSkillOwner, "managed", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Renamed != 1 {
		t.Fatalf("renamed = %d; want 1", report.Renamed)
	}
	if _, err := os.Stat(filepath.Join(root, "pdf-processing", "reference.md")); err != nil {
		t.Fatalf("supporting file did not travel with the rename: %v", err)
	}
	// Idempotent: a second run has nothing left to do.
	again, err := ReconcileDir(root, GlobalSkillOwner, "managed", false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Renamed != 0 || findItem(t, again, "pdf-processing").Action != ActionOK {
		t.Fatalf("second run was not a no-op: %+v", again)
	}
}

func TestReconcileRefusesCollisionAndNamelessSkill(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "pdf-processing"), "name: pdf-processing\ndescription: the incumbent")
	writeSkill(t, filepath.Join(root, "pdf-tools"), "name: pdf-processing\ndescription: the newcomer")
	writeSkill(t, filepath.Join(root, "legacy-no-frontmatter"), "")

	report, err := ReconcileDir(root, GlobalSkillOwner, "managed", false)
	if err != nil {
		t.Fatal(err)
	}
	conflict := findItem(t, report, "pdf-tools")
	if conflict.Action != ActionConflict {
		t.Fatalf("action = %q; want conflict — the declared name is taken", conflict.Action)
	}
	if _, err := os.Stat(filepath.Join(root, "pdf-tools", "SKILL.md")); err != nil {
		t.Fatalf("conflicting skill was moved anyway: %v", err)
	}
	skip := findItem(t, report, "legacy-no-frontmatter")
	if skip.Action != ActionSkip || skip.Reason == "" {
		t.Fatalf("nameless skill = %+v; want skip with a reason", skip)
	}
}
