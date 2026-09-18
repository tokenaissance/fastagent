package sandbox

// The store↔sandbox path mapping the panel's delete depends on (docs 10 §4
// G21). It has to agree with hydrate in both directions: hydrate writes
// "workspace/" + the path the LIST returned, and the list was taken with the
// sandbox's own scope — so the inverse has to strip exactly the scope the list
// carried, no more and no less.

import (
	"path"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// The layout (workspace.ScopeSegments), the writers' rule (workspace.WriteScope)
// and the panel's parser are three questions about one filesystem, so a key the
// store really holds must parse back to the scope it lives in. This is the
// property G23 was about: before it, each site had its own switch and they
// agreed only while every caller kept its own copy in sync.
func TestLayoutWriteScopeAndParserAgree(t *testing.T) {
	// The shapes a WRITER produces: WriteScope never yields a project chat
	// subdir, so these are the only two non-shared scopes a tool can write.
	for _, sc := range []workspace.Scope{
		{},
		{SessionID: "chat_9"},
		{ProjectID: "proj_1"},
	} {
		key := strings.Join(append(workspace.ScopeSegments(sc.ProjectID, sc.SessionID), "f.md"), "/")
		gotProject, gotSession, inScope, prefixed := parseStoreScope(key)
		if gotProject != sc.ProjectID || gotSession != sc.SessionID || inScope != "f.md" {
			t.Fatalf("parseStoreScope(%q) = (%q,%q,%q); want (%q,%q,%q)",
				key, gotProject, gotSession, inScope, sc.ProjectID, sc.SessionID, "f.md")
		}
		if wantPrefixed := sc.ProjectID != "" || sc.SessionID != ""; prefixed != wantPrefixed {
			t.Fatalf("parseStoreScope(%q) prefixed=%v; want %v", key, prefixed, wantPrefixed)
		}
	}
}

// A key under a project chat's own subdir is a DIFFERENT object, not a different
// spelling: the parser reads it as "the project root, holding <chat>/f.md". That
// is the hydrate mapping (a project chat's sandbox mounts the project root and
// cwds into its subdir), and it is why the duplicates G17/A produced were extra
// files rather than overwrites — they live at a path of their own.
func TestAProjectChatSubdirKeyIsItsOwnPath(t *testing.T) {
	gotProject, gotSession, inScope, prefixed := parseStoreScope("projects/proj_1/chat_9/f.md")
	if !prefixed || gotProject != "proj_1" || gotSession != "" || inScope != "chat_9/f.md" {
		t.Fatalf("parseStoreScope(project chat key) = (%q,%q,%q,prefixed=%v); want (proj_1,\"\",chat_9/f.md,true)",
			gotProject, gotSession, inScope, prefixed)
	}
	if s, err := SandboxPathForStorePath("projects/proj_1/chat_9/f.md"); err != nil || s != "/workspace/chat_9/f.md" {
		t.Fatalf("SandboxPathForStorePath(project chat key) = (%q,%v); want /workspace/chat_9/f.md", s, err)
	}
}

// A project session's writers and its sync write-back must land on the SAME
// scope — that is what stops the sync from copying a project's files into the
// chat's subdir (docs 10 §4 G17 residual A) and what makes the panel's delete
// address the object the tools read (G21). Both call workspace.WriteScope.
func TestProjectWritersAndTheSyncShareOneScope(t *testing.T) {
	const (
		agentID   = "agt_1"
		projectID = "proj_1"
		chatID    = "chat_9"
	)
	ws := workspace.WriteScope(projectID, chatID)
	got := syncStoreScope(sandboxScope{agentID: agentID, projectID: projectID, sessionID: chatID})
	if got.agentID != agentID || got.projectID != ws.ProjectID || got.sessionID != ws.SessionID {
		t.Fatalf("syncStoreScope(project chat) = %+v; want the writer scope %+v for agent %q",
			got, ws, agentID)
	}
	if got.sessionID != "" {
		t.Fatalf("syncStoreScope(project chat) kept the chat segment %q; a project is one tree", got.sessionID)
	}

	// A loose chat keeps its own segment on both sides.
	loose := syncStoreScope(sandboxScope{agentID: agentID, sessionID: chatID})
	if loose.projectID != "" || loose.sessionID != chatID {
		t.Fatalf("syncStoreScope(loose chat) = %+v; want the chat scope", loose)
	}

	// And the panel, handed the key that writer produced, resolves to the very
	// instance the delete has to reach.
	key := path.Join(strings.Join(workspace.ScopeSegments(ws.ProjectID, ws.SessionID), "/"), "notes.md")
	p, s, _, prefixed := StorePathScope(key, projectID, chatID)
	if !prefixed || p != projectID || s != chatID {
		t.Fatalf("StorePathScope(%q) = (%q,%q,prefixed=%v); want (%q,%q,true)",
			key, p, s, prefixed, projectID, chatID)
	}
}

func TestSandboxPathForStorePath(t *testing.T) {
	cases := []struct {
		name      string
		storePath string
		want      string
	}{
		{"loose chat file", "sessions/sess_1/notes.py", "/workspace/notes.py"},
		{"loose chat subdir", "sessions/sess_1/app/src/x.tsx", "/workspace/app/src/x.tsx"},
		{"project root file", "projects/proj_1/notes.md", "/workspace/notes.md"},
		{"project chat file", "projects/proj_1/chat_9/src/x.tsx", "/workspace/chat_9/src/x.tsx"},
		{"already scope-relative", "notes.md", "/workspace/notes.md"},
		{"nested without prefix", "app/src/x.tsx", "/workspace/app/src/x.tsx"},
		{"leading slash is tolerated", "/sessions/sess_1/f", "/workspace/f"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SandboxPathForStorePath(tc.storePath)
			if err != nil {
				t.Fatalf("SandboxPathForStorePath(%q): %v", tc.storePath, err)
			}
			if got != tc.want {
				t.Fatalf("SandboxPathForStorePath(%q) = %q; want %q", tc.storePath, got, tc.want)
			}
		})
	}

	// A path that names a scope rather than a file is refused: "rm -rf the
	// workspace" is not a delete the panel can ask for.
	for _, bad := range []string{"sessions/sess_1", "projects/proj_1", ""} {
		if got, err := SandboxPathForStorePath(bad); err == nil {
			t.Fatalf("SandboxPathForStorePath(%q) = %q, want an error", bad, got)
		}
	}
}

