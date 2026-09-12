package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// ConfigProjectionMarker is the completeness marker for one configs row's slice
// of the configs_kv mirror.
//
// Three words, three different things, and they are not interchangeable:
// configs_kv is the *data* (one row per leaf); "the projection" is the
// *relation* between a blob row and the leaves derived from it — the dual-write
// flattens one blob row into one configs_kv row per leaf (see
// scope.flattenJSONToKV); and this type is the *marker*, the record that such a
// projection happened and covered the whole row.
//
// The marker exists because a projection can be *incomplete* — a row written
// before the mirror existed, a hand-edited row, or a writer that never saw the
// whole blob leaves a subset behind, and from the rows alone a subset is
// indistinguishable from a whole. That is the shape the `web_search` outage
// took: once reads preferred the mirror, a subset silently truncated a
// namespace.
//
// This marker makes completeness a fact someone wrote down instead of a
// property a reader infers. It is recorded in the same transaction as the rows
// it covers, so "the marker exists" means "a dual-write emitted every leaf of
// this row"; and it carries a fingerprint of those leaves, so a later reader
// also catches rows that changed after the marker was written (manual SQL, a
// half-done repair). A row with no marker is not certified, and a reader that
// would otherwise prefer the mirror must treat it as incomplete.
//
// It is deliberately not a configs_kv row: a marker is metadata about the
// projection, not a leaf of it, and it must not surface in a prefix scan.
type ConfigProjectionMarker struct {
	// Prefix is the configs_kv name prefix this marker covers ("openai.",
	// "prefs.", "agent."). Stored so verification does not have to rebuild
	// the namespace -> prefix mapping, which has a rename in it
	// (agents.defaults -> agent.).
	Prefix string
	// KeyCount is the number of leaves the writer emitted.
	KeyCount int
	// Fingerprint is ConfigsKVFingerprint over those leaves. Empty means the
	// marker certifies nothing and VerifyProjectionMarker rejects it.
	Fingerprint string
	// Enabled is the configs row's on/off decision — the half of a row's
	// read state that is not a leaf. It is a pointer because it has three
	// values, not two: a marker written before configs_mirror carried the
	// column recorded the leaves but not the decision, and neither boolean
	// is then the row's answer. VerifyProjectionMarker rejects such a marker, the
	// same way it rejects an empty fingerprint, rather than defaulting it.
	// Re-running store.ReconcileConfigProjections or any dual-write records it.
	Enabled *bool
}

// NewConfigProjectionMarker builds the marker for a projection: the prefix the
// leaves live under, the row's enabled decision, how many there are, and their
// fingerprint.
func NewConfigProjectionMarker(prefix string, enabled bool, leaves map[string]ConfigValue) ConfigProjectionMarker {
	return ConfigProjectionMarker{
		Prefix:      prefix,
		KeyCount:    len(leaves),
		Fingerprint: ConfigsKVFingerprint(leaves),
		Enabled:     &enabled,
	}
}

