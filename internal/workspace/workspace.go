// Package workspace is the durable blob store for agent-generated artifacts
// (generated PDFs/images/audio, downloaded files, intermediate data, ...).
//
// The two implementations currently shipped are:
//   - LocalFS: writes under ~/.fastagent/workspaces/<agent>/. Default; used by
//     single-host deployments and keeps existing filesystem semantics intact.
//   - S3: writes to any S3-compatible bucket (AWS S3, MinIO, R2, B2, ...).
//     Required for stateless multi-pod deployments — any pod can read/write
//     the same object even though the filesystem is pod-local.
//
// Keep this package free of runtime deps on tools/handlers so sandbox code
// and agent code can share the same Store without pulling each other in.
package workspace

import (
	"context"
	"errors"
	"io"
	"time"
)

// Store is the durable artifact store. Implementations MUST be concurrency-
// safe — two pods may hit the same key at once.
//
// Paths are agent-relative (e.g. "report.pdf", "images/cover.png"). Absolute
// paths are never passed in. Implementations are free to add their own
// namespacing (bucket prefix, directory tree, ...) below the agent scope.
//
// projectID and sessionID together name the workspace folder for one
// chat. projectID wins when both are set: every session inside a
// project shares the same folder, which is the whole value of project
// (notes/files persist across the project's chats). On disk:
//
//	projectID="", sessionID=""   → <root>/<agentID>/<path>
//	projectID="", sessionID="x"  → <root>/<agentID>/sessions/x/<path>
//	projectID="p", sessionID=""  → <root>/<agentID>/projects/p/<path>
//	projectID="p", sessionID="x" → <root>/<agentID>/projects/p/x/<path>
//
// A project chat keeps its own subdirectory under the project
// ("projects/<pid>/<chat_id>/"), so dragging a chat into a project moves its
// files rather than merging them into the project's shared root — that root
// stays for deliberately shared files. Both stores implement this, and
// stripScopePrefix in internal/setup reads it back.
//
// List with both empty returns EVERY object under the agent regardless
// of project/session — used by the admin file browser. List with a
// specific scope returns only that subtree.
type Store interface {
	Put(ctx context.Context, agentID, projectID, sessionID, path string, r io.Reader, size int64, contentType string) error

	// PutIfVersion writes only when the object still carries `expected` — the
	// precondition is evaluated BY THE RESOURCE, in the same request as the
	// effect (obligation L4(a)/L7, docs/文件系统形式化证明/12-lease-formal-design.md
	// §3/§3.1): a stale expectation comes back as ErrVersionConflict instead of
	// silently overwriting whoever wrote in between.
	//
	// VersionAbsent means "this key must not exist yet" (a create-only write).
	//
	// Strength is per backend and MUST be declared, not assumed:
	//   S3      — exact (an If-Match / If-None-Match conditional PUT)
	//   LocalFS — best-effort (read-then-write; there is no kernel CAS). LocalFS
	//             deployments are single-host by construction; multi-replica
	//             installs must use S3 or a database-backed store.
	PutIfVersion(ctx context.Context, agentID, projectID, sessionID, path string, r io.Reader, size int64, contentType string, expected Version) error

	Get(ctx context.Context, agentID, projectID, sessionID, path string) (io.ReadCloser, error)

	Stat(ctx context.Context, agentID, projectID, sessionID, path string) (*ObjectInfo, error)

	List(ctx context.Context, agentID, projectID, sessionID string) ([]ObjectInfo, error)

	Delete(ctx context.Context, agentID, projectID, sessionID, path string) error

	// Move relocates a session's entire workspace from one (project,
	// session) scope to another. Used when a chat is dragged in or out
	// of a project: the session_id stays the same, only the projectID
	// changes (one of fromProjectID / toProjectID is "" for loose).
	// Implementations MUST refuse to overwrite a non-empty destination —
	// returning ErrMoveDestinationExists — so a slip can't merge two
	// chats' files. fromSessionID and toSessionID are usually equal
	// but kept as separate args in case a future caller wants to
	// rename the session at the same time. No-op if the source scope
	// has no objects.
	Move(ctx context.Context, agentID, fromProjectID, fromSessionID, toProjectID, toSessionID string) error

	SignedURL(ctx context.Context, agentID, projectID, sessionID, path string, ttl time.Duration) (string, error)
}

// ObjectInfo describes one stored object. Fields not known by a particular
// backend are zero.
type ObjectInfo struct {
	Path        string    // agent-relative
	Size        int64     // bytes, -1 when unknown
	ContentType string    // e.g. "image/png"
	ModTime     time.Time // UTC
	// Version identifies this object's content to its backend: S3's ETag,
	// LocalFS's size:mtime. It is OPAQUE to callers — an inner-layer token, not
	// an SDK type — and it is what PutIfVersion compares against. Empty means
	// "this backend cannot tell" (then PutIfVersion is a plain Put for that
	// backend and the caller must not claim prevention).
	Version Version
}

// Version is an opaque per-backend content identity (see ObjectInfo.Version).
type Version string

// VersionAbsent is the precondition "the key must not exist yet".
var VersionAbsent Version = ""

// ErrVersionConflict reports that a conditional write found a different version
// than the caller expected: somebody else wrote first. The caller must refuse
// and say so; it must never retry blindly (that would overwrite the winner).
var ErrVersionConflict = errors.New("workspace: version conflict")

// Common errors. Implementations should wrap these with fmt.Errorf("%w: ...")
// when adding context, so callers can still errors.Is() match.
var (
	ErrNotFound              = errors.New("workspace: object not found")
	ErrSignedURLUnsupported  = errors.New("workspace: signed URLs not supported by this backend")
	ErrMoveDestinationExists = errors.New("workspace: move destination already exists")
)
