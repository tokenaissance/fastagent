package skills

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogHandlerServesTheContract(t *testing.T) {
	layer := t.TempDir()
	writeCatalogSkill(t, layer, "refunds", "---\nname: refunds\ndescription: process refunds\n---\nbody\n",
		map[string]string{"references/GUIDE.md": "guide\n"})

	recorder := httptest.NewRecorder()
	CatalogHandler([]string{layer}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q; a derived catalog must not be cached", got)
	}

	var catalog Catalog
	if err := json.Unmarshal(recorder.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if len(catalog.Skills) != 1 || catalog.Skills[0].Path != "refunds" {
		t.Fatalf("catalog = %+v", catalog)
	}
	if len(catalog.Skills[0].Files) != 2 {
		t.Fatalf("files = %+v; want SKILL.md and the reference", catalog.Skills[0].Files)
	}
}

func TestCatalogHandlerRefusesNonGet(t *testing.T) {
	recorder := httptest.NewRecorder()
	CatalogHandler(nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", recorder.Code)
	}
}

// This body is the delivery point for both consumers of the endpoint - the dashboard
// reads it through the proxy, the MCP egress reads it directly - so the two facts that
// ride beside `skills` are pinned as they go out, not only inside BuildCatalog. A rule
// witnessed in a unit test and absent from the wire is the failure the formal record
// calls an unwitnessed delivery point.
//
// The second half is the empty list. A reader whose `warnings` member is required must
// get `[]`; a missing initialisation sends `null`, which decodes into the same empty
// slice here and into a different value in a typed client.
//
// The empty half carries a skill whose `{baseDir}` sits in a bundled script rather than
// in the manifest: that is where the warning must NOT fire, because no reader resolves
// the token there, so the narrowing has a witness on the wire too — not only inside
// BuildCatalog, which is where a narrowed trigger is easiest to get wrong in silence.
func TestCatalogHandlerCarriesTheCodesAndTheWarnings(t *testing.T) {
	layer := t.TempDir()
	writeCatalogSkill(t, layer, "refunds", "---\nname: refunds\n---\nbody\n", nil)
	writeCatalogSkill(t, layer, "no-frontmatter", "body without a manifest\n", nil)
	writeCatalogSkill(t, layer, "runner", "---\nname: runner\n---\nRun {baseDir}/go.sh\n", nil)

	recorder := httptest.NewRecorder()
	CatalogHandler([]string{layer}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	var wire struct {
		Skills        []CatalogSkill   `json:"skills"`
		Unpublishable []CatalogProblem `json:"unpublishable"`
		Warnings      []CatalogWarning `json:"warnings"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if len(wire.Unpublishable) != 1 || wire.Unpublishable[0].Code != CodeNoName {
		t.Fatalf("unpublishable = %+v; want the frontmatterless skill carrying %s on the wire",
			wire.Unpublishable, CodeNoName)
	}
	if len(wire.Warnings) != 1 || wire.Warnings[0].Code != CodeBaseDirToken || wire.Warnings[0].Path != "runner" {
		t.Fatalf("warnings = %+v; want one %s for runner", wire.Warnings, CodeBaseDirToken)
	}
	if len(wire.Warnings[0].Files) != 1 || wire.Warnings[0].Files[0] != "SKILL.md" {
		t.Fatalf("warning files = %v; want SKILL.md, the file a reader has to open to see the token", wire.Warnings[0].Files)
	}

	plain := t.TempDir()
	writeCatalogSkill(t, plain, "refunds", "---\nname: refunds\n---\nbody\n", nil)
	writeCatalogSkill(t, plain, "scripted", "---\nname: scripted\n---\n\nRun the bundled script.\n",
		map[string]string{"scripts/run.sh": "echo {baseDir}/go.sh\n"})
	recorder = httptest.NewRecorder()
	CatalogHandler([]string{plain}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if body := recorder.Body.String(); !strings.Contains(body, `"warnings":[]`) {
		t.Fatalf("body = %s; want an empty warnings array rather than null, an absent member, or a warning for a token only a script carries", body)
	}
}
