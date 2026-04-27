// Package users manages API keys for accessing FastClaw agents via the HTTP API.
//
// API keys are persisted through the configured `store.Store` (DB-backed in
// SaaS, file-backed locally — same JSON format on disk so a sqlite-only dev
// box can move to Postgres without losing keys, modulo a one-shot migration).
// Each key grants access to the agent API (chat, sessions, config). The
// gateway auth token (in fastclaw.json) is the admin key — it can manage
// other API keys.
//
// Tokens are stored as SHA256 hashes; the plaintext is shown to the caller
// exactly once at create/rotate. KeyPrefix (first ~10 chars of the original
// token) is kept alongside the hash purely for UI display so list views can
// show "fc_a1b2c3..." instead of an opaque hash.
package users

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// APIKey is the public representation handed back by Registry methods. The
// `Key` field is the masked display string ("fc_xxxx****") for list calls,
// and the freshly-issued plaintext token for the dedicated create/rotate
// return paths — never both.
type APIKey struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"createdAt"`
}

// User is kept as an alias for backward compatibility with API responses.
type User = APIKey

// Registry is a thin facade over store.Store. It exists so callers that
// previously held a *Registry don't need rewiring; the storage backend is
// now pluggable and durable across pod restarts.
type Registry struct {
	store store.Store
	mu    sync.Mutex
}

// Load returns a Registry backed by the given store. The store must already
// be initialized (Migrate called for DB stores). nil store is rejected —
// callers should pass an explicit FileStore for the local-file behavior.
func Load(st store.Store) (*Registry, error) {
	if st == nil {
		return nil, errors.New("users.Load: store is required")
	}
	return &Registry{store: st}, nil
}

// hashToken returns the canonical lookup key for a plaintext token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// keyPrefix returns the public-display slice of a freshly-generated token.
// We keep ~10 chars so the user can recognise "their" key in a list, while
// staying safely below the entropy a brute-force search would need.
func keyPrefix(token string) string {
	if len(token) <= 10 {
		return token
	}
	return token[:10]
}

func toAPIKey(rec *store.APIKeyRecord) *APIKey {
	if rec == nil {
		return nil
	}
	masked := rec.KeyPrefix
	if masked == "" {
		masked = "fc_********"
	} else {
		masked = masked + "****"
	}
	return &APIKey{
		ID:        rec.ID,
		Name:      rec.Name,
		Key:       masked,
		CreatedAt: rec.CreatedAt,
	}
}

// Add creates a new API key with the given id and name. Returns the public
// record (with masked key for display) and the freshly-issued plaintext
// token, which the caller MUST capture — it is never recoverable later.
func (r *Registry) Add(id, name string) (*APIKey, string, error) {
	if id == "" {
		return nil, "", errors.New("id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	token, err := newToken()
	if err != nil {
		return nil, "", err
	}
	rec := &store.APIKeyRecord{
		ID:        id,
		Name:      name,
		KeyHash:   hashToken(token),
		KeyPrefix: keyPrefix(token),
		CreatedAt: time.Now().UTC(),
	}
	if err := r.store.CreateAPIKey(context.Background(), rec); err != nil {
		return nil, "", err
	}
	return toAPIKey(rec), token, nil
}

// IssueToken rotates the token for an existing key id, returning the new
// plaintext. Previously-issued tokens stop working immediately.
func (r *Registry) IssueToken(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	token, err := newToken()
	if err != nil {
		return "", err
	}
	if err := r.store.RotateAPIKey(context.Background(), id, hashToken(token), keyPrefix(token)); err != nil {
		return "", err
	}
	return token, nil
}

func (r *Registry) Remove(id string) error {
	return r.store.DeleteAPIKey(context.Background(), id)
}

func (r *Registry) List() []*APIKey {
	recs, err := r.store.ListAPIKeys(context.Background())
	if err != nil {
		return nil
	}
	out := make([]*APIKey, 0, len(recs))
	for i := range recs {
		out = append(out, toAPIKey(&recs[i]))
	}
	return out
}

func (r *Registry) Get(id string) (*APIKey, bool) {
	rec, err := r.store.GetAPIKey(context.Background(), id)
	if err != nil || rec == nil {
		return nil, false
	}
	return toAPIKey(rec), true
}

// LookupByToken is the auth hot path: SHA256(token) → key id, with `ok`
// false when the token isn't recognised. Callers must NOT log the token —
// it's the bearer credential itself.
func (r *Registry) LookupByToken(token string) (string, bool) {
	id, ok, err := r.store.LookupAPIKeyByHash(context.Background(), hashToken(token))
	if err != nil {
		return "", false
	}
	return id, ok
}

func (r *Registry) Count() int {
	recs, err := r.store.ListAPIKeys(context.Background())
	if err != nil {
		return 0
	}
	return len(recs)
}

// Save is a no-op kept for backward compatibility with the old file-backed
// implementation. Mutations now persist immediately through the store.
func (r *Registry) Save() error { return nil }

func newToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "fc_" + hex.EncodeToString(buf[:]), nil
}
