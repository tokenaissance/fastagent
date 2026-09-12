package store

import (
	"bytes"
	"encoding/json"
)

// A configs row holds JSON text (configs.data). Whoever needs it as a map
// decodes it — and the default decode turns every number into a float64,
// which is a lossy interpretation of a number the row stores exactly.
// "902143" is fine; 9007199254740993 comes back as ...992 and the low digit
// is gone before any tag or type check can see it.
//
// json.Number keeps the literal. It marshals back byte-for-byte, so a map
// that holds json.Number survives every round trip in this package
// (SaveConfig's re-marshal, the configs_kv mirror, an HTTP response body),
// and only the caller that knows a field is a float64 pays the float64
// conversion. That is the same rule ConfigValue.Decode follows for tagged
// rows; this is the configs.data half of it.
//
// The decoder is the only difference from json.Unmarshal, deliberately: the
// bytes on disk stay the source of truth, and the in-memory shape stops
// being where precision dies.

// JSONToMap decodes a JSON object with numbers kept as json.Number literals.
func JSONToMap(blob []byte) (map[string]interface{}, error) {
	var m map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(blob))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// ValueToMap is JSONToMap applied to a Go value: marshal v, decode the
// result. Every call site it replaces was the same two lines — marshal a
// typed config struct into map[string]interface{} so it fits the column —
// and that second step is where an int64 field became a float64.
//
// Returns nil when v cannot be marshaled or is not a JSON object, which is
// what the callers did with the error anyway.
func ValueToMap(v interface{}) map[string]interface{} {
	if v == nil {
		return nil
	}
	blob, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	m, err := JSONToMap(blob)
	if err != nil {
		return nil
	}
	return m
}

// JSONObjectOf reports whether v is a JSON object — a map or struct that
// marshals to `{...}` — and hands it back as the map[string]interface{} the
// mirror flatteners descend into. Numbers come back as json.Number literals,
// same as every other decode in this package.
//
// The flatteners decide where a leaf boundary is by *structure*, not by
// concrete Go type. An earlier version asserted `v.(map[string]interface{})`,
// so anything else that is still a JSON object — a map[string]string, a
// map[string]SomeCfg, a plain struct — was not descended into and went to disk
// as one object-valued leaf. That is the "collapse" that put
// `tools.providers.searxng` in configs_kv where the mirror expects
// `tools.providers.searxng.endpoint`. Both flatteners (the write side in
// internal/scope and the mirror used by reconcile) call this, so they
// cannot disagree about where a leaf boundary is.
//
// Returns false for scalars, arrays and nil; those stay leaves.
//
// An EMPTY object is one of those leaves, and that is the subtle half of the
// rule. Descending into `{}` yields no leaf at all, so `{"config":{}}` and a
// map with no "config" key would flatten to the same row set — the empty
// object is *unrepresentable*, and a mirror-first read would answer "no such
// key" for a key the blob holds. Writing `{}` as one object-valued leaf keeps
// the two distinguishable: `{}` and absent stop being the same mirror. (An
// empty array never had this problem — arrays were always leaves.)
func JSONObjectOf(v interface{}) (map[string]interface{}, bool) {
	// Already-decoded JSON objects (the common case, and the only shape the
	// mirror side ever sees) skip the marshal/unmarshal round trip.
	m, ok := v.(map[string]interface{})
	if !ok {
		m = ValueToMap(v)
	}
	if len(m) == 0 {
		return nil, false
	}
	return m, true
}