// ConfigsKVFingerprint hashes a leaf set into a stable string. It is
// order-independent (the input is a map) and sensitive to every part of a row
// — name, value and value_kind — so adding, dropping, renaming or editing any
// leaf changes it.
func ConfigsKVFingerprint(leaves map[string]ConfigValue) string {
	keys := make([]string, 0, len(leaves))
	for k := range leaves {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		v := leaves[k]
		// NUL cannot appear in a config name and newline separates records,
		// so ("a", "b") cannot hash the same as ("a\x00b", "").
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(v.Value))
		h.Write([]byte{0})
		h.Write([]byte(v.Kind))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ProjectionMarkerSelfConsistent reports whether the marker attests to exactly these
// leaves, judged against the marker alone — no blob to compare against.
//
// It is the half of verification a mirror-first reader can run: the migration
// target is "the blob need not be read to answer", so the check cannot depend
// on the blob's enabled flag. What it still enforces is the completeness the
// marker exists for — the fingerprint covers every leaf (name, value and
// value_kind) and the count agrees, so a subset, a rename or an edit made
// after the marker was written fails it. A marker with no fingerprint, or one
// written before the marker carried the enabled flag, records no decision at
// all and returns false: "not certified" is the answer, not a defaulted one.
//
// A caller that trusts the mirror calls this with the leaves it just read; a
// false answer means "do not serve this row from the mirror", not "the rows
// are wrong".
func ProjectionMarkerSelfConsistent(m ConfigProjectionMarker, leaves map[string]ConfigValue) bool {
	if m.Fingerprint == "" || m.Enabled == nil {
		return false
	}
	return m.KeyCount == len(leaves) && m.Fingerprint == ConfigsKVFingerprint(leaves)
}

// VerifyProjectionMarker reports whether leaves and enabled are exactly the read
// state the marker attests to. A marker with no fingerprint, or one written
// before the marker carried the enabled flag, certifies nothing and returns
// false.
//
// A caller that trusts the mirror calls this with the row it just read; a
// false answer means "treat this row as missing from the mirror", not "the
// rows are wrong". enabled is the configs row's flag: recording it in the
// marker is what lets a mirror-first reader answer the veto question (a
// disabled row erases outer layers and blocks the fallback) without the blob.
// VerifyProjectionMarker is ProjectionMarkerSelfConsistent plus the one check that needs the
// blob — that the marker's recorded decision is the row's decision.
func VerifyProjectionMarker(m ConfigProjectionMarker, enabled bool, leaves map[string]ConfigValue) bool {
	return ProjectionMarkerSelfConsistent(m, leaves) && *m.Enabled == enabled
}

// ConfigsKVPrefixFor maps a configs row (kind, name) to the configs_kv name prefix
// its leaves live under. It is the storage-layout contract the dual-write, the
// backfill and the reconciler all share, defined once so the three cannot
// drift, and every reader resolves the prefix through it too
// (scope.kvPrefixForNamespace).
//
// The mapping must be injective within a kind: two rows that share a prefix
// share a DeleteConfigPrefix range and a prefix scan, so one would silently
// delete or merge the other's leaves. A plain name maps to "<name>.", so the
// only way to collide with a renamed row is to be named after the stem of its
// prefix — see reservedConfigsKVStems and ValidateConfigName, which is what makes
// that injectivity a checked property rather than a hope.
func ConfigsKVPrefixFor(kind, name string) string {
	if kind == KindSetting {
		if prefix, ok := configsKVRenames[name]; ok {
			return prefix
		}
	}
	return name + "."
}

// configsKVRenames maps a row whose configs_kv prefix is not simply its name onto
// that prefix. It exists because agents.defaults was written under "agent."
// before this layer did, and that layout is live data in every deployed
// database — it cannot be renamed, so the mapping stays here and everyone goes
// through ConfigsKVPrefixFor.
//
// It is a map rather than a branch in ConfigsKVPrefixFor so the invariant above
// is enumerable: TestConfigsKVPrefixIsInjective walks the reserved and ordinary
// names together and proves no two rows of one kind share a prefix.
var configsKVRenames = map[string]string{
	"agents.defaults": "agent.",
}

// reservedConfigsKVStems holds every <name> for which "<name>." is some renamed
// row's prefix. A configs row named one of these would land in the renamed
// row's configs_kv range: DeleteConfigPrefix(kind, scope, scopeID, "agent.")
// would take both rows' leaves with it, and a prefix scan would merge them, so
// ValidateConfigName refuses the name instead.
var reservedConfigsKVStems = map[string]bool{
	"agent": true, // the stem of configsKVRenames["agents.defaults"]
}

// ValidateConfigName refuses a configs row name the configs_kv layout cannot
// represent without a collision (see reservedConfigsKVStems). It is the name-side
// half of ConfigsKVPrefixFor being injective, checked at the write entry point
// (scope.SaveSetting) so a bad name is a rejected write rather than a silently
// shared prefix.
func ValidateConfigName(kind, name string) error {
	if kind != KindSetting {
		return nil
	}
	if reservedConfigsKVStems[name] {
		return fmt.Errorf("config name %q is reserved: configs_kv rows for the %q namespace live under the %q prefix, so this name would share that prefix and one write would overwrite the other",
			name, "agents.defaults", "agent.")
	}
	return nil
}

// projectionGapKeys is how many differing leaf names a gap report keeps per list —
// enough to diagnose one row, not enough to drown a log line.
const projectionGapKeys = 8

// ConfigProjectionGap is one configs row whose configs_kv projection is not a
// complete, unmodified mirror of the blob.
type ConfigProjectionGap struct {
	Kind     string
	Scope    string
	ScopeID  string
	Name     string
	WantKeys int
	GotKeys  int
	// Missing is in the blob projection but not the mirror; Extra is the other
	// way round; Changed is present in both but differs in value or value_kind.
	// Each is capped at projectionGapKeys.
	Missing []string
	Extra   []string
	Changed []string
}

// ConfigProjectionReconcile is the outcome of ReconcileConfigProjections.
type ConfigProjectionReconcile struct {
	// Examined is how many configs rows have a KV projection (provider /
	// setting / plugin_enabled).
	Examined int
	// Certified is how many rows matched the blob (exactly, or after only a
	// value_kind backfill) and are now marked.
	Certified int
	// Untyped is how many certified rows needed that backfill first: their
	// values already matched, only the value_kind tag was missing.
	Untyped int
	// Retagged is how many leaves gained a value_kind during the pass.
	Retagged int
	// Repaired is how many diverged rows --repair re-projected from the blob.
	// Zero unless repair was requested.
	Repaired int
	// Rewritten is how many leaves --repair wrote while re-projecting.
	Rewritten int
	// Gaps are rows whose mirror does not match the blob. They are left (or
	// made) uncertified, so a mirror-first reader falls back to the blob.
	Gaps []ConfigProjectionGap
}

// projectionDelta classifies how a stored mirror differs from the blob projection.
type projectionDelta int

const (
	// projectionExact: same names, values and value_kind.
	projectionExact projectionDelta = iota
	// projectionUntypedOnly: same names and values; at least one row carries no
	// value_kind because it predates the column. Recordable, not a gap.
	projectionUntypedOnly
	// projectionDiverged: anything else — a missing/extra leaf, a different value,
	// or a stored type that contradicts the blob.
	projectionDiverged
)

// classifyConfigProjection compares a blob projection (want) against the stored
// mirror (got). want is always tagged — the flattener tags every leaf — so a
// stored row with no tag is the pre-value_kind case, and its type is knowable
// from the blob rather than guessed. It also returns the leaves that need their
// tag filled when the verdict is projectionUntypedOnly.
func classifyConfigProjection(want, got map[string]ConfigValue) (projectionDelta, []string) {
	if len(want) != len(got) {
		return projectionDiverged, nil
	}
	var untagged []string
	for k, wv := range want {
		gv, ok := got[k]
		if !ok || gv.Value != wv.Value {
			return projectionDiverged, nil
		}
		switch {
		case gv.Kind == wv.Kind:
			// exact for this leaf
		case gv.Kind == "":
			// pre-value_kind row: text matches, tag missing
			untagged = append(untagged, k)
		default:
			// a stored type that contradicts the blob is a real divergence
			return projectionDiverged, nil
		}
	}
	if len(untagged) > 0 {
		return projectionUntypedOnly, untagged
	}
	return projectionExact, nil
}

// projectionGap builds the diagnostic for one mismatched row and caps the leaf-name
// lists so a single bad namespace cannot flood the report.
func projectionGap(kind, scope, scopeID, name string, want, got map[string]ConfigValue) ConfigProjectionGap {
	g := ConfigProjectionGap{
		Kind: kind, Scope: scope, ScopeID: scopeID, Name: name,
		WantKeys: len(want), GotKeys: len(got),
	}
	for k, wv := range want {
		gv, ok := got[k]
		switch {
		case !ok:
			g.Missing = append(g.Missing, k)
		case gv != wv:
			g.Changed = append(g.Changed, k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			g.Extra = append(g.Extra, k)
		}
	}
	sort.Strings(g.Missing)
	sort.Strings(g.Extra)
	sort.Strings(g.Changed)
	g.Missing = capKeys(g.Missing)
	g.Extra = capKeys(g.Extra)
	g.Changed = capKeys(g.Changed)
	return g
}

func capKeys(keys []string) []string {
	if len(keys) > projectionGapKeys {
		return keys[:projectionGapKeys]
	}
	return keys
}
