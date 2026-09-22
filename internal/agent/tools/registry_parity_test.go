package tools

// Nine tools are registered twice: once for the host path (registerFile and
// friends) and once by SetExecutor for the sandbox. The cloud deployment takes
// the second copy, so a fix that lands on the first one ships to nobody.
//
// That is not hypothetical. On 2026-09-22 write_file's description gained the
// "write it in pieces" sentence on the host copy only, on the same day the
// sandbox copy was being blamed for a truncated call. exec shows the same thing
// older: `stdin` lost its JSON example and `allow_long_wait` its consequence on
// whichever copy the edit did not name.
//
// These cases compare the two registries key by key and allow only differences
// that are deliberate AND named here. The list is the point: a tenth difference
// has to be added on purpose.
//
// Falsification: edit any shared description or parameter on one registration
// only and the case fails naming that key. Before the fix that follows, this
// tree fails on exec's stdin / run_in_background / allow_long_wait.
//
// Two differences are allowed, key by key:
//   - the `path` hint, which the two paths genuinely describe from different
//     vantages (identity files only exist on the sandbox-shaped registry) and
//     which the file schemas take as an argument to one builder;
//   - exec's description and its host-only `sandbox` switch, where the two
//     registrations are describing different machines on purpose.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// parityExecutor exists only so SetExecutor takes the sandbox branch.
type parityExecutor struct{}

func (parityExecutor) Exec(context.Context, string, time.Duration) (string, error) { return "", nil }
func (parityExecutor) ReadFile(context.Context, string) (string, error)            { return "", nil }
func (parityExecutor) WriteFile(context.Context, string, string) (string, error)   { return "", nil }
func (parityExecutor) ListDir(context.Context, string) (string, error)             { return "", nil }
func (parityExecutor) Backend() string                                             { return "parity" }
func (parityExecutor) Close() error                                                { return nil }

var _ sandbox.Executor = parityExecutor{}

// allowedDifferences maps a tool to the exact keys the two registrations may
// disagree on, with the reason. "description" is the tool description; any other
// key is a property name. Every entry is a decision someone made on purpose.
var allowedDifferences = map[string]map[string]string{
	"exec": {
		"description": "the two copies describe different machines: the host closure runs on the operator's box, the sandbox one runs in the container",
		"sandbox":     "the force-sandbox switch only means something where there is somewhere else to run",
	},
}

// pathHint is the one parameter whose text is expected to differ, everywhere:
// the two paths describe their own vantage point from one shared builder.
const pathHint = "path"

type toolPayload struct {
	description string
	properties  map[string]interface{}
	required    []string
}

func registryPayloads(t *testing.T, r *Registry) map[string]toolPayload {
	t.Helper()
	out := map[string]toolPayload{}
	for _, def := range r.Definitions() {
		raw, err := json.Marshal(def.Function.Parameters)
		if err != nil {
			t.Fatalf("marshal %s: %v", def.Function.Name, err)
		}
		var params map[string]interface{}
		if err := json.Unmarshal(raw, &params); err != nil {
			t.Fatalf("unmarshal %s: %v", def.Function.Name, err)
		}
		var required []string
		if rs, ok := params["required"].([]interface{}); ok {
			for _, v := range rs {
				required = append(required, v.(string))
			}
		}
		props, _ := params["properties"].(map[string]interface{})
		out[def.Function.Name] = toolPayload{description: def.Function.Description, properties: props, required: required}
	}
	return out
}

// bothRegistries returns what the model sees on each path: the plain registry,
// and the one SetExecutor installs — which is what a cloud turn runs.
func bothRegistries(t *testing.T) (host, sandboxed map[string]toolPayload) {
	t.Helper()
	host = registryPayloads(t, NewRegistry(t.TempDir(), t.TempDir()))

	sbx := NewRegistry(t.TempDir(), t.TempDir())
	sbx.SetExecutor(parityExecutor{})
	sandboxed = registryPayloads(t, sbx)
	return host, sandboxed
}

// reduced drops the keys a tool is allowed to differ on and blanks the path
// hint, so what is left must be byte-identical between the two paths.
func reduced(t *testing.T, tool string, p toolPayload) string {
	t.Helper()
	props := map[string]interface{}{}
	for k, v := range p.properties {
		if _, allowed := allowedDifferences[tool][k]; allowed {
			continue
		}
		props[k] = v
	}
	if path, ok := props[pathHint].(map[string]interface{}); ok {
		clone := map[string]interface{}{}
		for k, v := range path {
			clone[k] = v
		}
		clone["description"] = "<path-hint>"
		props[pathHint] = clone
	}
	out := map[string]interface{}{"properties": props, "required": p.required}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func toolNames(m map[string]toolPayload) []string {
	var names []string
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func TestBothRegistriesOfferTheSameTools(t *testing.T) {
	host, sandboxed := bothRegistries(t)
	var onlyHost, onlySandbox []string
	for _, n := range toolNames(host) {
		if _, ok := sandboxed[n]; !ok {
			onlyHost = append(onlyHost, n)
		}
	}
	for _, n := range toolNames(sandboxed) {
		if _, ok := host[n]; !ok {
			onlySandbox = append(onlySandbox, n)
		}
	}
	if len(onlyHost) > 0 || len(onlySandbox) > 0 {
		t.Fatalf("the two paths disagree about which tools exist:\n  host only:    %v\n  sandbox only: %v", onlyHost, onlySandbox)
	}
}

// The one that would have caught 2026-09-22: a whole description that only one
// path carries, which is how a fix ships to nobody.
func TestNoToolDescriptionIsOneSided(t *testing.T) {
	host, sandboxed := bothRegistries(t)
	for _, name := range toolNames(host) {
		if _, allowed := allowedDifferences[name]["description"]; allowed {
			continue
		}
		s, ok := sandboxed[name]
		if !ok {
			continue
		}
		if h := host[name]; h.description != s.description {
			t.Errorf("%s describes itself differently on the two paths, so only one of them ships this wording\n  host:    %q\n  sandbox: %q", name, h.description, s.description)
		}
	}
}

func TestNoToolParameterIsOneSided(t *testing.T) {
	host, sandboxed := bothRegistries(t)
	for _, name := range toolNames(host) {
		s, ok := sandboxed[name]
		if !ok {
			continue
		}
		want := reduced(t, name, host[name])
		if got := reduced(t, name, s); got != want {
			t.Errorf("%s carries different parameters on the two paths, so a caller on one is reading text the other does not have\n  host:    %s\n  sandbox: %s", name, want, got)
		}
	}
}

// The allowed list is what a reader has to be able to see. If a tool starts
// differing somewhere new, this fails even when the difference is harmless —
// because the harmless ones are how the expensive ones arrive.
func TestOnlyTheNamedDifferencesExist(t *testing.T) {
	host, sandboxed := bothRegistries(t)
	var differing []string
	for _, name := range toolNames(host) {
		s, ok := sandboxed[name]
		if !ok {
			continue
		}
		if raw(t, host[name]) != raw(t, s) {
			differing = append(differing, name)
		}
	}
	sort.Strings(differing)

	// Expected: exec (description + switch) and the four schemas whose path hint
	// is per-path by design.
	want := []string{"exec", "list_dir", "read_file", "write_file"}
	if strings.Join(differing, ",") != strings.Join(want, ",") {
		t.Fatalf("payloads differ for %v; want exactly %v (anything else is a copied schema). Named differences: %v", differing, want, allowedDifferences)
	}
}

func raw(t *testing.T, p toolPayload) string {
	t.Helper()
	b, err := json.Marshal(map[string]interface{}{"description": p.description, "properties": p.properties, "required": p.required})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
