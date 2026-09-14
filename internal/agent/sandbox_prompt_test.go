package agent

// The sandbox module is the only place the model learns what the container is
// like, so its facts have to be true for the backend the agent actually runs on:
//
//   - e2b (the hosted backend) runs every exec through `/bin/bash -c`
//     (internal/sandbox/e2b_executor.go, execOn), so `[[ ]]`, here-strings and
//     process substitution all work there. Telling the model they do not is a
//     false constraint that costs it workarounds it doesn't need.
//   - docker/boxlite run through `/bin/sh -c`, where those three really do fail —
//     the paragraph must survive for them.
//
// The module already knows which backend it is on (it ends by naming it), so the
// quirks paragraph is gated rather than deleted.

import (
	"strings"
	"testing"
)

func sandboxModule(backend string) string {
	cb := &ContextBuilder{sandboxEnabled: true, sandboxBackend: backend}
	return modSandbox(&promptCtx{cb: cb})
}

func TestSandboxPromptShellQuirksFollowTheBackend(t *testing.T) {
	e2b := sandboxModule("e2b")
	if strings.Contains(e2b, "NOT bash") || strings.Contains(e2b, "here-string") {
		t.Fatal("the e2b backend runs /bin/bash: POSIX-only warnings are false there")
	}
	if !strings.Contains(e2b, "E2B") {
		t.Error("the module must still name the backend it is describing")
	}

	docker := sandboxModule("docker")
	for _, want := range []string{"NOT bash", "here-string", "Process substitution"} {
		if !strings.Contains(docker, want) {
			t.Errorf("the docker backend runs /bin/sh — %q must stay", want)
		}
	}
}

// The worked PIL example was an illustration of the two-line rule above it (write
// a file, then exec it), and the graphics paragraph already covers headless
// drawing. Deleting it must not take any rule with it.
func TestSandboxPromptKeepsItsRulesWithoutTheWorkedExample(t *testing.T) {
	mod := sandboxModule("e2b")
	for _, gone := range []string{"Image.new", "draw.ellipse", "pip\", \"install"} {
		if strings.Contains(mod, gone) {
			t.Errorf("the worked example came back (%q)", gone)
		}
	}
	for _, keep := range []struct{ marker, why string }{
		{"/workspace", "where outputs go — the delivery rule depends on it"},
		{"READ-ONLY", "skills are mounted read-only; writing there silently vanishes"},
		{"Host paths", "host paths do not exist in the sandbox (real incident)"},
		{"write_file", "multi-line code must go through a file, not a one-liner"},
		{"headless", "no display: the browser skill is the only path for pages"},
	} {
		if !strings.Contains(mod, keep.marker) {
			t.Errorf("the sandbox module lost %q (%s)", keep.marker, keep.why)
		}
	}
}

// The file-delivery rule is the module's most load-bearing paragraph now that the
// exec schema no longer repeats it (docs/prompt-inventory.md): every clause has a
// failure behind it.
func TestFileDeliveryRulesSurviveTheTrim(t *testing.T) {
	mod := sandboxModule("e2b")
	for _, keep := range []struct{ marker, why string }{
		{"code block", "text files are shown inline, not attached"},
		{"never", "base64 inlining is forbidden"},
		{"base64", "…and named, so the model recognises the anti-pattern"},
		{"data:image", "fabricated data URLs render as garbage"},
		{"file saved", "announcing a file without content or path is not a delivery"},
	} {
		if !strings.Contains(mod, keep.marker) {
			t.Errorf("the delivery rule lost %q (%s)", keep.marker, keep.why)
		}
	}
}

func TestSandboxPromptStaysUnderItsBudget(t *testing.T) {
	// Two budgets, because the backends emit different text: e2b is the hosted
	// default (and the one that pays on every request), docker/boxlite add the
	// shell quirks.
	if got := len(sandboxModule("e2b")); got > 3700 {
		t.Fatalf("the e2b sandbox module is %d chars (budget 3700)", got)
	}
	if got := len(sandboxModule("docker")); got > 4050 {
		t.Fatalf("the docker sandbox module is %d chars (budget 4050 — it carries the shell quirks e2b does not)", got)
	}
}
