package setup

// The writer half of register rows 52 (`memory.autoPersist`), 51
// (`skillsLearner`) and 49 (`privacy.piiScrubbing`): the Runtime page in the
// fastagent webui.
//
// Both rows are read at agent-build time, and before this page existed their
// only writer was a hand-made POST /api/config — register row 52's audit found
// the reader chain witnessed end to end while the control an operator would
// look for did not exist anywhere. The page is super_admin-only, and
// scopeForSave maps a super_admin that is not acting-as to SCOPE.System, which
// is the scope every user space inherits from (the same reason the sandbox
// block is saved from that page).
//
// This drives the REAL handlers and a real DBStore, POSTs the exact body the
// page sends, and then reads it back twice: through GET /api/config (what the
// page renders on its next load) and through the same typed decode the
// gateway's assembleConfig performs (scope.SettingInto at system scope), so
// "the control can set the row" and "the row reaches its reader" are pinned in
// one place.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
)

// runtimePageMemoryLearningBody is the payload the Runtime page sends for these
// three namespaces (web/src/app/settings/runtime/page.tsx): the switches on, a
// cadence of 2, and a tool-call floor of 4.
const runtimePageMemoryLearningBody = `{"memory":{"autoPersist":{"enabled":true,"everyNTurns":2}},"skillsLearner":{"enabled":true,"minToolCalls":4},"privacy":{"piiScrubbing":{"enabled":true}}}`

func TestRuntimePage_CanSetMemoryAndSkillLearning(t *testing.T) {
	s, _, _ := setupFileUploadTest(t)
	ctx := context.Background()

	rec := httptest.NewRecorder()
	s.handleUpdateConfig(rec, stampSystemAdmin(httptest.NewRequest(
		http.MethodPost, "/api/config", strings.NewReader(runtimePageMemoryLearningBody))))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/config = %d (%s)", rec.Code, rec.Body.String())
	}

	// ── 1. GET returns both namespaces — what the page shows next time it loads.
	rec = httptest.NewRecorder()
	s.handleGetConfig(rec, stampSystemAdmin(httptest.NewRequest(http.MethodGet, "/api/config", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d (%s)", rec.Code, rec.Body.String())
	}
	body := decodeConfigBody(t, rec)
	if got := digPath(t, body, []string{"memory", "autoPersist", "enabled"}); got != true {
		t.Errorf("memory.autoPersist.enabled read back as %#v", got)
	}
	if got := digPath(t, body, []string{"memory", "autoPersist", "everyNTurns"}); got != float64(2) {
		t.Errorf("memory.autoPersist.everyNTurns read back as %#v", got)
	}
	if got := digPath(t, body, []string{"skillsLearner", "enabled"}); got != true {
		t.Errorf("skillsLearner.enabled read back as %#v", got)
	}
	if got := digPath(t, body, []string{"skillsLearner", "minToolCalls"}); got != float64(4) {
		t.Errorf("skillsLearner.minToolCalls read back as %#v", got)
	}
	if got := digPath(t, body, []string{"privacy", "piiScrubbing", "enabled"}); got != true {
		t.Errorf("privacy.piiScrubbing.enabled read back as %#v", got)
	}

	// ── 2. The reader. This is literally the call the gateway makes while
	// assembling a user space (scope.SettingInto over the system ← user ← agent
	// chain), so a value that survives this hop is a value the Manager hands to
	// newAgentWithActor — the hop register row 52 was about.
	var mem config.MemoryCfg
	if err := scope.SettingInto(ctx, s.dataStore, "memory", "", "", &mem); err != nil {
		t.Fatalf("typed memory read: %v", err)
	}
	if !mem.AutoPersist.Enabled || mem.AutoPersist.EveryNTurns != 2 {
		t.Errorf("the memory row did not survive into the typed read the gateway uses: %+v", mem.AutoPersist)
	}
	var learner config.SkillsLearnerCfg
	if err := scope.SettingInto(ctx, s.dataStore, "skillsLearner", "", "", &learner); err != nil {
		t.Fatalf("typed skillsLearner read: %v", err)
	}
	if !learner.Enabled || learner.MinToolCalls != 4 {
		t.Errorf("the skillsLearner row did not survive into the typed read: %+v", learner)
	}
	var privacy config.PrivacyCfg
	if err := scope.SettingInto(ctx, s.dataStore, "privacy", "", "", &privacy); err != nil {
		t.Fatalf("typed privacy read: %v", err)
	}
	if !privacy.PIIScrubbing.Enabled {
		t.Errorf("the privacy row did not survive into the typed read the gateway uses: %+v", privacy)
	}
}
