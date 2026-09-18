package sandbox

import (
	"fmt"
	"path"
	"strings"
)

// Store-path ↔ sandbox-path mapping for the panel's file operations.
//
// It is the inverse of what hydrate does: `hydrateWorkspaceEntries` puts every
// store object at `workspace/` + the path the LIST returned, and the list was
// taken with the sandbox's own scope (project chats list with session="", so a
// project's files land at the project tree's root). The file panel works in
// AGENT-relative store paths — the shape `handleAgentFileList` returns and the
// download endpoint consumes ("sessions/<sid>/f", "projects/<pid>/x",
// "projects/<pid>/<chat>/f").
//
// One mapping, one home: the delete side must agree with hydrate and with the
// download side, or it silently addresses a path nobody else uses (that is
// exactly 10 §4 G21, where the delete handler applied the scope twice).

// parseStoreScope splits an agent-relative store path into the scope it belongs
// to and the path inside that scope.
//
// prefixed=false means the caller handed us a scope-relative path (the older
// shape: a bare name plus the scope query params) — the caller then keeps its
// own scope. A path that already carries a prefix always wins, because that is
// what the file list returns and what the user clicked on.
func parseStoreScope(storePath string) (projectID, sessionID, inScope string, prefixed bool) {
	clean := strings.TrimLeft(path.Clean("/"+storePath), "/")
	parts := strings.Split(clean, "/")
	switch {
	case len(parts) >= 2 && parts[0] == "sessions":
		return "", parts[1], strings.Join(parts[2:], "/"), true
	case len(parts) >= 2 && parts[0] == "projects":
		return parts[1], "", strings.Join(parts[2:], "/"), true
	default:
		return "", "", clean, false
	}
}

// StorePathScope resolves an agent-relative store path plus the caller's scope
// hints into the scope a SANDBOX holding that file would be keyed by.
//
// The prefix wins where it speaks: "sessions/<sid>/f" names its own chat, and
// "projects/<pid>/x" names its project — while the CHAT for a project path comes
// from the hint, because a project-root file lives in the instance of whichever
// chat has the panel open (that is the container hydrate put it into). A path
// with no prefix keeps the caller's scope entirely (the older, scope-relative
// shape).
func StorePathScope(storePath, projectHint, sessionHint string) (projectID, sessionID, inScope string, prefixed bool) {
	projectID, sessionID, inScope, prefixed = parseStoreScope(storePath)
	if !prefixed {
		return projectHint, sessionHint, inScope, false
	}
	if projectID != "" && sessionID == "" {
		// Project path: the scope names the project, the chat comes from the
		// caller. Using the hint here is what keeps the delete pointed at the
		// very instance the agent's turns use (docs 01 §8.2's scope split).
		sessionID = sessionHint
	}
	return projectID, sessionID, inScope, true
}

// SandboxPathForStorePath maps an agent-relative store path to the absolute
// path the same file has inside a hydrated sandbox.
//
// It refuses the two shapes that would turn a delete into something worse: an
// empty in-scope path (the scope directory itself) and anything that escapes
// the workspace root.
func SandboxPathForStorePath(storePath string) (string, error) {
	_, _, inScope, _ := parseStoreScope(storePath)
	if inScope == "" || inScope == "." {
		return "", fmt.Errorf("sandbox: refusing to map %q — it names a scope, not a file", storePath)
	}
	if strings.HasPrefix(inScope, "..") {
		return "", fmt.Errorf("sandbox: refusing to map %q — it escapes the workspace root", storePath)
	}
	return "/workspace/" + inScope, nil
}
