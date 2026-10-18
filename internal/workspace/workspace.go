// Package workspace is the durable blob store for agent-generated artifacts
// (generated PDFs/images/audio, downloaded files, intermediate data, ...).
//
// The two implementations currently shipped are:
//   - LocalFS: writes under ~/.fastagent/workspaces/<agent>/. Default.
//   - S3: writes to any S3-compatible bucket (AWS S3, MinIO, R2, B2, ...).
//
// The port lives in internal/workspace/port. This package keeps the
// implementations and re-exports the port names for source compatibility.
package workspace

import "github.com/fastclaw-ai/fastclaw/internal/workspace/port"

type Store = port.Store
type ObjectInfo = port.ObjectInfo
type Version = port.Version
type Scope = port.Scope

var (
	VersionAbsent            = port.VersionAbsent
	ErrNotFound              = port.ErrNotFound
	ErrSignedURLUnsupported  = port.ErrSignedURLUnsupported
	ErrVersionConflict       = port.ErrVersionConflict
	ErrMoveDestinationExists = port.ErrMoveDestinationExists
)

var ScopeSegments = port.ScopeSegments
var IsBuildArtifactDir = port.IsBuildArtifactDir

func WriteScope(projectID, chatSessionID string) Scope {
	return port.WriteScope(projectID, chatSessionID)
}
