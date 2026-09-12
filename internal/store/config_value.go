package store

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// A configs_kv value is stored as TEXT, which cannot say whether the text
// "123" was the string "123" or the number 123. That information is gone by
// the time the row is written, so no amount of validation on read brings it
// back — the column has to carry it. value_kind does: one tag per row, naming
// the JSON type the text is.
//
// The vocabulary is the JSON data model, deliberately not Go's type names.
// JSON is already the type domain of this data — configs.data is a JSON blob
// and encoding/json maps it onto interface{} as exactly these six types — so
// a tag records what the value already was rather than binding the table to
// Go. A reader in any language can act on it, and renaming a Go type never
// becomes a data migration. (A tag naming int64/time.Duration would be that
// binding; "number" is not.)
//
// The tag is a decoding hint, never a constraint. A row whose text doesn't
// match its tag is still returned — see ConfigValue.Decode. Rejecting a value
// loses data, which is the failure this mechanism exists to stop.
const (
	ValueKindString = "string"
	ValueKindNumber = "number"
	ValueKindBool   = "bool"
	ValueKindNull   = "null"
	ValueKindObject = "object"
	ValueKindArray  = "array"
)

// ConfigValue is one configs_kv row payload: the stored text plus the tag
// naming its JSON type.
//
// Kind == "" means the row carries no tag — it was written before value_kind
// existed, by hand, or by an older build. Readers must treat that as unknown
// and fall back to the legacy heuristic, which is why Decode is a method
// rather than a bare switch at each call site.
//
// Rows are always written with a tag: the Store KV methods take a ConfigValue
// rather than a bare string, so a writer cannot silently produce another
// untagged row.
type ConfigValue struct {
	Value string
	Kind  string
}

// EncodeConfigValue maps a JSON-decoded Go value onto the (text, tag) pair
// that reproduces it exactly.
//
// Values arrive here already decoded from JSON (configs.Data is a
// map[string]interface{}), so the cases below are encoding/json's output
// types, plus json.Number for callers that decoded with UseNumber to keep a
// literal intact.
func EncodeConfigValue(v interface{}) ConfigValue {
	switch t := v.(type) {
	case nil:
		// Distinguishable from "" only because the tag rides alongside;
		// that is one of the reasons the tag is a column and not a prefix.
		return ConfigValue{Kind: ValueKindNull}
	case bool:
		return ConfigValue{Value: strconv.FormatBool(t), Kind: ValueKindBool}
	case string:
		return ConfigValue{Value: t, Kind: ValueKindString}
	case json.Number:
		// Verbatim: routing 9223372036854775807 through a float64 gives
		// 9.223372036854776e+18.
		return ConfigValue{Value: t.String(), Kind: ValueKindNumber}
	case float64:
		// 'g' with -1 precision is the shortest form that parses back to
		// the same float64. The old write path used %g for this and then
		// the read path compared the reformatted result against the input,
		// which is why 1e21 came back as the *string* "1e21".
		return ConfigValue{Value: strconv.FormatFloat(t, 'g', -1, 64), Kind: ValueKindNumber}
	case float32:
		return ConfigValue{Value: strconv.FormatFloat(float64(t), 'g', -1, 32), Kind: ValueKindNumber}
	case int:
		return ConfigValue{Value: strconv.FormatInt(int64(t), 10), Kind: ValueKindNumber}
	case int8:
		return ConfigValue{Value: strconv.FormatInt(int64(t), 10), Kind: ValueKindNumber}
	case int16:
		return ConfigValue{Value: strconv.FormatInt(int64(t), 10), Kind: ValueKindNumber}
	case int32:
		return ConfigValue{Value: strconv.FormatInt(int64(t), 10), Kind: ValueKindNumber}
	case int64:
		return ConfigValue{Value: strconv.FormatInt(t, 10), Kind: ValueKindNumber}
	case uint:
		return ConfigValue{Value: strconv.FormatUint(uint64(t), 10), Kind: ValueKindNumber}
	case uint8:
		return ConfigValue{Value: strconv.FormatUint(uint64(t), 10), Kind: ValueKindNumber}
	case uint16:
		return ConfigValue{Value: strconv.FormatUint(uint64(t), 10), Kind: ValueKindNumber}
	case uint32:
		return ConfigValue{Value: strconv.FormatUint(uint64(t), 10), Kind: ValueKindNumber}
	case uint64:
		return ConfigValue{Value: strconv.FormatUint(t, 10), Kind: ValueKindNumber}
	case map[string]interface{}, []interface{}:
		blob, err := json.Marshal(t)
		if err != nil {
			// Unreachable for JSON-decoded input (every one of these types
			// marshals), but a hand-built map can hold a func or a channel.
			// Store the text form as a string rather than dropping the row.
			return ConfigValue{Value: fmt.Sprint(t), Kind: ValueKindString}
		}
		return configValueFromJSON(blob)
	default:
		blob, err := json.Marshal(v)
		if err != nil {
			return ConfigValue{Value: fmt.Sprint(v), Kind: ValueKindString}
		}
		return configValueFromJSON(blob)
	}
}

