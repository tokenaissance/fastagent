package scope

// Provider KV rows are the one family whose *prefix* is a user-supplied data
// segment. flattenJSONToKV emits "<name>.<struct field>": the name goes into
// the key verbatim (kvPrefix = name + ".") and is never shown to
// kvkeys.StoredSegment, while every segment below it is a Go struct field and
// converts normally.
//
// That split is only safe while config.ProviderConfig exposes nothing but
// struct fields down to its leaves. A free-form map (or interface{}) field
// would make the flattener descend into user data and re-case its keys —
// exactly the web_search failure mode, one namespace over. These tests pin
// the invariant from both ends: a real round trip through the mirror, and a
// shape guard that fails the moment a map is added to the struct.

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/kvkeys"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestProviderKVRoundTripThroughConfigsKV(t *testing.T) {
	db := openScopeDBNamed(t, "provider_data_key")
	defer db.Close()
	ctx := context.Background()

	// Underscore in the name: the prefix must survive verbatim (a casing
	// rewrite here would rename the provider on read).
	const name = "my_vendor"
	want := config.ProviderConfig{
		APIKey:   "sk-abc",
		APIBase:  "https://api.example.com/v1",
		APIType:  "openai",
		AuthType: "bearer",
		Models:   []config.ModelEntry{{ID: "web_search_3", ContextWindow: 128000}},
	}
	if err := SaveProvider(ctx, db, "user-a", "", name, want); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	rows, err := db.ListConfigValues(ctx, store.KindProvider, User, "user-a", "")
	if err != nil {
		t.Fatalf("ListConfigValues: %v", err)
	}
	got := make([]string, 0, len(rows))
	for k := range rows {
		got = append(got, k)
	}
	sort.Strings(got)
	wantRows := []string{
		"my_vendor.api_base",
		"my_vendor.api_key",
		"my_vendor.api_type",
		"my_vendor.auth_type",
		"my_vendor.models",
	}
	if !reflect.DeepEqual(got, wantRows) {
		t.Fatalf("provider mirror rows = %v, want %v", got, wantRows)
	}

	// Drop the authoritative blob row: the mirror is now the only source,
	// so a re-casing bug in the mirror cannot be masked by the blob.
	rec, err := db.GetConfigByName(ctx, store.KindProvider, "user-a", "", name)
	if err != nil {
		t.Fatalf("GetConfigByName: %v", err)
	}
	if err := db.DeleteConfig(ctx, rec.ID); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}

	provs, err := Providers(ctx, db, "user-a", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if _, ok := provs[name]; !ok {
		t.Fatalf("provider %q missing from the mirror: %#v", name, provs)
	}
	if !reflect.DeepEqual(provs[name], want) {
		t.Fatalf("mirror mirror = %#v, want %#v", provs[name], want)
	}
}

// The segment rules the round trip relies on, stated directly: below a
// provider name, camelCase struct fields fold on the way in and unfold on the
// way out. Pinned separately so a change to dataPaths/openPaths that swallows
// the provider namespace fails with a pointed message.
func TestProviderKVSegmentRules(t *testing.T) {
	name := []string{"my_vendor"}
	for _, c := range []struct{ in, stored string }{
		{"apiKey", "api_key"},
		{"apiBase", "api_base"},
		{"apiType", "api_type"},
		{"authType", "auth_type"},
	} {
		if got := kvkeys.StoredSegment(name, c.in); got != c.stored {
			t.Fatalf("StoredSegment(%s) = %q, want %q", c.in, got, c.stored)
		}
		if got := kvkeys.RestoredSegment(name, c.stored); got != c.in {
			t.Fatalf("RestoredSegment(%s) = %q, want %q", c.stored, got, c.in)
		}
	}
	// The name itself is not a key kvkeys converts — the writer glues it on.
	// A name that needed folding would be a bug, which is why the charset is
	// restricted (see ValidateProviderName).
	if kvkeys.IsDataKey(name) {
		t.Fatalf("IsDataKey(%v) = true; the provider name must not be routed through the segment converters", name)
	}
}

// TestProviderConfigHasNoFreeFormMaps is the guard: it walks the struct the
// flattener serializes and fails on any map or interface field that the
// flattener would recurse into and whose keys it would re-case. Slices are
// leaves here (the flattener stores the whole array as one opaque row), which
// is why []ModelEntry is fine and []map[string]string would still be fine —
// but a bare map is not.
func TestProviderConfigHasNoFreeFormMaps(t *testing.T) {
	var walk func(path string, typ reflect.Type)
	seen := map[reflect.Type]bool{}
	walk = func(path string, typ reflect.Type) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.PkgPath != "" { // unexported: encoding/json ignores it
				continue
			}
			field := path + "." + f.Name
			switch f.Type.Kind() {
			case reflect.Map, reflect.Interface:
				t.Errorf("config.ProviderConfig%s is a %s; the configs_kv flattener recurses into it and camel-folds its keys. "+
					"Register the path in kvkeys.dataPaths/openPaths (or pin the flatten behavior) before adding it.",
					field, f.Type.Kind())
			case reflect.Struct:
				walk(field, f.Type)
			}
		}
	}
	walk("ProviderConfig", reflect.TypeOf(config.ProviderConfig{}))
}
