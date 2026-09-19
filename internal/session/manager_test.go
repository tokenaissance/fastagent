package session

import (
	"context"
	"errors"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type noopSessionStore struct{}

func (noopSessionStore) GetSession(context.Context, string, string) ([]provider.Message, error) {
	return nil, nil
}

// recordingStore keeps the scopes the port was called with, so the *route* a
// per-turn fact takes can be asserted rather than assumed.
type recordingStore struct {
	noopSessionStore
	saved   []WriteScope
	fences  []*TurnFence
	chatter []string
}

func (r *recordingStore) SaveSession(_ context.Context, _, _ string, _ []provider.Message, scope WriteScope) error {
	r.saved = append(r.saved, scope)
	r.fences = append(r.fences, scope.Fence)
	r.chatter = append(r.chatter, scope.ChatterUserID)
	return nil
}

func (r *recordingStore) AppendMessage(_ context.Context, _, _ string, _ provider.Message, scope WriteScope) error {
	r.chatter = append(r.chatter, scope.ChatterUserID)
	return nil
}

// The refusal is *produced* by the store and *named* by this package. What the
// turn loop reads back has to be this package's own sentinel, or the inner layer
// would have to know a type owned by the outer one — the Dependency Rule applied
// to an error value (docs 10 §10.9).
//
// This witnesses the translation itself rather than a session driven through a
// double: a double that stands in for the *adapter* bypasses the very mapping
// under test (the first version of this test did exactly that and passed through
// nothing, which is how the mistake was found).
//
// Falsification: make writeRefusal return err unchanged and the first assertion
// fails — the loop would see the store's error and not recognise its own refusal.
func TestWriteRefusalTranslatesTheStoresSentinel(t *testing.T) {
	if err := writeRefusal(store.ErrSessionFenceLost); !errors.Is(err, ErrSessionFenceLost) {
		t.Fatalf("writeRefusal(store sentinel) = %v, want this package's ErrSessionFenceLost", err)
	}
	if err := writeRefusal(nil); err != nil {
		t.Fatalf("a successful write must stay successful, got %v", err)
	}
	other := errors.New("some other failure")
	if err := writeRefusal(other); !errors.Is(err, other) {
		t.Fatalf("an unrelated error must pass through unchanged, got %v", err)
	}
}

// The write scope is the ONE route for the facts that describe the possession
// writing: the fence and the chatter ride the same value, and neither depends on
// a context value owned by an outer package (which is what forced this package
// to import `internal/store`, docs 10 §10.9).
//
// Falsification: drop ChatterUserID from writeScope() — or read it back from the
// context as before — and this fails at "the chatter never reached the write".
func TestWriteScopeCarriesTheChatterAndTheFence(t *testing.T) {
	rec := &recordingStore{}
	m := NewManagerWithStoreForUser(t.TempDir(), rec, "u_owner", "agent-1")
	s := m.Get("web", "", "chat-scope", "")
	s.SetChatter("u_chatter")
	s.SetTurnFence("pod-a/1", 7)
	s.Append(provider.Message{Role: "user", Content: "hi"})

	if len(rec.chatter) == 0 || rec.chatter[len(rec.chatter)-1] != "u_chatter" {
		t.Fatalf("the chatter never reached the write: %v", rec.chatter)
	}
	if len(rec.saved) == 0 {
		t.Fatal("SaveSession was never called")
	}
	scope := rec.saved[len(rec.saved)-1]
	if scope.Fence == nil || scope.Fence.Holder != "pod-a/1" || scope.Fence.Epoch != 7 {
		t.Fatalf("the fence did not ride the scope: %+v", scope.Fence)
	}
	if scope.Channel != "web" || scope.ChatID != "chat-scope" {
		t.Fatalf("the conversation triple did not ride the scope: %+v", scope)
	}
}

// The row-creation path (a brand-new conversation, stored before it has any
// history) is the second caller of the one constructor — the place where the
// first version of this change hand-wrote a second WriteScope literal and broke
// its own "one route" claim. The witness is the scope the store actually
// received: the triple is there, and the two per-turn fields are explicitly
// empty rather than "whatever happened to be lying around".
//
// Falsification: build the literal inline again and drop the triple from it —
// this fails, and the row would be created with no conversation attached.
func TestTheRowCreationPathUsesTheOneScopeConstructor(t *testing.T) {
	rec := &recordingStore{}
	m := NewManagerWithStoreForUser(t.TempDir(), rec, "u_owner", "agent-1")
	key := m.OpenNewSession("web", "acct-1", "chat-row")

	if len(rec.saved) == 0 {
		t.Fatal("creating a row did not reach the store")
	}
	scope := rec.saved[0]
	if scope.Channel != "web" || scope.AccountID != "acct-1" || scope.ChatID != "chat-row" {
		t.Fatalf("the created row's scope lost its identity: %+v", scope)
	}
	if scope.ChatterUserID != "" || scope.Fence != nil {
		t.Fatalf("a row with no turn yet must carry no per-turn facts: %+v", scope)
	}
	_ = key
}
func (noopSessionStore) SaveSession(context.Context, string, string, []provider.Message, WriteScope) error {
	return nil
}
func (noopSessionStore) AppendMessage(context.Context, string, string, provider.Message, WriteScope) error {
	return nil
}
func (noopSessionStore) ListMessages(context.Context, string, string) ([]provider.Message, error) {
	return nil, nil
}
func (noopSessionStore) ListWebSessions(context.Context, string) ([]WebSession, error) {
	return nil, nil
}
func (noopSessionStore) DeleteSession(context.Context, string, string) error {
	return nil
}
func (noopSessionStore) RenameSession(context.Context, string, string, string) error {
	return nil
}
func (noopSessionStore) MoveSession(context.Context, string, string, string) error {
	return nil
}
func (noopSessionStore) ResolveActiveSessionKey(context.Context, string, string, string, string) (string, error) {
	return "", nil
}
func (noopSessionStore) LookupSessionTriple(context.Context, string, string) (string, string, string, error) {
	return "", "", "", nil
}
func (noopSessionStore) LookupSessionProject(context.Context, string, string) (string, error) {
	return "", nil
}

func TestNewManagerWithStoreForUserEmptyUserIDDoesNotPanic(t *testing.T) {
	mgr := NewManagerWithStoreForUser(t.TempDir(), noopSessionStore{}, "", "agent-1")
	if mgr == nil {
		t.Fatal("expected manager")
	}
	s := mgr.Get("web", "", "chat-1", "")
	if s == nil {
		t.Fatal("expected session")
	}
	s.Append(provider.Message{Role: "user", Content: "hello"})
	if got := len(s.GetMessages()); got != 1 {
		t.Fatalf("message count = %d, want 1", got)
	}
}