// StringValue is the tagged-string shorthand, for callers that store an
// opaque text payload rather than a decoded JSON value (a cursor, a token, a
// dot-path) and want to say so without spelling out the tag.
func StringValue(s string) ConfigValue {
	return ConfigValue{Value: s, Kind: ValueKindString}
}

// configValueFromJSON tags just-marshaled JSON text by its first byte. It is
// how the non-scalar and unknown Go types above get a tag without a second
// type switch on the decoded form.
func configValueFromJSON(blob []byte) ConfigValue {
	text := string(blob)
	if len(blob) == 0 {
		return ConfigValue{Value: text, Kind: ValueKindString}
	}
	switch blob[0] {
	case '{':
		return ConfigValue{Value: text, Kind: ValueKindObject}
	case '[':
		return ConfigValue{Value: text, Kind: ValueKindArray}
	case '"':
		var s string
		if err := json.Unmarshal(blob, &s); err != nil {
			return ConfigValue{Value: text, Kind: ValueKindString}
		}
		return ConfigValue{Value: s, Kind: ValueKindString}
	case 't', 'f':
		return ConfigValue{Value: text, Kind: ValueKindBool}
	case 'n':
		return ConfigValue{Kind: ValueKindNull}
	default:
		return ConfigValue{Value: text, Kind: ValueKindNumber}
	}
}

// Decode returns the value as the JSON type Kind names.
//
// Numbers decode to json.Number rather than float64. That is the point of the
// tag: float64 cannot hold every int64, and encoding/json marshals a
// json.Number as its raw literal, so the marshal→unmarshal hop in jsonInto
// still lands an exact int64 in an int64 field. A caller that wants a float64
// converts explicitly and thereby writes down where precision is allowed to
// be lost.
//
// An untagged row (Kind == "") falls back to the legacy heuristic verbatim,
// so rows written before value_kind keep reading exactly as they did. An
// unknown tag (a newer build wrote it) does the same — the text is still the
// source of truth, and guessing beats failing.
func (v ConfigValue) Decode() interface{} {
	switch v.Kind {
	case ValueKindString:
		return v.Value
	case ValueKindNumber:
		return json.Number(v.Value)
	case ValueKindBool:
		return v.Value == "true"
	case ValueKindNull:
		return nil
	case ValueKindObject, ValueKindArray:
		var out interface{}
		if err := json.Unmarshal([]byte(v.Value), &out); err != nil {
			// A malformed tagged row is a write bug, not a read
			// instruction: hand back the text instead of dropping it.
			return v.Value
		}
		return out
	default:
		return decodeLegacyValue(v.Value)
	}
}

// decodeLegacyValue is the pre-value_kind guesser, kept byte-for-byte so
// untagged rows behave exactly as they always have. It is a guess: the column
// alone cannot tell the string "123" from the number 123, so it gets some
// rows wrong (an all-digit api_key became a number and then failed to
// unmarshal into a string field, dropping the field). New rows carry a tag
// precisely so this function stops being consulted.
func decodeLegacyValue(s string) interface{} {
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	// Try JSON array/object.
	if len(s) > 0 && (s[0] == '[' || s[0] == '{') {
		var v interface{}
		if json.Unmarshal([]byte(s), &v) == nil {
			return v
		}
	}
	// Try number — only if the entire string is numeric.
	var f float64
	if _, err := fmt.Sscanf(s, "%g", &f); err == nil {
		// Verify the scan consumed the whole string.
		check := fmt.Sprintf("%g", f)
		if check == s {
			return f
		}
	}
	return s
}
