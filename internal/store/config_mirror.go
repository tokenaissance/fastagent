package store

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// ConfigMirror is the completeness marker for one configs row's slice of the
// configs_kv mirror.
//
// The mirror is a projection of the legacy blob: the dual-write flattens one
// blob row into one configs_kv row per leaf (see scope.flattenJSONToKV). A
// projection can be *incomplete* — a row written before the mirror existed, a
// hand-edited row, or a writer that never saw the whole blob leaves a subset
// behind, and from the rows alone a subset is indistinguishable from a whole.
// That is the shape the `web_search` outage took: once reads preferred the
// mirror, a subset silently truncated a namespace.
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
type ConfigMirror struct {
	// Prefix is the configs_kv name prefix this marker covers ("openai.",
	// "prefs.", "agent."). Stored so verification does not have to rebuild
	// the namespace -> prefix mapping, which has a rename in it
	// (agents.defaults -> agent.).
	Prefix string
	// KeyCount is the number of leaves the writer emitted.
	KeyCount int
	// Fingerprint is MirrorFingerprint over those leaves. Empty means the
	// marker certifies nothing and VerifyConfigMirror rejects it.
	Fingerprint string
}

// NewConfigMirror builds the marker for a projection: the prefix the leaves
// live under, how many there are, and their fingerprint.
func NewConfigMirror(prefix string, leaves map[string]ConfigValue) ConfigMirror {
	return ConfigMirror{
		Prefix:      prefix,
		KeyCount:    len(leaves),
		Fingerprint: MirrorFingerprint(leaves),
	}
}

// MirrorFingerprint hashes a leaf set into a stable string. It is
// order-independent (the input is a map) and sensitive to every part of a row
// — name, value and value_kind — so adding, dropping, renaming or editing any
// leaf changes it.
func MirrorFingerprint(leaves map[string]ConfigValue) string {
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

// VerifyConfigMirror reports whether leaves is exactly the projection the
// marker attests to. A marker with no fingerprint (never written, or written
// by an older build) certifies nothing and returns false.
//
// A caller that trusts the mirror calls this with the rows it just read; a
// false answer means "treat this row as missing from the mirror", not "the
// rows are wrong".
func VerifyConfigMirror(m ConfigMirror, leaves map[string]ConfigValue) bool {
	if m.Fingerprint == "" {
		return false
	}
	return m.KeyCount == len(leaves) && m.Fingerprint == MirrorFingerprint(leaves)
}

// MirrorPrefixFor maps a configs row (kind, name) to the configs_kv name prefix
// its leaves live under. It is the storage-layout contract the dual-write, the
// backfill and the reconciler all share, defined once so the three cannot
// drift. agents.defaults is the single rename: the blob row keeps its dotted
// namespace, its mirror lives under "agent.".
func MirrorPrefixFor(kind, name string) string {
	if kind == KindSetting && name == "agents.defaults" {
		return "agent."
	}
	return name + "."
}

// mirrorGapKeys is how many differing leaf names a gap report keeps per list —
// enough to diagnose one row, not enough to drown a log line.
const mirrorGapKeys = 8

// ConfigMirrorGap is one configs row whose configs_kv projection is not a
// complete, unmodified mirror of the blob.
type ConfigMirrorGap struct {
	Kind     string
	Scope    string
	ScopeID  string
	Name     string
	WantKeys int
	GotKeys  int
	// Missing is in the blob projection but not the mirror; Extra is the other
	// way round; Changed is present in both but differs in value or value_kind.
	// Each is capped at mirrorGapKeys.
	Missing []string
	Extra   []string
	Changed []string
}

// ConfigMirrorReconcile is the outcome of ReconcileConfigMirrors.
type ConfigMirrorReconcile struct {
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
	// Gaps are rows whose mirror does not match the blob. They are left (or
	// made) uncertified, so a mirror-first reader falls back to the blob.
	Gaps []ConfigMirrorGap
}

// mirrorDelta classifies how a stored mirror differs from the blob projection.
type mirrorDelta int

const (
	// mirrorExact: same names, values and value_kind.
	mirrorExact mirrorDelta = iota
	// mirrorUntypedOnly: same names and values; at least one row carries no
	// value_kind because it predates the column. Recordable, not a gap.
	mirrorUntypedOnly
	// mirrorDiverged: anything else — a missing/extra leaf, a different value,
	// or a stored type that contradicts the blob.
	mirrorDiverged
)

// classifyConfigMirror compares a blob projection (want) against the stored
// mirror (got). want is always tagged — the flattener tags every leaf — so a
// stored row with no tag is the pre-value_kind case, and its type is knowable
// from the blob rather than guessed. It also returns the leaves that need their
// tag filled when the verdict is mirrorUntypedOnly.
func classifyConfigMirror(want, got map[string]ConfigValue) (mirrorDelta, []string) {
	if len(want) != len(got) {
		return mirrorDiverged, nil
	}
	var untagged []string
	for k, wv := range want {
		gv, ok := got[k]
		if !ok || gv.Value != wv.Value {
			return mirrorDiverged, nil
		}
		switch {
		case gv.Kind == wv.Kind:
			// exact for this leaf
		case gv.Kind == "":
			// pre-value_kind row: text matches, tag missing
			untagged = append(untagged, k)
		default:
			// a stored type that contradicts the blob is a real divergence
			return mirrorDiverged, nil
		}
	}
	if len(untagged) > 0 {
		return mirrorUntypedOnly, untagged
	}
	return mirrorExact, nil
}

// mirrorGap builds the diagnostic for one mismatched row and caps the leaf-name
// lists so a single bad namespace cannot flood the report.
func mirrorGap(kind, scope, scopeID, name string, want, got map[string]ConfigValue) ConfigMirrorGap {
	g := ConfigMirrorGap{
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
	if len(keys) > mirrorGapKeys {
		return keys[:mirrorGapKeys]
	}
	return keys
}
