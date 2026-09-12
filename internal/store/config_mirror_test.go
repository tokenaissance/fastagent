package store

import "testing"

// A marker has to fingerprint a leaf set the same way no matter what order the
// map iterates in, and differently for any change to a leaf — that is the whole
// point of recording one.
func TestMirrorFingerprintIsOrderIndependentAndSensitive(t *testing.T) {
	base := map[string]ConfigValue{
		"openai.api_key":  StringValue("sk-1"),
		"openai.api_base": StringValue("https://api.openai.com"),
	}
	reordered := map[string]ConfigValue{}
	reordered["openai.api_base"] = StringValue("https://api.openai.com")
	reordered["openai.api_key"] = StringValue("sk-1")
	if MirrorFingerprint(base) != MirrorFingerprint(reordered) {
		t.Fatal("fingerprint depends on map iteration order")
	}

	changedValue := map[string]ConfigValue{
		"openai.api_key":  StringValue("sk-2"),
		"openai.api_base": StringValue("https://api.openai.com"),
	}
	if MirrorFingerprint(base) == MirrorFingerprint(changedValue) {
		t.Fatal("fingerprint ignores a changed value")
	}

	// Same text, different tag: a string "1" and the number 1 must not hash
	// the same, or value_kind would not be part of the projection identity.
	changedKind := map[string]ConfigValue{
		"openai.api_key":  StringValue("sk-1"),
		"openai.api_base": {Value: "https://api.openai.com", Kind: ValueKindNull},
	}
	if MirrorFingerprint(base) == MirrorFingerprint(changedKind) {
		t.Fatal("fingerprint ignores a changed value_kind")
	}

	addedKey := map[string]ConfigValue{
		"openai.api_key":  StringValue("sk-1"),
		"openai.api_base": StringValue("https://api.openai.com"),
		"openai.timeout":  StringValue("30"),
	}
	if MirrorFingerprint(base) == MirrorFingerprint(addedKey) {
		t.Fatal("fingerprint ignores an added leaf")
	}
}

// A marker certifies exactly the leaf set it was built from — no more, no less.
func TestVerifyConfigMirror(t *testing.T) {
	leaves := map[string]ConfigValue{"openai.api_key": StringValue("sk-1")}
	m := NewConfigMirror("openai.", true, leaves)
	if m.KeyCount != 1 || m.Prefix != "openai." {
		t.Fatalf("NewConfigMirror = %+v", m)
	}
	if !VerifyConfigMirror(m, true, leaves) {
		t.Fatal("marker does not verify its own leaves")
	}

	if VerifyConfigMirror(m, true, map[string]ConfigValue{}) {
		t.Fatal("marker verified an empty (subset) projection")
	}
	superset := map[string]ConfigValue{
		"openai.api_key": StringValue("sk-1"),
		"openai.extra":   StringValue("x"),
	}
	if VerifyConfigMirror(m, true, superset) {
		t.Fatal("marker verified a superset projection")
	}

	// A marker with no fingerprint certifies nothing — an uncertified row must
	// never read as complete just because a marker row exists.
	if VerifyConfigMirror(ConfigMirror{Prefix: "openai."}, true, leaves) {
		t.Fatal("fingerprint-less marker verified leaves")
	}
}

// MirrorSelfConsistent is the blob-free half of verification: the check a
// mirror-first reader runs against the leaves it just loaded. It must hold
// whenever the marker covers those leaves, regardless of any blob, and fail on
// the same completeness violations VerifyConfigMirror catches.
func TestMirrorSelfConsistent(t *testing.T) {
	leaves := map[string]ConfigValue{"openai.api_key": StringValue("sk-1")}
	m := NewConfigMirror("openai.", true, leaves)
	if !MirrorSelfConsistent(m, leaves) {
		t.Fatal("marker does not certify its own leaves")
	}
	// The decision is the marker's; self-consistency does not consult a blob,
	// so a marker recording "disabled" still certifies its leaves.
	if !MirrorSelfConsistent(NewConfigMirror("openai.", false, leaves), leaves) {
		t.Fatal("a disabled marker did not certify its leaves")
	}

	if MirrorSelfConsistent(m, map[string]ConfigValue{}) {
		t.Fatal("marker certified an empty (subset) projection")
	}
	if MirrorSelfConsistent(m, map[string]ConfigValue{
		"openai.api_key": StringValue("sk-1"),
		"openai.extra":   StringValue("x"),
	}) {
		t.Fatal("marker certified a superset projection")
	}
	if MirrorSelfConsistent(ConfigMirror{Prefix: "openai."}, leaves) {
		t.Fatal("fingerprint-less marker certified leaves")
	}
	unrecorded := NewConfigMirror("openai.", true, leaves)
	unrecorded.Enabled = nil
	if MirrorSelfConsistent(unrecorded, leaves) {
		t.Fatal("a marker with no recorded decision certified its leaves")
	}
}

