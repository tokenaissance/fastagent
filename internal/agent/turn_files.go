package agent

import (
	"context"
	"log/slog"
	"time"
)

// turnFilesMeta walks the session's workspace and returns the files this turn
// created or modified, as assistant-message metadata:
//
//	{ "files": [{"path": "notes.md", "size": 42}, …], "turnEndedAt": 1760… }
//
// Why this exists: the web transcript renders a "Files from this turn" panel,
// and the client used to reconstruct that set itself — snapshot the workspace
// before the turn, list again after, diff by size|mtime. That worked for the
// live turn but not for history: /api/chat/history carried no per-turn file
// information, so a reloaded chat could only fall back to "every file in this
// session", which then rendered under a bubble claiming to be this turn's.
//
// Stamping the list onto the closing assistant message fixes both paths at
// once, because the same metadata map is (a) persisted in
// session_messages.metadata and (b) emitted on the trailing `content` event.
//
// The scan is time-based (`mtime >= turnStart`), mirroring
// gateway.appendRecentWorkspaceMedia: it survives overwrites, path moves and
// stores that don't preserve mtime ordering, none of which a path diff handles.
// Scope comes from store.List's own parameters, so agent identity files
// (SKILL.md, main.py, …) never leak in — they live outside the session/project
// subtree.
//
// Returns nil when there is nothing to report; callers merge it, so nil is a
// no-op rather than an empty list.
func (a *Agent) turnFilesMeta(ctx context.Context, projectID, sessionID string, turnStart time.Time) map[string]any {
	if a.workspaceStore == nil || sessionID == "" {
		return nil
	}
	objs, err := a.workspaceStore.List(ctx, a.agentID, projectID, sessionID)
	if err != nil {
		// Never fail a turn over cosmetics — the panel just stays empty.
		slog.Warn("turn files: workspace list failed",
			"agent", a.name, "project", projectID, "session", sessionID, "error", err)
		return nil
	}
	// One-second back-buffer: some store backends round mtime to whole seconds,
	// which can leave a file written 0.4s into the turn with a timestamp 0.6s
	// before turnStart (same rationale as appendRecentWorkspaceMedia).
	cutoff := turnStart.Add(-1 * time.Second)
	files := make([]map[string]any, 0, len(objs))
	for _, o := range objs {
		if o.ModTime.Before(cutoff) {
			continue
		}
		size := o.Size
		if size < 0 {
			// Unknown size (stub / streaming backend) — the panel shows the
			// path and omits the byte count rather than printing -1.
			size = 0
		}
		files = append(files, map[string]any{"path": o.Path, "size": size})
	}
	if len(files) == 0 {
		return nil
	}
	// turnEndedAt lets the client re-run the SAME window on demand (the panel's
	// refresh) instead of keeping its own pre-turn snapshot around. Seconds, to
	// match the unit GET /agents/{id}/files reports for `modTime` — the client
	// compares the two directly.
	return map[string]any{"files": files, "turnEndedAt": time.Now().Unix()}
}
