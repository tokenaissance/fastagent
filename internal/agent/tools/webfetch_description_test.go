package tools

// The web_search-vs-web_fetch routing rule is stated TWICE in the system prompt
// already — `toolDisciplineContent` (agent mode) and `modChatbotTools` (chatbot
// mode) — and the web_fetch schema restated it a third time, for 1,687 chars of
// JSON sent with every request. The schema keeps only what the call site needs:
// what the tool returns, what the `url` argument must be, and the one behaviour
// the model cannot infer (a URL that already failed this turn is refused).

import (
	"encoding/json"
	"strings"
	"testing"
)

const webFetchSchemaBudget = 900

func webFetchPayload(t *testing.T) string {
	t.Helper()
	r := NewRegistry("", "")
	RegisterWebFetch(r)
	for _, def := range r.Definitions() {
		if def.Function.Name != "web_fetch" {
			continue
		}
		raw, err := json.Marshal(def)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(raw)
	}
	t.Fatal("web_fetch is not registered")
	return ""
}

func TestWebFetchSchemaKeepsCallTimeFacts(t *testing.T) {
	lower := strings.ToLower(webFetchPayload(t))

	for _, want := range []struct{ marker, why string }{
		{"plain text", "what the caller gets back"},
		{"exact", "the url argument must be the real URL, not a snippet or a guess"},
		{"refused", "a URL that already failed this turn is refused — retrying it wastes a round"},
	} {
		if !strings.Contains(lower, want.marker) {
			t.Errorf("web_fetch schema no longer states %q (%s)", want.marker, want.why)
		}
	}
	// The routing rule belongs to the system prompt; a third copy here is what
	// this trim removed, so its return is a failure, not a judgement call.
	for _, gone := range []string{"google.com/search", "web_search first", "camoufox"} {
		if strings.Contains(lower, gone) {
			t.Errorf("web_fetch schema restates the routing rule again (%q)", gone)
		}
	}
}

func TestWebFetchSchemaStaysUnderItsBudget(t *testing.T) {
	if payload := len(webFetchPayload(t)); payload > webFetchSchemaBudget {
		t.Fatalf("web_fetch payload is %d chars (budget %d) — it is sent with every request",
			payload, webFetchSchemaBudget)
	}
}
