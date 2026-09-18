package skills

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
