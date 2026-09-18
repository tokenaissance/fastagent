package workspace

import "testing"

// ScopeSegments is the layout table, written down once: LocalFS joins it onto a
// root, S3 joins it into an object key. Before this existed the same switch was
// in both backends (docs 10 §4 G23).
func TestScopeSegmentsIsTheLayoutTable(t *testing.T) {
	cases := []struct {
		name      string
		projectID string
		sessionID string
		want      string
	}{
		{"agent-shared", "", "", ""},
		{"loose chat", "", "sess_1", "sessions/sess_1"},
		{"project root", "proj_1", "", "projects/proj_1"},
		{"project chat", "proj_1", "chat_9", "projects/proj_1/chat_9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := join(tc.projectID, tc.sessionID)
			if got != tc.want {
				t.Fatalf("ScopeSegments(%q, %q) = %q; want %q",
					tc.projectID, tc.sessionID, got, tc.want)
			}
		})
	}
}

// WriteScope is the writers' rule and the sandbox's write-back rule in one
// place: inside a project the chat segment is dropped, so every writer lands at
// the project root the dev server serves. Keyed on "is there a project", not on
// "is a runtime wired" — that conjunct is what made the callers agree by
// accident (docs 10 §4 G23).
func TestWriteScopeCollapsesInsideAProject(t *testing.T) {
	cases := []struct {
		name      string
		projectID string
		chat      string
		want      Scope
	}{
		{"project chat collapses to the project root", "proj_1", "chat_9", Scope{ProjectID: "proj_1"}},
		{"project root stays the project root", "proj_1", "", Scope{ProjectID: "proj_1"}},
		{"loose chat keeps its session", "", "chat_9", Scope{SessionID: "chat_9"}},
		{"agent-shared stays shared", "", "", Scope{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WriteScope(tc.projectID, tc.chat); got != tc.want {
				t.Fatalf("WriteScope(%q, %q) = %+v; want %+v",
					tc.projectID, tc.chat, got, tc.want)
			}
		})
	}
}

// The two rules have to describe the same filesystem: a writer's scope must be
// the scope whose segments its keys are written under. This is the property
// that broke when each site had its own switch.
func TestAWriterScopeIsTheScopeItsKeysLandIn(t *testing.T) {
	for _, chat := range []string{"chat_9", ""} {
		ws := WriteScope("proj_1", chat)
		want := []string{"projects", "proj_1"}
		got := ScopeSegments(ws.ProjectID, ws.SessionID)
		if len(got) != len(want) {
			t.Fatalf("writer scope %+v gave segments %v; want %v", ws, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("writer scope %+v gave segments %v; want %v", ws, got, want)
			}
		}
	}
}

func join(projectID, sessionID string) string {
	segs := ScopeSegments(projectID, sessionID)
	out := ""
	for i, s := range segs {
		if i > 0 {
			out += "/"
		}
		out += s
	}
	return out
}
