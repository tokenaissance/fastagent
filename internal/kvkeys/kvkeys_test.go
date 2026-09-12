package kvkeys

import (
	"reflect"
	"testing"
)

func TestIsDataKey(t *testing.T) {
	cases := []struct {
		path string
		segs []string
		want bool
	}{
		{"tool category id", []string{"tools", "categories", "web_search"}, true},
		{"category field", []string{"tools", "categories", "web_search", "primary"}, false},
		{"provider name", []string{"tools", "providers", "searxng"}, true},
		{"provider field", []string{"tools", "providers", "searxng", "endpoint"}, false},
		{"provider option key", []string{"tools", "providers", "my_vendor", "options", "extra_key"}, true},
		{"skill id", []string{"skills", "entries", "web_search_skill"}, true},
		{"skill env var", []string{"skills", "entries", "web_search_skill", "env", "REPLICATE_API_TOKEN"}, true},
		{"skill field", []string{"skills", "entries", "web_search_skill", "enabled"}, false},
		{"plugin id", []string{"plugins", "entries", "my_plugin"}, true},
		{"plugin config key", []string{"plugins", "entries", "my_plugin", "config", "retry_count"}, true},
		{"nested plugin config key", []string{"plugins", "entries", "my_plugin", "config", "headers", "X_API_Key"}, true},
		{"deeper nested plugin config key", []string{"plugins", "entries", "my_plugin", "config", "mcpServers", "my_server", "env", "API_KEY"}, true},
		{"plugin config container", []string{"plugins", "entries", "my_plugin", "config"}, false},
		{"provider option container", []string{"tools", "providers", "my_vendor", "options"}, false},
		{"plugin field", []string{"plugins", "entries", "my_plugin", "enabled"}, false},
		{"team id", []string{"teams", "ops_team"}, true},
		{"team field", []string{"teams", "ops_team", "default_agent"}, false},
		{"nested struct field", []string{"memory", "auto_persist"}, false},
		{"struct field under map key", []string{"objectstore", "s3", "access_key"}, false},
		{"agents.defaults key", []string{"agent", "replicate_api_token"}, false},
		{"wrong namespace", []string{"sandbox", "web_search"}, false},
		{"too shallow", []string{"tools", "categories"}, false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := IsDataKey(c.segs); got != c.want {
				t.Fatalf("IsDataKey(%v) = %v, want %v", c.segs, got, c.want)
			}
		})
	}
}

// TestSegmentRoundTrip pins the property that matters: writing a logical JSON
// path into dotted KV segments and reading it back is the identity — for
// struct fields (camelCase → snake_case → camelCase) AND for data keys
// (verbatim both ways, which is what broke web_search and ALL_CAPS env vars).
func TestSegmentRoundTrip(t *testing.T) {
	paths := [][]string{
		{"tools", "categories", "web_search", "primary"},
		{"tools", "categories", "webSearch", "autoFallback"},
		{"tools", "categories", "web_search", "fallbacks"},
		{"tools", "providers", "my_vendor", "apiKey"},
		{"tools", "providers", "my_vendor", "options", "extra_key"},
		{"skills", "entries", "web_search_skill", "env", "REPLICATE_API_TOKEN"},
		{"skills", "entries", "claude-mem:babysit", "env", "user_agent_x"},
		{"plugins", "entries", "my_plugin", "config", "poll_interval_sec"},
		{"plugins", "entries", "my_plugin", "config", "headers", "X_API_Key"},
		{"plugins", "entries", "my_plugin", "config", "mcpServers", "my_server", "env", "API_KEY"},
		{"plugins", "entries", "my-plugin", "config", "retry_count", "nested_key"},
		{"teams", "ops_team", "defaultAgent"},
		{"memory", "autoPersist", "enabled"},
		{"privacy", "piiScrubbing", "enabled"},
		{"objectstore", "s3", "accessKey"},
		{"objectstore", "s3", "useSSL"},
		{"agent", "maxToolIterations"},
	}
	for _, logical := range paths {
		stored := make([]string, 0, len(logical))
		prefix := make([]string, 0, len(logical))
		for _, seg := range logical {
			s := StoredSegment(prefix, seg)
			stored = append(stored, s)
			prefix = append(prefix, s)
		}
		restored := make([]string, 0, len(logical))
		prefix = prefix[:0]
		for _, seg := range stored {
			r := RestoredSegment(prefix, seg)
			restored = append(restored, r)
			prefix = append(prefix, r)
		}
		if !reflect.DeepEqual(restored, logical) {
			t.Fatalf("round trip %v → stored %v → restored %v", logical, stored, restored)
		}
	}
}

// TestStoredSegmentShapes pins the on-disk spelling so the dev rows stay
// readable: struct fields snake_case, data keys verbatim.
func TestStoredSegmentShapes(t *testing.T) {
	cases := []struct {
		prefix []string
		key    string
		want   string
	}{
		{nil, "apiKey", "api_key"},
		{[]string{"memory"}, "autoPersist", "auto_persist"},
		{[]string{"agent"}, "REPLICATE_API_TOKEN", "replicate_api_token"},
		{[]string{"tools", "categories"}, "web_search", "web_search"},
		{[]string{"tools", "categories"}, "webSearch", "webSearch"},
		{[]string{"skills", "entries", "web_search_skill", "env"}, "REPLICATE_API_TOKEN", "REPLICATE_API_TOKEN"},
	}
	for _, c := range cases {
		if got := StoredSegment(c.prefix, c.key); got != c.want {
			t.Fatalf("StoredSegment(%v, %q) = %q, want %q", c.prefix, c.key, got, c.want)
		}
	}
}

func TestPath(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"tools.categories.", []string{"tools", "categories"}},
		{"agent.", []string{"agent"}},
		{"", nil},
		{".", nil},
		{"skills..entries.", []string{"skills", "entries"}},
	}
	for _, c := range cases {
		if got := Path(c.in); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("Path(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
