package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// fakeWorkspaceStore satisfies workspace.Store for turnFilesMeta tests: the
// embedded nil interface supplies every method the helper never calls.
type fakeWorkspaceStore struct {
	workspace.Store
	objs  []workspace.ObjectInfo
	err   error
	calls int
}

func (f *fakeWorkspaceStore) List(ctx context.Context, agentID, projectID, sessionID string) ([]workspace.ObjectInfo, error) {
	f.calls++
	return f.objs, f.err
}

func turnFiles(t *testing.T, meta map[string]any) []map[string]any {
	t.Helper()
	raw, ok := meta["files"]
	if !ok {
		t.Fatalf("metadata has no files key: %#v", meta)
	}
	files, ok := raw.([]map[string]any)
	if !ok {
		t.Fatalf("files is not []map[string]any: %#v", raw)
	}
	return files
}

func TestTurnFilesMetaKeepsOnlyFilesFromThisTurn(t *testing.T) {
	turnStart := time.Now()
	stale := workspace.ObjectInfo{Path: "old-report.md", Size: 10, ModTime: turnStart.Add(-10 * time.Minute)}
	fresh := workspace.ObjectInfo{Path: "report.md", Size: 42, ModTime: turnStart.Add(2 * time.Second)}
	// mtime rounding: a write 0.4s into the turn can carry a stamp 0.6s BEFORE
	// turnStart, so the 1s back-buffer must still catch it.
	rounded := workspace.ObjectInfo{Path: "chart.png", Size: 7, ModTime: turnStart.Add(-600 * time.Millisecond)}
	unknownSize := workspace.ObjectInfo{Path: "stream.bin", Size: -1, ModTime: turnStart.Add(time.Second)}

	ws := &fakeWorkspaceStore{objs: []workspace.ObjectInfo{stale, fresh, rounded, unknownSize}}
	a := &Agent{name: "agent-1", agentID: "agent-1", workspaceStore: ws}

	meta := a.turnFilesMeta(context.Background(), "p1", "s1", turnStart)
	if meta == nil {
		t.Fatal("expected metadata, got nil")
	}
	if _, ok := meta["turnEndedAt"].(int64); !ok {
		t.Errorf("turnEndedAt missing or not int64: %#v", meta["turnEndedAt"])
	}

	files := turnFiles(t, meta)
	got := make([]string, 0, len(files))
	for _, f := range files {
		got = append(got, f["path"].(string))
	}
	want := map[string]bool{"report.md": true, "chart.png": true, "stream.bin": true}
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want the three fresh files only", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected path %q (a previous turn's file leaked in)", p)
		}
	}

	// Unknown size is normalised, not surfaced as -1.
	for _, f := range files {
		if f["path"] == "stream.bin" && f["size"].(int64) != 0 {
			t.Errorf("unknown size should normalise to 0, got %v", f["size"])
		}
	}
	if ws.calls != 1 {
		t.Errorf("expected exactly one workspace listing, got %d", ws.calls)
	}
}

func TestTurnFilesMetaStaysSilentWhenThereIsNothingToSay(t *testing.T) {
	turnStart := time.Now()

	cases := []struct {
		name       string
		agent      *Agent
		sessionID  string
		wantNoCall bool
	}{
		{
			name:       "no workspace store wired",
			agent:      &Agent{name: "a", agentID: "a"},
			sessionID:  "s1",
			wantNoCall: true,
		},
		{
			name:      "server-side sessionless chat",
			agent:     &Agent{name: "a", agentID: "a", workspaceStore: &fakeWorkspaceStore{}},
			sessionID: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if meta := tc.agent.turnFilesMeta(context.Background(), "", tc.sessionID, turnStart); meta != nil {
				t.Fatalf("expected nil metadata, got %#v", meta)
			}
		})
	}

	// A turn that wrote nothing must not stamp an empty list — the panel hides
	// on absence, so `"files": []` would render an empty card.
	onlyStale := &fakeWorkspaceStore{objs: []workspace.ObjectInfo{
		{Path: "old.md", Size: 1, ModTime: turnStart.Add(-time.Hour)},
	}}
	a := &Agent{name: "a", agentID: "a", workspaceStore: onlyStale}
	if meta := a.turnFilesMeta(context.Background(), "", "s1", turnStart); meta != nil {
		t.Fatalf("expected nil for a turn with no output, got %#v", meta)
	}

	// A listing failure is cosmetic: nil, never an error out of the turn.
	failing := &fakeWorkspaceStore{err: errors.New("store down")}
	boom := &Agent{name: "a", agentID: "a", workspaceStore: failing}
	if meta := boom.turnFilesMeta(context.Background(), "", "s1", turnStart); meta != nil {
		t.Fatalf("expected nil when the listing fails, got %#v", meta)
	}
}
