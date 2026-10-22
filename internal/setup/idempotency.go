package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

/**
 * [INPUT]: one submission's four scope parts (account, task, authorized client, key) plus
 *          the digest of its normalized instruction payload.
 * [OUTPUT]: the scope key and content digest the store claims. The store answers miss,
 *           duplicate (the first answer stands) or conflict (same key, different content).
 * [POS]: The input half of §3.1 of docs/fastagent/design/14-turn-identity.md. The checker
 *        itself is chat_idempotency in the store, so a retry that lands on another replica
 *        sees the same window.
 * [PROTOCOL]: The wire field is `idempotencyKey` (`turnId` is its legacy spelling, read and
 *        nothing more). The client identity arrives as `X-Fastagent-Client`; the pod cannot
 *        derive it, because it sees the cloud account and the end user, never "which
 *        authorized MCP client". Changing any of these three names changes the contract.
 */

// The window is a promise with an edge, not a lease: it holds nothing, and outside it the same
// key is simply a new instruction (§3.1 items 3 and 8). Ten minutes is comfortably larger than
// any client's retry horizon; expired rows are swept on the next claim.
const idempotencyTTL = 10 * time.Minute

// idempotencyKeyOf is the caller's string, in either spelling. `turnId` came first and used to
// mean identity; since 2026-09-28 identity is minted here (internal/agent/turn_id.go), so the
// old spelling is read as what it should always have been — a de-duplication key.
func idempotencyKeyOf(req chatRequest) string {
	if k := strings.TrimSpace(req.IdempotencyKey); k != "" {
		return k
	}
	return strings.TrimSpace(req.TurnID)
}

// clientIdentityOf answers "which authorized client". The trust model is the same as
// X-Fastagent-End-User's: only a holder of the cloud credential can reach this handler, and
// cloud is the only thing that knows the authorized client's id. An absent header leaves the
// domain at (account, task) — narrower than §3.1 asks for, and declared as such rather than
// silently widened into "every client shares one domain".
func clientIdentityOf(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Fastagent-Client"))
}

// idempotencyScopeKey composes the four parts. `verb` keeps the operations separate: a run and
// a steer are different things to do with a task, so the same string names two different
// operations rather than colliding in one domain.
func idempotencyScopeKey(verb, uid, agentID, sessionID, client, key string) string {
	return verb + "|" + uid + "|" + agentID + "|" + sessionID + "|" + client + "|" + key
}

// instructionDigest identifies the CONTENT that a key stands for: "body + attachment summary",
// not the raw JSON bytes (§3.1 item 5 — a retry that differs only in framing is a retry). The
// parts are joined with a NUL so that ("ab", "c") and ("a", "bc") cannot collide.
func instructionDigest(parts []string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])
}
