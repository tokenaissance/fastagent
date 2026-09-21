package agent

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// The attachment posture (change list B10 / obligation L7): an attachment is a
// CREATE-ONLY write — it must not replace a workspace file it has never seen —
// and a taken name is answered the way the composer answers it, by keeping both.
//
// Both halves are the ones a claim like "attachments are create-only" silently
// loses: without the first, a channel user's upload quietly overwrites the
// agent's artifact; without the second, the refusal was a `slog.Warn` while the
// path still went into the "[Attached: …]" breadcrumb, so the model read a
// DIFFERENT file and believed it was the one the user just sent.
//
// Falsification (run for real): hand the store a plain `Put` and the stale case
// below goes red (the earlier bytes are gone); drop the keep-both loop and the
// breadcrumb names a file the attachment never landed in.

type attachmentStore struct {
	mu       sync.Mutex
	objects  map[string]string
	attempts []struct {
		name     string
		expected workspace.Version
	}
}

func newAttachmentStore(seed map[string]string) *attachmentStore {
	objects := map[string]string{}
	for k, v := range seed {
		objects[k] = v
	}
	return &attachmentStore{objects: objects}
}

func (s *attachmentStore) PutIfVersion(_ context.Context, _ /*agent*/, _ /*project*/, _ /*session*/, path string, r io.Reader, _ int64, _ string, expected workspace.Version) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, struct {
		name     string
		expected workspace.Version
	}{path, expected})
	if _, taken := s.objects[path]; taken && expected == workspace.VersionAbsent {
		return workspace.ErrVersionConflict
	}
	s.objects[path] = string(body)
	return nil
}

func (s *attachmentStore) Put(ctx context.Context, agentID, projectID, sessionID, path string, r io.Reader, size int64, contentType string) error {
	// The port's real meaning: a blind overwrite, no precondition. Modelling it
	// as PutIfVersion would make this fake unable to tell the two apart — and
	// telling them apart is the whole point of the posture.
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, struct {
		name     string
		expected workspace.Version
	}{path, workspace.Version("(blind Put)")})
	s.objects[path] = string(body)
	return nil
}
func (s *attachmentStore) Get(context.Context, string, string, string, string) (io.ReadCloser, error) {
	return nil, workspace.ErrNotFound
}
func (s *attachmentStore) Stat(context.Context, string, string, string, string) (*workspace.ObjectInfo, error) {
	return nil, workspace.ErrNotFound
}
func (s *attachmentStore) List(context.Context, string, string, string) ([]workspace.ObjectInfo, error) {
	return nil, nil
}
func (s *attachmentStore) Delete(context.Context, string, string, string, string) error { return nil }
func (s *attachmentStore) Move(context.Context, string, string, string, string, string) error {
	return nil
}
func (s *attachmentStore) SignedURL(context.Context, string, string, string, string, time.Duration) (string, error) {
	return "", nil
}

func (s *attachmentStore) body(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[name]
}

func dataURL(body string) string {
	return "data:text/plain;base64," + base64Of(body)
}

func TestAttachmentIsCreateOnlyAndKeepsBothOnCollision(t *testing.T) {
	ctx := context.Background()
	store := newAttachmentStore(map[string]string{
		"notes.txt": "the file the workspace already had",
	})
	a := &Agent{name: "ag", agentID: "ag", workspaceStore: store}

	paths := a.WriteSessionAttachments(ctx, "sess-1", "", []Attachment{{Name: "notes.txt", URL: dataURL("the user's new file")}})

	// The engine still gets a path, and it is the path the bytes landed on.
	if len(paths) != 1 {
		t.Fatalf("paths = %v; want exactly one landed attachment", paths)
	}
	landed := paths[0]
	if landed == "notes.txt" {
		t.Fatal("the attachment took a name the store already held")
	}
	if got := store.body(landed); got != "the user's new file" {
		t.Fatalf("body at %q = %q; want the user's bytes", landed, got)
	}
	// …and the file that was there is untouched: keeping both means keeping both.
	if got := store.body("notes.txt"); got != "the file the workspace already had" {
		t.Fatalf("the pre-existing file = %q; it must not be replaced", got)
	}
	// Every write was conditional — create-only, never a blind Put.
	for _, attempt := range store.attempts {
		if attempt.expected != workspace.VersionAbsent {
			t.Fatalf("write of %q carried expectation %q; attachments are create-only", attempt.name, attempt.expected)
		}
	}
}

func TestAttachmentKeepsBothWithoutNestingTheCounter(t *testing.T) {
	ctx := context.Background()
	store := newAttachmentStore(map[string]string{
		"notes.txt":     "first",
		"notes (1).txt": "second",
	})
	a := &Agent{name: "ag", agentID: "ag", workspaceStore: store}

	paths := a.WriteSessionAttachments(ctx, "sess-1", "", []Attachment{{Name: "notes.txt", URL: dataURL("third")}})

	if len(paths) != 1 || paths[0] != "notes (2).txt" {
		t.Fatalf("paths = %v; want [notes (2).txt] (bump the counter, do not nest it)", paths)
	}
}

func TestAttachmentThatCannotFindANameIsNotClaimed(t *testing.T) {
	ctx := context.Background()
	// Every spelling taken: the honest answer is to attach nothing, not to hand
	// the model a path pointing at somebody else's bytes.
	seed := map[string]string{"notes.txt": "x"}
	for n := 1; n <= attachmentKeepBothCap; n++ {
		seed[strings.Replace("notes (N).txt", "N", itoa(n), 1)] = "x"
	}
	store := newAttachmentStore(seed)
	a := &Agent{name: "ag", agentID: "ag", workspaceStore: store}

	paths := a.WriteSessionAttachments(ctx, "sess-1", "", []Attachment{{Name: "notes.txt", URL: dataURL("the user's file")}})

	if len(paths) != 0 {
		t.Fatalf("paths = %v; nothing landed, so nothing may be claimed", paths)
	}
}
