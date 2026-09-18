package runtime

import "testing"

// A project has ONE preview — one record, one port, one URL — so it has to have
// one container, whichever entry point asked for it (docs 10 §4 G17, option G).
// Before this rule, the console started the preview in "…:p:<pid>" while the
// agent's turns ran in "…:p:<pid>:s:<chat>", so the dev server the user was
// looking at was the one nobody wrote into.
func TestPreviewSandboxSession(t *testing.T) {
	cases := []struct {
		name      string
		projectID string
		sessionID string
		want      string
	}{
		{"project preview ignores the calling chat", "proj_1", "chat_9", ""},
		{"console start (no chat) is the same slot", "proj_1", "", ""},
		{"a loose chat keeps its own session", "", "chat_9", "chat_9"},
		{"agent-shared scope stays shared", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := previewSandboxSession(tc.projectID, tc.sessionID); got != tc.want {
				t.Fatalf("previewSandboxSession(%q,%q) = %q; want %q",
					tc.projectID, tc.sessionID, got, tc.want)
			}
		})
	}
}