// enabled is half of what a marker attests to, so the two halves cannot be
// swapped: leaves that match under the wrong decision are not certified, and a
// marker written before the column existed attests to no decision at all.
func TestVerifyConfigMirrorCoversEnabled(t *testing.T) {
	leaves := map[string]ConfigValue{"openai.api_key": StringValue("sk-1")}

	enabledMarker := NewConfigMirror("openai.", true, leaves)
	if VerifyConfigMirror(enabledMarker, false, leaves) {
		t.Fatal("an enabled marker verified a disabled row")
	}
	disabledMarker := NewConfigMirror("openai.", false, leaves)
	if VerifyConfigMirror(disabledMarker, true, leaves) {
		t.Fatal("a disabled marker verified an enabled row")
	}
	if !VerifyConfigMirror(disabledMarker, false, leaves) {
		t.Fatal("a disabled marker did not verify its own decision")
	}

	// The row registry also has to represent a row with no leaves at all — a
	// disabled namespace — which is why the empty projection is a legal,
	// certifiable marker rather than one that gets deleted.
	empty := NewConfigMirror("agent.", false, map[string]ConfigValue{})
	if !VerifyConfigMirror(empty, false, map[string]ConfigValue{}) {
		t.Fatal("an empty disabled projection did not verify")
	}
	if VerifyConfigMirror(empty, true, map[string]ConfigValue{}) {
		t.Fatal("an empty projection verified under the wrong decision")
	}

	// A marker with no recorded decision (written before the column existed)
	// is rejected either way, rather than defaulting to one of the two.
	unrecorded := disabledMarker
	unrecorded.Enabled = nil
	if VerifyConfigMirror(unrecorded, false, leaves) || VerifyConfigMirror(unrecorded, true, leaves) {
		t.Fatal("a marker with no enabled record certified a decision")
	}
}

// MirrorPrefixFor must stay injective within a kind: two rows sharing a prefix
// share a delete range and a prefix scan. The one rename is why the stem it
// occupies is reserved, and ValidateConfigName is what enforces it.
func TestMirrorPrefixIsInjective(t *testing.T) {
	names := []string{"agents.defaults", "prefs", "sandbox", "skills.entries", "agent", "agents"}
	seen := map[string]string{}
	for _, name := range names {
		prefix := MirrorPrefixFor(KindSetting, name)
		if err := ValidateConfigName(KindSetting, name); err != nil {
			if name != "agent" {
				t.Fatalf("ValidateConfigName(%q) rejected a legal name: %v", name, err)
			}
			// A rejected name is allowed to share a prefix precisely because
			// nothing may write it.
			continue
		}
		if other, ok := seen[prefix]; ok {
			t.Fatalf("names %q and %q share the configs_kv prefix %q", other, name, prefix)
		}
		seen[prefix] = name
	}
	if err := ValidateConfigName(KindSetting, "agent"); err == nil {
		t.Fatal("the reserved stem was accepted as a settings namespace")
	}
	if got := MirrorPrefixFor(KindSetting, "agents.defaults"); got != "agent." {
		t.Fatalf("agents.defaults prefix = %q, want agent. (the live layout)", got)
	}
	// Other kinds have no rename, so their prefixes are the plain mapping and
	// the settings reservation does not apply to them.
	if got := MirrorPrefixFor(KindProvider, "agent"); got != "agent." {
		t.Fatalf("provider agent prefix = %q", got)
	}
	if err := ValidateConfigName(KindProvider, "agent"); err != nil {
		t.Fatalf("provider names are validated by ValidateProviderName instead: %v", err)
	}
}
