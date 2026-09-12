package store

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// TestConfigDataKeepsNumberLiteralsAtRest is the write-side precision
// guarantee, tested from the other end: a row whose digits were written by
// something other than this process (an older build, a hand edit, psql)
// must come back with those digits, and must go back to the column
// unchanged. The old decode turned 9007199254740993 into ...992 on the way
// in and wrote the rounded value on the next save.
func TestConfigDataKeepsNumberLiteralsAtRest(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	const raw = `{"id":9007199254740993,"window":1000000,"ratio":0.1}`
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO configs (id, kind, scope, user_id, agent_id, name, enabled, data)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"cfg-raw", KindProvider, "system", "", "", "raw", true, raw); err != nil {
		t.Fatalf("insert raw row: %v", err)
	}

	rec, err := db.GetConfigByName(ctx, KindProvider, "", "", "raw")
	if err != nil {
		t.Fatalf("GetConfigByName: %v", err)
	}
	for key, want := range map[string]string{
		"id":     "9007199254740993",
		"window": "1000000",
		"ratio":  "0.1",
	} {
		n, ok := rec.Data[key].(json.Number)
		if !ok {
			t.Fatalf("%s = %#v (%T), want json.Number — a float64 here is lost precision",
				key, rec.Data[key], rec.Data[key])
		}
		if n.String() != want {
			t.Fatalf("%s = %s, want %s", key, n, want)
		}
	}

	// Saving it back must not rewrite the digits.
	if err := db.SaveConfig(ctx, rec); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	var stored string
	if err := db.db.QueryRowContext(ctx, `SELECT data FROM configs WHERE id = ?`, "cfg-raw").Scan(&stored); err != nil {
		t.Fatalf("read back column: %v", err)
	}
	if !strings.Contains(stored, "9007199254740993") {
		t.Fatalf("stored data = %s, want the 16-digit literal intact", stored)
	}
}

// TestValueToMapKeepsIntDigits pins the helper every struct→map write now
// goes through. The shape it replaces — json.Marshal into
// map[string]interface{} with the default decode — is what turned an int64
// field into a float64 before the value ever reached a column.
func TestValueToMapKeepsIntDigits(t *testing.T) {
	in := struct {
		Max  int64 `json:"max"`
		Port int   `json:"port"`
	}{Max: math.MaxInt64, Port: 8080}

	m := ValueToMap(in)
	n, ok := m["max"].(json.Number)
	if !ok {
		t.Fatalf("ValueToMap[int64] = %#v (%T), want json.Number", m["max"], m["max"])
	}
	if n.String() != "9223372036854775807" {
		t.Fatalf("max = %s, want the full 19 digits", n)
	}
	if got, ok := m["port"].(json.Number); !ok || got.String() != "8080" {
		t.Fatalf("port = %#v, want json.Number 8080", m["port"])
	}

	// The same value reaches both stores exactly: the blob column verbatim,
	// and the KV mirror through EncodeConfigValue.
	if v := EncodeConfigValue(m["max"]); v.Kind != ValueKindNumber || v.Value != "9223372036854775807" {
		t.Fatalf("EncodeConfigValue(ValueToMap(max)) = %+v, want the tagged literal", v)
	}

	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := db.SaveConfig(ctx, &ConfigRecord{
		Kind: KindProvider, UserID: "", AgentID: "", Name: "structured", Enabled: true, Data: m,
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	rec, err := db.GetConfigByName(ctx, KindProvider, "", "", "structured")
	if err != nil {
		t.Fatalf("GetConfigByName: %v", err)
	}
	if got := rec.Data["max"]; got != json.Number("9223372036854775807") {
		t.Fatalf("round-tripped max = %#v (%T)", got, got)
	}
}

// TestJSONToMapNestedNumbers guard the depth rule: UseNumber has to apply
// inside arrays and objects too, which is where a decoded config puts most
// of its numbers.
func TestJSONToMapNestedNumbers(t *testing.T) {
	m, err := JSONToMap([]byte(`{"models":[{"contextWindow":1000000},{"id":9007199254740993}]}`))
	if err != nil {
		t.Fatalf("JSONToMap: %v", err)
	}
	blob, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(blob) != `{"models":[{"contextWindow":1000000},{"id":9007199254740993}]}` {
		t.Fatalf("round trip = %s, want the input unchanged", blob)
	}
}

// TestJSONObjectOfEmptyObjectIsALeaf pins the rule that keeps an empty object
// representable in a flat mirror. Descending into `{}` emits no leaf, so
// `{"config":{}}` and a map with no "config" key would flatten to the same
// rows — the empty object would be indistinguishable from "no such key", and a
// mirror-first read would answer `nil` for a key the blob holds as `{}`.
//
// Everything that is a JSON object *and carries keys* is still a node to
// descend into; only the empty one is a leaf.
func TestJSONObjectOfEmptyObjectIsALeaf(t *testing.T) {
	if m, ok := JSONObjectOf(map[string]interface{}{}); ok {
		t.Fatalf("JSONObjectOf({}) = (%v, true), want a leaf so the key survives", m)
	}
	if m, ok := JSONObjectOf(map[string]string{}); ok {
		t.Fatalf("JSONObjectOf(map[string]string{}) = (%v, true), want a leaf", m)
	}
	if m, ok := JSONObjectOf(struct{}{}); ok {
		t.Fatalf("JSONObjectOf(struct{}{}) = (%v, true), want a leaf", m)
	}
	if m, ok := JSONObjectOf(map[string]interface{}{"a": nil}); !ok || len(m) != 1 {
		t.Fatalf("JSONObjectOf({\"a\":null}) = (%v, %v), want a one-key node", m, ok)
	}
	// Scalars, arrays and nil were already leaves; unchanged.
	for _, v := range []interface{}{nil, "s", 1, true, []interface{}{}} {
		if m, ok := JSONObjectOf(v); ok {
			t.Fatalf("JSONObjectOf(%#v) = (%v, true), want a leaf", v, m)
		}
	}
}
