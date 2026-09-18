package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Reconcile aligns already-installed skills with the identity rule enforced at
// ingest (see name.go): a skill directory must be named like the `name` its
// SKILL.md declares. Everything installed before that rule existed can violate
// it, and three durable stores carry the old name:
//
//	1. the directory itself                     <layer root>/<dirName>/
//	2. the object-store keys                    <owner>/skills/<dirName>/...
//	3. configs_kv rows and name lists           skills.entries.<dirName>[.env.*]
//	                                           skills.agent_entries.<agent>.<dirName>.*
//	                                           skills.always_load[] / skills.disabled[]
//
// This file moves (1) and reports (2) and (3) precisely enough for an operator
// to act; the running service moves (2) when it has a store handle, and (3) is
// a keyed rewrite of existing rows rather than a file move, so it stays an
// explicit step (see docs/issues/skill-name-migration.md).
//
// Everything is idempotent and refuses to guess: a skill whose frontmatter name
// is missing, invalid, or already taken is reported, never renamed.

// ReconcileAction is what a reconcile decided for one skill directory.
type ReconcileAction string

const (
	ActionOK       ReconcileAction = "ok"       // directory already matches the declared name
	ActionRename   ReconcileAction = "rename"   // directory will be (or was) renamed
	ActionConflict ReconcileAction = "conflict" // declared name is already taken by another skill
	ActionSkip     ReconcileAction = "skip"     // no usable declared name — cannot be aligned or published
)

// ReconcileItem is one skill directory's verdict.
type ReconcileItem struct {
	Layer        string          `json:"layer"`
	Owner        string          `json:"owner"`
	Root         string          `json:"root"`
	DirName      string          `json:"dirName"`
	DeclaredName string          `json:"declaredName,omitempty"`
	Action       ReconcileAction `json:"action"`
	Reason       string          `json:"reason,omitempty"`
	// RemoteKeyPrefix is the object-store prefix holding the old name, and
	// RemoteKeyTarget the prefix it must become. Empty for layers that are never
	// mirrored (extra / bundled / user seeds).
	RemoteKeyPrefix string `json:"remoteKeyPrefix,omitempty"`
	RemoteKeyTarget string `json:"remoteKeyTarget,omitempty"`
	// ConfigPaths lists the configs_kv paths that name this skill today and
	// would therefore have to be rewritten with the new name.
	ConfigPaths []string `json:"configPaths,omitempty"`
}

// ReconcileReport is the whole run, in the order the directories were read.
type ReconcileReport struct {
	Applied   bool            `json:"applied"`
	Items     []ReconcileItem `json:"items"`
	Renamed   int             `json:"renamed"`
	Conflicts int             `json:"conflicts"`
	Skipped   int             `json:"skipped"`
}

// ReconcileDir examines one layer root. With dryRun it only reports; otherwise
// it renames the directories that need it. A failed rename stops nothing —
// each skill is independent, and the report says which ones changed.
func ReconcileDir(rootDir, owner, layer string, dryRun bool) (ReconcileReport, error) {
	report := ReconcileReport{}
	if rootDir == "" {
		return report, nil
	}
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, dirName := range names {
		item := ReconcileItem{
			Layer:   layer,
			Owner:   owner,
			Root:    rootDir,
			DirName: dirName,
		}
		declared, reason := ReadSkillName(filepath.Join(rootDir, dirName))
		item.DeclaredName = declared
		switch {
		case declared == "":
			item.Action = ActionSkip
			item.Reason = reason
		case declared == dirName:
			item.Action = ActionOK
		default:
			target := filepath.Join(rootDir, declared)
			if _, statErr := os.Stat(target); statErr == nil {
				item.Action = ActionConflict
				item.Reason = fmt.Sprintf("a skill named %q already exists in this layer", declared)
			} else if !os.IsNotExist(statErr) {
				item.Action = ActionConflict
				item.Reason = statErr.Error()
			} else if dryRun {
				item.Action = ActionRename
				item.Reason = "dry run: directory would be renamed"
			} else if rerr := os.Rename(filepath.Join(rootDir, dirName), target); rerr != nil {
				item.Action = ActionConflict
				item.Reason = "rename failed: " + rerr.Error()
			} else {
				item.Action = ActionRename
				item.Reason = "directory renamed"
			}
		}

		if owner != "" {
			item.RemoteKeyPrefix = owner + "/skills/" + dirName + "/"
			if declared != "" && declared != dirName {
				item.RemoteKeyTarget = owner + "/skills/" + declared + "/"
			}
		}
		item.ConfigPaths = skillConfigPaths(owner, dirName)

		switch item.Action {
		case ActionRename:
			report.Renamed++
		case ActionConflict:
			report.Conflicts++
		case ActionSkip:
			report.Skipped++
		}
		report.Items = append(report.Items, item)
	}
	return report, nil
}

// skillConfigPaths names the configs_kv rows that reference a skill by name, so
// a reconcile run can hand over an exact rewrite list instead of "look for it".
// The configs KV stores one row per dotted path (see internal/kvkeys), so these
// are the prefixes, not wildcards: env keys hang below the entry path.
func skillConfigPaths(owner, skillName string) []string {
	if skillName == "" {
		return nil
	}
	paths := []string{"skills.entries." + skillName}
	if owner != "" && !strings.HasPrefix(owner, "_") {
		// Per-agent entries live under the agent IDs they belong to.
		paths = append(paths, "skills.agent_entries."+owner+"."+skillName)
	}
	paths = append(paths, "skills.always_load[]", "skills.disabled[]")
	return paths
}

// ReconcileLayout runs ReconcileDir over every filesystem layer a caller names,
// lowest precedence first, and merges the reports.
func ReconcileLayout(layers []LayerSpec, dryRun bool) (ReconcileReport, error) {
	merged := ReconcileReport{Applied: !dryRun}
	for _, layer := range layers {
		report, err := ReconcileDir(layer.Dir, layer.Owner, layer.Layer, dryRun)
		if err != nil {
			return merged, err
		}
		merged.Items = append(merged.Items, report.Items...)
		merged.Renamed += report.Renamed
		merged.Conflicts += report.Conflicts
		merged.Skipped += report.Skipped
	}
	return merged, nil
}
