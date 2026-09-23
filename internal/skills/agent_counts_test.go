package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// agentLayer writes one skill into an agent's own layer and returns that directory,
// so a caller can also drop fixtures into it directly.
func agentLayer(t *testing.T, home, agentID string) string {
	t.Helper()
	dir := filepath.Join(home, "agents", agentID, "agent", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The count is the number `list_skills` lists, so the two layers an agent publishes
// are added up and the platform library is counted with them - not just the agent's
// own directory, which is the cheaper and wrong answer.
func TestPublishedCountsAddsBothLayers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FASTAGENT_HOME", home)

	platform := filepath.Join(home, "skills")
	writeCatalogSkill(t, platform, "platform-skill", "---\nname: platform-skill\n---\nbody\n", nil)

	own := agentLayer(t, home, "agt_counts")
	writeCatalogSkill(t, own, "refunds", "---\nname: refunds\n---\nbody\n", nil)
	writeCatalogSkill(t, own, "empty-dir", "---\nname: empty-dir\n---\nbody\n", nil)

	counts, failures := PublishedCounts([]string{"agt_counts"})
	if len(failures) != 0 {
		t.Fatalf("failures = %+v; want none", failures)
	}
	if counts["agt_counts"] != 3 {
		t.Fatalf("count = %d; want 3 (platform + the agent's two)", counts["agt_counts"])
	}
}

// A skill the agent shadows with its own copy is published once, and the shadowing
// rule is BuildCatalog's - the count must not re-derive it.
func TestPublishedCountsAppliesTheShadowingRule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FASTAGENT_HOME", home)

	writeCatalogSkill(t, filepath.Join(home, "skills"), "refunds",
		"---\nname: refunds\n---\nplatform copy\n", nil)
	own := agentLayer(t, home, "agt_shadow")
	writeCatalogSkill(t, own, "refunds", "---\nname: refunds\n---\nagent copy\n", nil)

	counts, failures := PublishedCounts([]string{"agt_shadow"})
	if len(failures) != 0 {
		t.Fatalf("failures = %+v; want none", failures)
	}
	if counts["agt_shadow"] != 1 {
		t.Fatalf("count = %d; want 1 - the agent's copy replaces the platform's", counts["agt_shadow"])
	}
}

// A skill that exists but cannot be published is not counted: the number has to match
// the listing, and the listing does not serve it.
func TestPublishedCountsExcludesUnpublishable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FASTAGENT_HOME", home)

	own := agentLayer(t, home, "agt_partial")
	writeCatalogSkill(t, own, "refunds", "---\nname: refunds\n---\nbody\n", nil)
	writeCatalogSkill(t, own, "pdf-tools", "---\nname: pdf-processing\n---\nbody\n", nil)
	writeCatalogSkill(t, own, "no-manifest", "body without frontmatter\n", nil)

	counts, failures := PublishedCounts([]string{"agt_partial"})
	if len(failures) != 0 {
		t.Fatalf("failures = %+v; want none", failures)
	}
	if counts["agt_partial"] != 1 {
		t.Fatalf("count = %d; want 1 - only the skill whose name matches is published", counts["agt_partial"])
	}
}

// "We could not read this layer" must not arrive as a count. An unreadable layer goes
// to the second map and the agent gets no entry in the first, because a caller that
// reads a 0 here would conclude the agent publishes nothing.
func TestPublishedCountsReportsAnUnreadableLayerSeparately(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FASTAGENT_HOME", home)

	own := agentLayer(t, home, "agt_unreadable")
	writeCatalogSkill(t, own, "refunds", "---\nname: refunds\n---\nbody\n", nil)
	// A directory the scan cannot walk: filepath.WalkDir reports the chmod, which
	// ScanSkillDirs returns as an unreadable location rather than as three skills.
	if err := os.Chmod(own, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(own, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable directory is still readable")
	}

	counts, failures := PublishedCounts([]string{"agt_unreadable"})
	if _, ok := counts["agt_unreadable"]; ok {
		t.Fatalf("counts = %+v; want no entry for an agent whose layer could not be read", counts)
	}
	if failures["agt_unreadable"] == "" {
		t.Fatalf("failures = %+v; want a reason", failures)
	}
}

// Two agents with their own layers are counted independently, and the shared platform
// layer is scanned once for both - the reason this function exists rather than a
// per-agent scan at each call site.
func TestPublishedCountsCountsAgentsIndependently(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FASTAGENT_HOME", home)

	writeCatalogSkill(t, filepath.Join(home, "skills"), "platform-skill",
		"---\nname: platform-skill\n---\nbody\n", nil)
	writeCatalogSkill(t, agentLayer(t, home, "agt_one"), "refunds", "---\nname: refunds\n---\nbody\n", nil)
	writeCatalogSkill(t, agentLayer(t, home, "agt_two"), "invoices", "---\nname: invoices\n---\nbody\n", nil)
	writeCatalogSkill(t, agentLayer(t, home, "agt_two"), "quotes", "---\nname: quotes\n---\nbody\n", nil)

	counts, failures := PublishedCounts([]string{"agt_one", "agt_two"})
	if len(failures) != 0 {
		t.Fatalf("failures = %+v; want none", failures)
	}
	if counts["agt_one"] != 2 || counts["agt_two"] != 3 {
		t.Fatalf("counts = %+v; want agt_one=2, agt_two=3", counts)
	}
}
