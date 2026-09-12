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
