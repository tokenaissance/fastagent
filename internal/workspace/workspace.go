// Package workspace is the durable blob store for agent-generated artifacts
// (generated PDFs/images/audio, downloaded files, intermediate data, ...).
//
// The two implementations currently shipped are:
//   - LocalFS: writes under ~/.fastclaw/workspaces/<agent>/. Default; used by
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
// sessionID isolates artifacts produced inside a single chat session so
// concurrent sessions of the same agent don't clobber each other's
// outputs. Pass an empty string for agent-shared artifacts (skills,
// admin uploads, anything that should outlive a single session). On
// disk this maps to:
//
//	sessionID == ""   →  <root>/<agentID>/<path>
//	sessionID == "x"  →  <root>/<agentID>/sessions/x/<path>
//
// List(agentID, "") returns EVERY object under the agent regardless of
// session — used by the admin file browser. List(agentID, "x") returns
// only files in session x.
type Store interface {
	Put(ctx context.Context, agentID, sessionID, path string, r io.Reader, size int64, contentType string) error

	Get(ctx context.Context, agentID, sessionID, path string) (io.ReadCloser, error)

	Stat(ctx context.Context, agentID, sessionID, path string) (*ObjectInfo, error)

	List(ctx context.Context, agentID, sessionID string) ([]ObjectInfo, error)

	Delete(ctx context.Context, agentID, sessionID, path string) error

	SignedURL(ctx context.Context, agentID, sessionID, path string, ttl time.Duration) (string, error)
}

// ObjectInfo describes one stored object. Fields not known by a particular
// backend are zero.
type ObjectInfo struct {
	Path        string    // agent-relative
	Size        int64     // bytes, -1 when unknown
	ContentType string    // e.g. "image/png"
	ModTime     time.Time // UTC
}

// Common errors. Implementations should wrap these with fmt.Errorf("%w: ...")
// when adding context, so callers can still errors.Is() match.
var (
	ErrNotFound             = errors.New("workspace: object not found")
	ErrSignedURLUnsupported = errors.New("workspace: signed URLs not supported by this backend")
)
