package workspace

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// PutIfVersion is the family-B capability (docs 12 §3.1, obligation L7): the
// precondition rides with the write. On LocalFS it is declared best-effort —
// stat, compare, write — which is still enough to refuse a writer whose
// expectation is already stale.
//
// Falsification: drop the version comparison from LocalFS.PutIfVersion and the
// stale-expectation cases below stop failing.
func TestLocalFSPutIfVersionRefusesAStaleExpectation(t *testing.T) {
	ctx := context.Background()
	fs := NewLocalFS(t.TempDir())
	const agent, proj, sess, path = "agt", "proj", "sess", "notes.md"

	// Create-only: VersionAbsent succeeds when the key is free…
	if err := fs.PutIfVersion(ctx, agent, proj, sess, path, strings.NewReader("v1"), 2, "", VersionAbsent); err != nil {
		t.Fatalf("create-only on a free key: %v", err)
	}
	// …and refuses when it is not.
	if err := fs.PutIfVersion(ctx, agent, proj, sess, path, strings.NewReader("v1b"), 3, "", VersionAbsent); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("create-only on an existing key = %v, want ErrVersionConflict", err)
	}

	info, err := fs.Stat(ctx, agent, proj, sess, path)
	if err != nil || info.Version == "" {
		t.Fatalf("Stat returned no version: info=%+v err=%v", info, err)
	}

	// The holder of the current version may write…
	if err := fs.PutIfVersion(ctx, agent, proj, sess, path, strings.NewReader("v2"), 2, "", info.Version); err != nil {
		t.Fatalf("write with the live version: %v", err)
	}
	// …but the version that was just superseded may not.
	if err := fs.PutIfVersion(ctx, agent, proj, sess, path, strings.NewReader("v3"), 2, "", info.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("write with a stale version = %v, want ErrVersionConflict", err)
	}
	rc, err := fs.Get(ctx, agent, proj, sess, path)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "v2" {
		t.Fatalf("content = %q, want v2 (the stale writer must not have landed)", data)
	}
}