func TestStorePathScope(t *testing.T) {
	cases := []struct {
		name         string
		storePath    string
		projectHint  string
		sessionHint  string
		wantProject  string
		wantSession  string
		wantPrefixed bool
	}{
		{
			name:      "loose chat path names its own chat",
			storePath: "sessions/sess_1/f", sessionHint: "ignored",
			wantProject: "", wantSession: "sess_1", wantPrefixed: true,
		},
		{
			name:      "project path takes the chat from the caller",
			storePath: "projects/proj_1/notes.md", sessionHint: "chat_9",
			wantProject: "proj_1", wantSession: "chat_9", wantPrefixed: true,
		},
		{
			name:      "scope-relative path keeps the caller's scope",
			storePath: "notes.md", projectHint: "proj_1", sessionHint: "chat_9",
			wantProject: "proj_1", wantSession: "chat_9", wantPrefixed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, s, _, prefixed := StorePathScope(tc.storePath, tc.projectHint, tc.sessionHint)
			if p != tc.wantProject || s != tc.wantSession || prefixed != tc.wantPrefixed {
				t.Fatalf("StorePathScope(%q, %q, %q) = (%q,%q,prefixed=%v); want (%q,%q,%v)",
					tc.storePath, tc.projectHint, tc.sessionHint, p, s, prefixed,
					tc.wantProject, tc.wantSession, tc.wantPrefixed)
			}
		})
	}
}
