package store

import (
	"context"
	"encoding/json"
	"math"
	"testing"
)

// TestEncodeDecodeConfigValueRoundTrip is the core of the typed-value change:
// for every JSON type, what goes in must come back — as the same type, not as
// a string that a reader has to re-guess.
func TestEncodeDecodeConfigValueRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		in       interface{}
		wantText string
		wantKind string
		wantJSON string // Decode()'s JSON form, so json.Number vs float64 doesn't matter
	}{
		{"string", "hello", "hello", ValueKindString, `"hello"`},
		// The two rows the old column could not tell apart. This is the
		// whole point of the tag.
		{"string that looks numeric", "123", "123", ValueKindString, `"123"`},
		{"empty string", "", "", ValueKindString, `""`},
		{"string that looks boolean", "true", "true", ValueKindString, `"true"`},
		{"number", float64(42), "42", ValueKindNumber, `42`},
		{"number zero", float64(0), "0", ValueKindNumber, `0`},
		{"number negative", float64(-7), "-7", ValueKindNumber, `-7`},
		{"number fraction", float64(1.5), "1.5", ValueKindNumber, `1.5`},
		{"int", int(7), "7", ValueKindNumber, `7`},
		{"int64", int64(-9), "-9", ValueKindNumber, `-9`},
		{"json number literal", json.Number("1.50"), "1.50", ValueKindNumber, `1.50`},
		{"bool true", true, "true", ValueKindBool, `true`},
		{"bool false", false, "false", ValueKindBool, `false`},
		{"null", nil, "", ValueKindNull, `null`},
		{"object", map[string]interface{}{"a": float64(1)}, `{"a":1}`, ValueKindObject, `{"a":1}`},
		{"array", []interface{}{float64(1), float64(2)}, `[1,2]`, ValueKindArray, `[1,2]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EncodeConfigValue(c.in)
			if got.Kind != c.wantKind {
				t.Fatalf("EncodeConfigValue(%#v).Kind = %q, want %q", c.in, got.Kind, c.wantKind)
			}
			if got.Value != c.wantText {
				t.Fatalf("EncodeConfigValue(%#v).Value = %q, want %q", c.in, got.Value, c.wantText)
			}
			blob, err := json.Marshal(got.Decode())
			if err != nil {
				t.Fatalf("marshal Decode(): %v", err)
			}
			if string(blob) != c.wantJSON {
				t.Fatalf("Decode() marshals to %s, want %s", blob, c.wantJSON)
			}
		})
	}
}

// TestConfigValueNumberKeepsLargeInt64 is the precision regression. The old
// read path round-tripped through float64 and %g, so a 19-digit id came back
// as the *string* "9223372036854775807" (the reformatted float64 did not
// match the input, so the numeric guess was rejected) — and any value that
// did survive came back with the low digits replaced by zeros.
func TestConfigValueNumberKeepsLargeInt64(t *testing.T) {
	const literal = "9223372036854775807" // math.MaxInt64

	// Written from a kept-intact literal (a caller that decoded with
	// json.Number, or any writer that holds the digits).
	v := EncodeConfigValue(json.Number(literal))
	if v.Kind != ValueKindNumber || v.Value != literal {
		t.Fatalf("encoded %q as %+v, want the literal verbatim", literal, v)
	}

	// The marshal→unmarshal hop every typed config goes through must land
	// the exact int64, not a rounded float.
	blob, err := json.Marshal(v.Decode())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got int64
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal %s into int64: %v", blob, err)
	}
	if got != math.MaxInt64 {
		t.Fatalf("int64 round trip = %d, want %d", got, int64(math.MaxInt64))
	}
}

// TestConfigValueNumberFloat64Boundary documents where precision is allowed
// to go: a writer that hands over a float64 has already lost the digits, and
// no tag can put them back. The tag records the type, it does not recover
// information the caller dropped before the call.
func TestConfigValueNumberFloat64Boundary(t *testing.T) {
	v := EncodeConfigValue(float64(math.MaxInt64))
	if v.Kind != ValueKindNumber {
		t.Fatalf("kind = %q, want %q", v.Kind, ValueKindNumber)
	}
	if v.Value == "9223372036854775807" {
		t.Fatalf("float64 input unexpectedly kept 19 digits: %q", v.Value)
	}
	// Whatever it did keep must still be a valid JSON number that parses.
	var f float64
	if err := json.Unmarshal([]byte(v.Value), &f); err != nil {
		t.Fatalf("encoded text %q is not a valid JSON number: %v", v.Value, err)
	}
}

// TestConfigValueFloatFormatIsValidJSON guards the write path against
// producing text that json.Number refuses to marshal — encoding/json
// validates the literal, and an exponent form it rejects would turn a config
// write into a runtime error.
func TestConfigValueFloatFormatIsValidJSON(t *testing.T) {
	for _, f := range []float64{
		0, -0, 1, -1, 0.1, 1e-7, 1e21, 1e-21, 1234567890123456789, math.MaxFloat64, math.SmallestNonzeroFloat64,
		// The exponent-form boundary: strconv.FormatFloat('g', -1, 64)
		// switches to "1e+06" at 1e6, encoding/json only at 1e21.
		1e6, 1e7, 1e15, 1e20, 1e-5,
	} {
		v := EncodeConfigValue(f)
		if _, err := json.Marshal(v.Decode()); err != nil {
			t.Fatalf("float64 %v encoded as %q then failed to marshal: %v", f, v.Value, err)
		}
		var back float64
		if err := json.Unmarshal([]byte(v.Value), &back); err != nil || back != f {
			t.Fatalf("float64 %v encoded as %q, decoded to %v (err=%v)", f, v.Value, back, err)
		}
	}
}

// TestConfigValueIntegralFloatKeepsJSONIntForm is the second half of the
// precision story. A float64 whose value is integral still has to reach an
// int field on the way back — every settings write goes through
// setup.toMap (struct → marshal → map[string]interface{}), and
// encoding/json decodes numbers there as float64, so this is the normal
// path, not a corner case. contextWindow: 1000000 is a real Gemini entry.
//
// strconv.FormatFloat('g', -1, 64) wrote that as "1e+06", which is a valid
// JSON number but not an integer literal: jsonInto then failed with
// "cannot unmarshal number 1e+06 into Go value of type int64" and the whole
// namespace fell back. The stored text must be what encoding/json itself
// writes, so the tag keeps the value *and* its integer form.
func TestConfigValueIntegralFloatKeepsJSONIntForm(t *testing.T) {
	for _, f := range []float64{1e6, 1e7, 1e15, 1e18, 1.5e7} {
		v := EncodeConfigValue(f)
		if v.Kind != ValueKindNumber {
			t.Fatalf("EncodeConfigValue(%v).Kind = %q, want %q", f, v.Kind, ValueKindNumber)
		}
		want, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("marshal %v: %v", f, err)
		}
		if v.Value != string(want) {
			t.Fatalf("EncodeConfigValue(%v).Value = %q, want %q", f, v.Value, want)
		}

		// The hop that actually broke: jsonInto marshals the rebuilt map and
		// unmarshals it onto the typed struct.
		blob, err := json.Marshal(map[string]interface{}{"contextWindow": v.Decode()})
		if err != nil {
			t.Fatalf("marshal rebuilt map for %v: %v", f, err)
		}
		var dst struct {
			ContextWindow int `json:"contextWindow"`
		}
		if err := json.Unmarshal(blob, &dst); err != nil {
			t.Fatalf("float64 %v stored as %q does not project onto an int field: %v", f, v.Value, err)
		}
		if float64(dst.ContextWindow) != f {
			t.Fatalf("projected %d, want %v", dst.ContextWindow, f)
		}
	}
}

// TestConfigValueNestedNumbersKeepTheirDigits pins that the tag's promise
// holds below the top level too. Decode of an object/array used plain
// json.Unmarshal, which hands back float64 for every nested number — so a
// 19-digit id inside {"id": …} came back with its low digits replaced by
// zeros even though the row text was exact.
func TestConfigValueNestedNumbersKeepTheirDigits(t *testing.T) {
	const literal = "9223372036854775807" // math.MaxInt64
	v := ConfigValue{Value: `{"id":` + literal + `}`, Kind: ValueKindObject}

	blob, err := json.Marshal(v.Decode())
	if err != nil {
		t.Fatalf("marshal decoded object: %v", err)
	}
	var dst struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(blob, &dst); err != nil {
		t.Fatalf("unmarshal %s into int64: %v", blob, err)
	}
	if dst.ID != math.MaxInt64 {
		t.Fatalf("nested int64 = %d (text %s), want %d", dst.ID, blob, int64(math.MaxInt64))
	}
}

// TestDecodeConfigValueUnknownKindFallsBack pins that a tag written by a
// newer build (or a typo) does not make the row unreadable: the text is still
// the source of truth, so decoding guesses rather than failing.
func TestDecodeConfigValueUnknownKindFallsBack(t *testing.T) {
	v := ConfigValue{Value: "42", Kind: "bigint"}
	if got := v.Decode(); got != float64(42) {
		t.Fatalf("Decode() with unknown kind = %#v (%T), want the legacy guess", got, got)
	}
}

// TestDecodeConfigValueMalformedObjectKeepsText pins the "hint, not
// constraint" rule: a tagged object row whose text is not JSON returns the
// text instead of dropping the value.
func TestDecodeConfigValueMalformedObjectKeepsText(t *testing.T) {
	v := ConfigValue{Value: "{not json", Kind: ValueKindObject}
	if got := v.Decode(); got != "{not json" {
		t.Fatalf("Decode() = %#v, want the raw text back", got)
	}
}

// TestDecodeLegacyValue is the untagged-row contract, moved here from the
// scope package along with the heuristic itself: rows written before
// value_kind existed must keep reading exactly as they did.
func TestDecodeLegacyValue(t *testing.T) {
	cases := []struct {
		in   string
		want interface{}
	}{
		{"true", true},
		{"false", false},
		{"42", float64(42)},
		{"[1,2]", []interface{}{float64(1), float64(2)}},
		{`{"a":1}`, map[string]interface{}{"a": float64(1)}},
		{"hello", "hello"},
		{"", ""},
		// The guesses that make the tag necessary: both are indistinguishable
		// from a string in the column.
		{"123456", float64(123456)},
		// Not a whole-string number, so it stays a string — the old
		// behaviour, kept.
		{"9223372036854775807", "9223372036854775807"},
	}
	for _, c := range cases {
		got := ConfigValue{Value: c.in}.Decode() // Kind == "" → legacy path
		gotBlob, _ := json.Marshal(got)
		wantBlob, _ := json.Marshal(c.want)
		if string(gotBlob) != string(wantBlob) {
			t.Fatalf("legacy decode(%q) = %#v (%T), want %#v", c.in, got, got, c.want)
		}
	}
}

// TestConfigsKvValueKindRoundTrip pins that the tag reaches the column and
// comes back, through both the point and the prefix read.
func TestConfigsKvValueKindRoundTrip(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	rows := map[string]ConfigValue{
		"cfg.port":    EncodeConfigValue(float64(8080)),
		"cfg.name":    EncodeConfigValue("123"), // a string that looks numeric
		"cfg.enabled": EncodeConfigValue(true),
		"cfg.nothing": EncodeConfigValue(nil),
		"cfg.list":    EncodeConfigValue([]interface{}{"a"}),
		// Untagged on purpose: a row from before the column existed.
		"cfg.legacy": {Value: "42"},
	}
	for name, v := range rows {
		if err := db.SetConfigValue(ctx, KindSetting, "system", "", name, v); err != nil {
			t.Fatalf("SetConfigValue(%s): %v", name, err)
		}
	}

	for name, want := range rows {
		got, err := db.GetConfigValue(ctx, KindSetting, "system", "", name)
		if err != nil {
			t.Fatalf("GetConfigValue(%s): %v", name, err)
		}
		if got != want {
			t.Fatalf("GetConfigValue(%s) = %+v, want %+v", name, got, want)
		}
	}

	all, err := db.ListConfigValues(ctx, KindSetting, "system", "", "cfg.")
	if err != nil {
		t.Fatalf("ListConfigValues: %v", err)
	}
	for name, want := range rows {
		if got := all[name]; got != want {
			t.Fatalf("ListConfigValues[%s] = %+v, want %+v", name, got, want)
		}
	}

	// The two rows the old schema conflated stay distinguishable after the
	// round trip: "123" the string is not 123 the number.
	if s := all["cfg.name"]; s.Kind != ValueKindString || s.Decode() != "123" {
		t.Fatalf("cfg.name = %+v, want the string 123", s)
	}
	if n := all["cfg.port"]; n.Kind != ValueKindNumber {
		t.Fatalf("cfg.port = %+v, want a tagged number", n)
	}
}

// TestMigrateRetrofitsValueKindBeforeBackfill pins the migration *order*.
// migrateConfigsToKV writes configs_kv rows through SetConfigValue, which
// names the value_kind column — so on a legacy table (created before the
// column) the backfill must not run first, or the migration dies with
// "no such column: value_kind". An empty configs_kv plus at least one
// configs row is enough to reach it, which is exactly what a database that
// predates dual-writing looks like.
func TestMigrateRetrofitsValueKindBeforeBackfill(t *testing.T) {
	db, err := NewDBStore("sqlite", "file:kvvaluemigorder?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Fresh schema, then one row to backfill.
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := db.SaveConfig(ctx, &ConfigRecord{
		Kind: KindProvider, UserID: "u_backfill", Name: "openai",
		Enabled: true, Data: map[string]interface{}{"api_key": "sk-x", "timeout": float64(30)},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	// Rewind configs_kv to the pre-tag shape: column gone, table empty, so
	// the next Migrate has to both retrofit and backfill.
	if _, err := db.db.ExecContext(ctx, `ALTER TABLE configs_kv DROP COLUMN value_kind`); err != nil {
		t.Skipf("sqlite build without ALTER TABLE DROP COLUMN: %v", err)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate on a pre-tag configs_kv: %v", err)
	}
	v, err := db.GetConfigValue(ctx, KindProvider, "user", "u_backfill", "openai.api_key")
	if err != nil {
		t.Fatalf("backfilled row missing: %v", err)
	}
	if v.Value != "sk-x" || v.Kind != ValueKindString {
		t.Fatalf("backfilled row = %+v, want the tagged string sk-x", v)
	}
	if n, err := db.GetConfigValue(ctx, KindProvider, "user", "u_backfill", "openai.timeout"); err != nil ||
		n.Kind != ValueKindNumber {
		t.Fatalf("backfilled numeric row = %+v err=%v, want a tagged number", n, err)
	}
}

// TestMigrateConfigsKvValueKindRetrofitsLegacyTable pins the retrofit path: a
// configs_kv created before the column existed (CREATE TABLE IF NOT EXISTS
// leaves it alone) gains the column, its rows stay readable as untagged, and
// running the migration twice is a no-op.
func TestMigrateConfigsKvValueKindRetrofitsLegacyTable(t *testing.T) {
	db, err := NewDBStore("sqlite", "file:kvvaluemig?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// The pre-tag schema, verbatim.
	if _, err := db.db.ExecContext(ctx, `CREATE TABLE configs_kv (
		kind TEXT NOT NULL,
		scope TEXT NOT NULL,
		scope_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		value TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (kind, scope, scope_id, name)
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO configs_kv (kind, scope, scope_id, name, value) VALUES ('setting','system','','theme','dark')`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	if err := db.migrateConfigsKvValueKind(ctx); err != nil {
		t.Fatalf("migrateConfigsKvValueKind: %v", err)
	}
	has, err := db.tableHasColumn(ctx, "configs_kv", "value_kind")
	if err != nil || !has {
		t.Fatalf("value_kind column missing after migration (has=%v err=%v)", has, err)
	}

	// The pre-existing row is untagged, so it still decodes the legacy way.
	got, err := db.GetConfigValue(ctx, KindSetting, "system", "", "theme")
	if err != nil {
		t.Fatalf("GetConfigValue after migration: %v", err)
	}
	if got.Kind != "" || got.Value != "dark" {
		t.Fatalf("legacy row = %+v, want untagged \"dark\"", got)
	}

	// Idempotent.
	if err := db.migrateConfigsKvValueKind(ctx); err != nil {
		t.Fatalf("second migrateConfigsKvValueKind: %v", err)
	}
	// And a fresh write now carries a tag on the retrofitted table.
	if err := db.SetConfigValue(ctx, KindSetting, "system", "", "theme", EncodeConfigValue(true)); err != nil {
		t.Fatalf("SetConfigValue on retrofitted table: %v", err)
	}
	if got, _ := db.GetConfigValue(ctx, KindSetting, "system", "", "theme"); got.Kind != ValueKindBool {
		t.Fatalf("row written after retrofit = %+v, want a tagged bool", got)
	}
}
