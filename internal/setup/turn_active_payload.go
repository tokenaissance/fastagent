package setup

import (
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// turnActivePayload is the ONE wire shape for the session's live-turn fact.
//
// Contract C1 of the patched formal method (docs/文件系统形式化证明/08 §10.4):
// **one fact, one wire shape**. The fact has two exits — `event: turn_active` on
// /api/chat/subscribe and `turnActive` on /api/chat/history — and they used to
// spell the expiry differently (`expires_at` vs `expiresAt`). Because both were
// `map[string]any`, nothing but a live client could notice; the client parsed
// `expiresAt`, so a fact arriving over SSE had NO expiry and therefore never
// lapsed — a stale claim that could not self-heal.
//
// One struct, used by both exits: a shape drift is now a compile error.
type turnActivePayload struct {
	Holder    string `json:"holder"`
	Epoch     int64  `json:"epoch"`
	ExpiresAt string `json:"expiresAt"`
}

func newTurnActivePayload(rec *store.SessionLeaseRecord) *turnActivePayload {
	if rec == nil {
		return nil
	}
	return &turnActivePayload{
		Holder:    rec.HolderID,
		Epoch:     rec.Epoch,
		ExpiresAt: rec.ExpiresAt.UTC().Format(time.RFC3339),
	}
}
