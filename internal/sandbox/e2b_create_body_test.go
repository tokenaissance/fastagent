package sandbox

// The create body decides what a routine timeout means. Getting these fields
// wrong does not fail loudly — it just puts the whole rebuild path back on the
// happy path, which is what produced "instance 多创建好多个" in production.

import (
	"encoding/json"
	"testing"
	"time"
)

func TestE2BCreateBodyPausesInsteadOfKilling(t *testing.T) {
	var body map[string]any
	if err := json.Unmarshal(e2bCreateBody("tpl-x", 30*time.Minute), &body); err != nil {
		t.Fatalf("create body is not valid JSON: %v", err)
	}

	if body["templateID"] != "tpl-x" {
		t.Fatalf("templateID = %v, want tpl-x", body["templateID"])
	}
	if body["timeout"] != float64(1800) {
		t.Fatalf("timeout = %v, want 1800 seconds", body["timeout"])
	}

	// The three that move an expiry off the failure path.
	if body["autoPause"] != true {
		t.Fatal("autoPause must be on: without it an expiry kills the sandbox and costs a rebuild")
	}
	if body["autoPauseMemory"] != true {
		t.Fatal("autoPauseMemory must stay true: a filesystem-only snapshot cannot be woken by traffic")
	}
	resume, ok := body["autoResume"].(map[string]any)
	if !ok {
		t.Fatalf("autoResume = %#v, want an object (the API takes {enabled: bool})", body["autoResume"])
	}
	if resume["enabled"] != true {
		t.Fatal("autoResume must be enabled so activity wakes a paused sandbox")
	}

	// secure: true is a security decision, not a default: only secure sandboxes
	// get an envd access token, and without one anyone holding the sandbox id —
	// which we hand out in preview URLs — can run commands in it.
	if body["secure"] != true {
		t.Fatal("secure must be true: non-secure sandboxes leave envd open to anyone with the id")
	}
}
