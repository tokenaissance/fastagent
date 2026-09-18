package workspace

// Where a key lives, and where a writer must put it — one home for both.
//
// Two rules used to be written out in four places: the on-disk layout lived in
// LocalFS.scopeDir and (again) in S3.key / S3.scopePrefix, while "a project is
// one tree, so writers drop the chat segment" lived in the agent's file tools
// and (again) in the sandbox's sync write-back. Each pair agreed by convention,
// and the two pairs agreed only because the runtime happened to be wired
// everywhere (docs 10 §4 G23). Both rules are now one function each, and every
// caller derives from them.

// Scope names the (project, chat) pair a store key belongs to. An empty field
// means "not that kind of scope": the zero value is the agent-shared scope.
type Scope struct {
	ProjectID string
	SessionID string
}

// ScopeSegments is the ONE expression of the layout table — the path segments
// between the agent directory and the key, mirroring what LocalFS writes on
// disk and what S3 puts in a key:
//
//	pid="", sid=""   →  (nothing, the agent-shared root)
//	pid="", sid="x"  →  sessions/x
//	pid="p", sid=""  →  projects/p                    (project root)
//	pid="p", sid="x" →  projects/p/x                  (project chat subdir)
//
// Project chats keep their own subdir so two concurrent chats cannot collide on
// `notes.md` and "move a chat into / out of a project" is a single rename.
func ScopeSegments(projectID, sessionID string) []string {
	switch {
	case projectID != "" && sessionID != "":
		return []string{"projects", projectID, sessionID}
	case projectID != "":
		return []string{"projects", projectID}
	case sessionID != "":
		return []string{"sessions", sessionID}
	default:
		return nil
	}
}

// WriteScope is the ONE expression of "a project is one tree" for writers.
//
// Inside a project the chat segment is dropped, so every writer — file tools,
// the sandbox's write-back, the panel — addresses the project root the dev
// server serves (docs 01 §8.2, 10 §4 G17/G22/G23). The chat still names the
// sandbox INSTANCE; that is the container's business, not the key's.
//
// This is deliberately keyed on "is there a project", not on "is a project
// runtime wired": the collapse used to depend on the runtime being configured,
// so the callers agreed only while somebody else kept wiring it.
func WriteScope(projectID, chatSessionID string) Scope {
	if projectID != "" {
		return Scope{ProjectID: projectID}
	}
	return Scope{SessionID: chatSessionID}
}
