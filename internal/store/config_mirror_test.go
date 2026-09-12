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
	m := NewConfigMirror("openai.", leaves)
	if m.KeyCount != 1 || m.Prefix != "openai." {
		t.Fatalf("NewConfigMirror = %+v", m)
	}
	if !VerifyConfigMirror(m, leaves) {
		t.Fatal("marker does not verify its own leaves")
	}

	if VerifyConfigMirror(m, map[string]ConfigValue{}) {
		t.Fatal("marker verified an empty (subset) projection")
	}
	superset := map[string]ConfigValue{
		"openai.api_key": StringValue("sk-1"),
		"openai.extra":   StringValue("x"),
	}
	if VerifyConfigMirror(m, superset) {
		t.Fatal("marker verified a superset projection")
	}

	// A marker with no fingerprint certifies nothing — an uncertified row must
	// never read as complete just because a marker row exists.
	if VerifyConfigMirror(ConfigMirror{Prefix: "openai."}, leaves) {
		t.Fatal("fingerprint-less marker verified leaves")
	}
}
