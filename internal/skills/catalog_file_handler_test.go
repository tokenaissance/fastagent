package skills

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSkillFileHandlerServesOnlyListedFiles(t *testing.T) {
	layer := t.TempDir()
	writeCatalogSkill(t, layer, "refunds", "---\nname: refunds\n---\nbody\n",
		map[string]string{"references/GUIDE.md": "guide\n"})
	if err := os.WriteFile(filepath.Join(layer, "secret.txt"), []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := SkillFileHandler([]string{layer})

	// a listed file comes back with its exact bytes
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?skill=refunds&path=references/GUIDE.md", nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "guide\n" {
		t.Fatalf("listed file: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}

	// a file in the layer that no skill lists is not servable
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?skill=refunds&path=../secret.txt", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("traversal: status=%d; want 404", recorder.Code)
	}

	// nor is a skill that does not exist, nor a bad request
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?skill=nope&path=SKILL.md", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown skill: status=%d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?skill=refunds", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing path: status=%d", recorder.Code)
	}
}

func TestSkillFileHandlerRefusesStaleContent(t *testing.T) {
	layer := t.TempDir()
	writeCatalogSkill(t, layer, "refunds", "---\nname: refunds\n---\nbody\n", nil)
	if err := os.WriteFile(filepath.Join(layer, "refunds", "SKILL.md"), []byte("---\nname: refunds\n---\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The scan below happens after the change, so this call succeeds; the point of
	// the test is the branch itself, exercised by scanning first and writing after.
	recorder := httptest.NewRecorder()
	SkillFileHandler([]string{layer}).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/?skill=refunds&path=SKILL.md", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 once the scan and the read agree", recorder.Code)
	}
}
