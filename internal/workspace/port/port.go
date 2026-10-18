// Package port holds the workspace port: the durable artifact store
// contract, its scope value, and the errors adapters must return.
//
// The adapter package internal/workspace implements this port. Consumers
// that need only the contract can depend on this package without importing
// LocalFS, S3, or their SDKs.
package port

import (
	"context"
	"errors"
	"io"
	"time"
)

// Scope names the (project, chat) pair a store key belongs to. An empty field
// means "not that kind of scope": the zero value is the agent-shared scope.
type Scope struct {
	ProjectID string
	SessionID string
}

// ScopeSegments is the ONE expression of the layout table — the path segments
// between the agent directory and the key:
//
//	pid="", sid=""   →  (nothing, the agent-shared root)
//	pid="", sid="x"  →  sessions/x
//	pid="p", sid=""  →  projects/p
//	pid="p", sid="x" →  projects/p/x
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
// Inside a project the chat segment is dropped, so every writer addresses the
// project root the dev server serves. The chat still names the sandbox
// instance; that is the container's business, not the key's.
func WriteScope(projectID, chatSessionID string) Scope {
	if projectID != "" {
		return Scope{ProjectID: projectID}
	}
	return Scope{SessionID: chatSessionID}
}

// Store is the durable artifact store. Implementations MUST be concurrency-
// safe — two pods may hit the same key at once.
//
// Paths are agent-relative (e.g. "report.pdf", "images/cover.png"). Absolute
// paths are never passed in. Implementations are free to add their own
// namespacing (bucket prefix, directory tree, ...) below the agent scope.
//
// projectID and sessionID together name the workspace folder for one chat.
// projectID wins when both are set: every session inside a project shares the
// same folder, which is the whole value of project. On disk:
//
//	projectID="", sessionID=""   → <root>/<agentID>/<path>
//	projectID="", sessionID="x"  → <root>/<agentID>/sessions/x/<path>
//	projectID="p", sessionID=""  → <root>/<agentID>/projects/p/<path>
//	projectID="p", sessionID="x" → <root>/<agentID>/projects/p/x/<path>
type Store interface {
	Put(ctx context.Context, agentID, projectID, sessionID, path string, r io.Reader, size int64, contentType string) error

	// PutIfVersion writes only when the object still carries `expected`.
	// A stale expectation comes back as ErrVersionConflict instead of
	// silently overwriting whoever wrote in between.
	//
	// VersionAbsent means "this key must not exist yet" (a create-only write).
	PutIfVersion(ctx context.Context, agentID, projectID, sessionID, path string, r io.Reader, size int64, contentType string, expected Version) error

	Get(ctx context.Context, agentID, projectID, sessionID, path string) (io.ReadCloser, error)

	Stat(ctx context.Context, agentID, projectID, sessionID, path string) (*ObjectInfo, error)

	List(ctx context.Context, agentID, projectID, sessionID string) ([]ObjectInfo, error)

	Delete(ctx context.Context, agentID, projectID, sessionID, path string) error

	// Move relocates a session's entire workspace from one scope to another.
	// Implementations MUST refuse to overwrite a non-empty destination with
	// ErrMoveDestinationExists.
	Move(ctx context.Context, agentID, fromProjectID, fromSessionID, toProjectID, toSessionID string) error

	SignedURL(ctx context.Context, agentID, projectID, sessionID, path string, ttl time.Duration) (string, error)
}

// ObjectInfo describes one stored object. Fields not known by a particular
// backend are zero.
type ObjectInfo struct {
	Path        string
	Size        int64
	ContentType string
	ModTime     time.Time
	// Version identifies this object's content to its backend. It is opaque
	// to callers. Empty means "this backend cannot tell".
	Version Version
}

// Version is an opaque per-backend content identity.
type Version string

// VersionAbsent is the precondition "the key must not exist yet".
var VersionAbsent Version = ""

// Common errors. Implementations should wrap these with fmt.Errorf("%w: ...")
// when adding context, so callers can still errors.Is() match.
var (
	ErrNotFound              = errors.New("workspace: object not found")
	ErrSignedURLUnsupported  = errors.New("workspace: signed URLs not supported by this backend")
	ErrVersionConflict       = errors.New("workspace: version conflict")
	ErrMoveDestinationExists = errors.New("workspace: move destination already exists")
)

var buildArtifactDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	".output":      true,
	".vite":        true,
	".next":        true,
	".turbo":       true,
	".cache":       true,
	".pnpm-store":  true,
	".wrangler":    true,
}

// IsBuildArtifactDir reports whether a directory of this base name should be
// pruned from workspace enumeration.
func IsBuildArtifactDir(name string) bool { return buildArtifactDirs[name] }
