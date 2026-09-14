package setup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A deployment with no wired runtime manager must still answer GET
// /api/runtime/templates with an empty list rather than a 503/500.
//
// The chat workspace panel's first-boot template picker degrades on an empty
// list ("use the stored/default ref"); a hard failure would instead surface as
// an error banner the moment a project is booted for the first time.
//
// Only the nil-manager branch is reachable in a unit test: `runtimeMgr` is a
// concrete *runtime.Manager, so the populated branch needs a sandbox-backed
// manager (covered by the Runtime suite + the cloud-side proxy tests).
func TestRuntimeTemplatesWithoutManager(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleRuntimeTemplates(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/templates", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (degradation must not be an error)", rec.Code)
	}

	var body struct {
		Templates []string `json:"templates"`
		Default   string   `json:"default"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%q)", err, rec.Body.String())
	}
	if len(body.Templates) != 0 {
		t.Errorf("templates = %v, want empty", body.Templates)
	}
	if body.Default != "" {
		t.Errorf("default = %q, want empty", body.Default)
	}
}
