package session

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// Session holds the message history for one conversation thread within
// a (channel, accountID, chatID) triple. session_key is the per-session
// opaque id; the triple identifies "where" the conversation lives.
type Session struct {
	mu         sync.Mutex
	Messages   []provider.Message
	filePath   string
	snapshot   []provider.Message // undo snapshot: a SHALLOW copy — see Snapshot()
	snapshotAt time.Time          // when that snapshot was taken; it has a lifetime
	store      SessionStore
	userID     string
	agentID    string
	sessionKey string
	channel    string
	accountID  string
	chatID     string
	// projectID, when non-empty, is stamped on every SaveSession write
	// for this session. Set on the FIRST turn of a brand-new chat that
	// arrived with a project hint (URL `?project=<pid>`); for existing
	// rows it's read back via Manager.Get and late-bound here so the
	// next save preserves it.
	projectID string
	// chatterUserID is the per-turn conversation participant — distinct
	// from userID (UserSpace owner = channel binder) whenever an IM
	// channel routes a per-sender app_user into a channel-owner
	// UserSpace. Set per-turn by the agent loop via SetChatter so the
	// ctx() embeds it for DBStore session writes (sessions.chatter_user_id /
	// session_messages.chatter_user_id / session_events.chatter_user_id).
	// Empty when the caller hasn't bound a chatter — writes leave the
	// column '' and readers fall back to user_id.
	chatterUserID string
	// provider and model are stamped onto assistant messages by
	// Append so session_messages rows record which LLM produced them.
	// Set per-turn by the agent loop via SetProviderModel.
	provider string
	model    string
	// runReceipt is the agent's world at the start of the turn that is about to
	// run, as one opaque JSON document (configuration fingerprint + skills /
	// tools / memory / identity / cron fingerprints today). Append stamps it
	// onto the assistant message as metadata, which makes the turn receipt the
	// durable home of "what this conversation last ran under and last saw" — the
	// baseline the environment signal needs after a rebuild or a restart, so the
	// signal does not keep one of its own (docs 10 §3.3/§4, G9 + G20).
	//
	// Opaque on purpose: this package owns the fact "a turn carries a receipt
	// with it", not what is inside one. The producer and the reader of the
	// document are both the agent (docs 10 §4, G20).
	runReceipt string

	// Steering: turnDepth counts in-flight HandleMessage turns for this
	// session (a counter, not a bool, so re-entrant/overlapping turns
	// don't strand the active flag). steerBuf holds user messages that
	// arrived mid-turn; the running ReAct loop drains them between tool
	// iterations. Both are guarded by mu. getByKey never touches these,
	// so a Manager.Get reload (which overwrites Messages) can't clobber a
	// pending steer.
	turnDepth int
	steerBuf  []provider.Message

	// Turn slot: exactly one turn at a time may write this session's
	// history. turnActive is true while someone holds the slot; turnWaiters
	// is the FIFO of callers queued behind it, each parked on its own
	// channel. Handoff closes the waiter's channel and keeps turnActive set,
	// so the slot is never momentarily free (a fresh caller cannot jump the
	// queue). Guarded by mu; never held while blocking on a waiter.
	//
	// See docs/session-turn-integrity.md — clause W. Two turns appending to
	// one session interleave their messages and can leave a tool reply
	// separated from its call, which is what poisoned session
	// hJKMWwtOp3mJOtqN8Uz2mW in production (2026-09-13).
	turnActive  bool
	turnWaiters []chan struct{}

	// lastTouched is when this session was last handed out by Manager.Get. The
	// cache uses it to drop entries nothing is working on.
	lastTouched time.Time

	// turnFence is the (holder, epoch) pair this session's writes must present
	// while a turn holds the cross-replica lease (docs/session-turn-integrity.md
	// A1.4). Set at admission, refreshed by every renew, cleared when the turn
	// ends or loses the lease. Nil means "unfenced": no lease is wired
	// (single-instance installs, tests, doctor, migrations) and the writes land
	// exactly as they did before. Like chatterUserID, it is read inside
	// Session.Append's critical section and stamped by ctx().
	turnFence *TurnFence
	// fenceLost records the first refused fenced write: the turn was
	// superseded while it was running. The loop reads it at its iteration
	// boundary, stops, and tells the user (A1.4).
	fenceLost error
}

// TurnFence is one possession of a session's turn lease: the holder's identity
// and the fencing epoch it must present on writes. The store checks the pair in
// the same statement as the write, so a superseded turn cannot append.
type TurnFence struct {
	Holder string
	Epoch  int64
}

// ErrSessionFenceLost is this package's own refusal: the turn lost the lease
// between admission and this write, so the write did not land.
//
// It exists as a value of *this* package on purpose. Before it, the inner layer
// compared the store's `ErrSessionFenceLost` — an error type owned by the outer
// package — which is the Dependency Rule applied to an error value: whoever
// refuses (the store) is not necessarily whoever names the refusal (the use
// case). The adapter translates one into the other, exactly as it translates
// TurnFence into the store's fence type.
var ErrSessionFenceLost = errors.New("session: the turn no longer holds the lease; the write was refused")

// FenceLost reports the refusal recorded by a fenced write, or nil. A turn
// that sees it must stop: another turn owns this history now.
func (s *Session) FenceLost() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fenceLost
}

// Fence reports the possession this session's writes must present, or nil
// when the session is unfenced. Exposed for the turn loop's supersession check
// and for tests; the store never reads it — it receives the fence per write.
func (s *Session) Fence() *TurnFence {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnFence
}

// SetTurnFence binds the current lease possession to this session's writes.
func (s *Session) SetTurnFence(holder string, epoch int64) {
	s.mu.Lock()
	s.turnFence = &TurnFence{Holder: holder, Epoch: epoch}
	s.mu.Unlock()
}

// ClearTurnFence unfences the session's writes: called when the turn ends, and
// the moment a renew proves the lease moved to a peer (L4a — the fence has to
// be gone before any later append can land).
func (s *Session) ClearTurnFence() {
	s.mu.Lock()
	s.turnFence = nil
	s.mu.Unlock()
}

// evictIdleLocked drops cached sessions the process no longer needs. It is safe
// by construction: the store is authoritative, so the next Get rebuilds a dropped
// session and no caller can observe the difference. Sessions with work in flight
// (a held turn slot or a turn body running) are never dropped — their in-memory
// state (steer buffer, turn fence, waiter queue) is not rebuildable.
//
// It only does the work below once the cache is over budget, so the common path
// costs a length check.
//
// The budget is a **session count** (agentSessionCacheMaxSessions). The residual is
// stated rather than implied: sessions are not equal in size, so this bounds the
// number of live entries, not the bytes they hold (docs 10 §10.3). What makes it
// the right unit anyway is that every entry costs the same *bookkeeping* and the
// heavy part — the history — is not resident for its own sake: `Get` re-reads it
// from the authoritative store on every call, so evicting an entry costs a
// rebuild that was going to happen anyway.
func (m *Manager) evictIdleLocked(now time.Time) {
	// Count first, decide second. Two things a dropped entry buys back: a session slot and the
	// lines it holds. The candidates carry their own line count so the loop can subtract instead
	// of re-walking (the walk below is already one lock per entry, which is the whole cost).
	type candidate struct {
		key      string
		touched  time.Time
		messages int
	}
	cands := make([]candidate, 0, len(m.sessions))
	totalMessages := 0
	for key, sess := range m.sessions {
		sess.mu.Lock()
		sess.snapshotExpiredLocked(now)
		messages := len(sess.Messages) + len(sess.snapshot)
		busy := sess.turnActive || sess.turnDepth > 0
		touched := sess.lastTouched
		sess.mu.Unlock()
		totalMessages += messages
		if key == m.lastKey {
			continue // the caller is holding this one right now
		}
		if busy {
			continue // in-flight state (steer buffer, fence, waiters) is not rebuildable
		}
		cands = append(cands, candidate{key: key, touched: touched, messages: messages})
	}
	if len(m.sessions) <= agentSessionCacheMaxSessions && totalMessages <= agentSessionCacheMaxMessages {
		// Under budget is not "nothing to do": the idle rule below still drops entries nobody has
		// touched for sessionCacheMaxIdle, so the walk continues (it has already happened).
		anyIdle := false
		for _, c := range cands {
			if now.Sub(c.touched) >= sessionCacheMaxIdle {
				anyIdle = true
				break
			}
		}
		if !anyIdle {
			return
		}
	}
	// Genuinely idle entries go first (sessionCacheMinIdle), then oldest-first.
	// The idle test is an ordering *preference*, never a shield: if everything is
	// fresh we still evict the oldest, because the store rebuilds it and the
	// alternative is unbounded growth.
	sort.Slice(cands, func(a, b int) bool {
		aIdle := now.Sub(cands[a].touched) >= sessionCacheMinIdle
		bIdle := now.Sub(cands[b].touched) >= sessionCacheMinIdle
		if aIdle != bIdle {
			return aIdle
		}
		return cands[a].touched.Before(cands[b].touched)
	})
	for _, c := range cands {
		// Two reasons to drop, and the candidates are sorted so the first one comes first:
		//   - it has gone idle (sessionCacheMaxIdle) — dropped regardless of the budgets;
		//   - the cache is over a budget — dropped oldest-first until it is not.
		idle := now.Sub(c.touched) >= sessionCacheMaxIdle
		if !idle && len(m.sessions) <= agentSessionCacheMaxSessions && totalMessages <= agentSessionCacheMaxMessages {
			return
		}
		delete(m.sessions, c.key)
		totalMessages -= c.messages
	}
}

// cacheMessagesLocked counts the history lines the cache is holding. It is
// instrumentation for the footprint line — not a budget (the budget is the
// session count) — but it is read under each session's lock so the number it
// prints is not sampled by a racy walk.
//
// It counts the undo snapshot too: while it exists it is a second full copy of
// the working set and is NOT rebuildable from the store (Undo restores from
// process memory only), which is why "rebuildable" has to be judged per field,
// not per struct (docs 10 §10.5).
func (m *Manager) cacheMessagesLocked() int {
	total := 0
	for _, sess := range m.sessions {
		sess.mu.Lock()
		total += len(sess.Messages) + len(sess.snapshot)
		sess.mu.Unlock()
	}
	return total
}

// SessionKey returns the opaque session_key this Session is bound to.
// Exposed so per-turn plumbing (e.g. the tool registry binding for
// goal-scoped tools) can address the right row without re-resolving
// the (channel, account, chat) quadruple every time.
func (s *Session) SessionKey() string { return s.sessionKey }

// ctx returns a context tagged with this Session's user so the store layer
// can scope SQL by user_id. Falls back to context.Background() when no
// user is set; the store will then default to config.DefaultUserID.
//
// Also embeds the per-turn chatter (when set) so DBStore session writes
// (sessions.chatter_user_id / session_messages.chatter_user_id /
// session_events.chatter_user_id) can record the actual conversation
// participant. user_id stays = UserSpace owner; chatter is the
// additional dimension. Both tags are independent — empty chatter
// just leaves the column ”.
func (s *Session) ctx() context.Context {
	ctx := context.Background()
	if s.userID != "" {
		ctx = config.WithUserID(ctx, s.userID)
	}
	return ctx
}

// WriteScope is the set of facts about the possession that is writing: which
// conversation, the row's project stamp, the per-turn chatter, and the lease
// possession that authorises the write.
//
// It exists because those facts used to take *two* routes — `fence` and the
// conversation triple as arguments, the chatter as a context value whose key was
// owned by the store package. One concept, two mechanisms, and the second one
// cost the Dependency Rule: this file needed `internal/store` for a single
// tagging helper. Now the scope is passed by value, the inner layer owns its
// vocabulary, and the adapter translates into whatever the store wants
// internally (same shape as `storeFence`).
//
// What deliberately did NOT move here: `provider`, `model` and `runReceipt`.
// Those belong to the *message*, not to the write — their home is the message
// metadata precisely so they survive compaction and a pod rebuild (G9/G20).
type WriteScope struct {
	Channel       string
	AccountID     string
	ChatID        string
	ProjectID     string
	ChatterUserID string
	Fence         *TurnFence
}

// writeScope assembles the scope from the session's current turn facts. Callers
// must hold s.mu (the fields it reads are guarded by it).
func (s *Session) writeScope() WriteScope {
	return newWriteScope(s.channel, s.accountID, s.chatID, s.projectID, s.chatterUserID, s.turnFence)
}

// newWriteScope is the ONLY constructor of a WriteScope. It exists because the
// invariant "one fact, one route" is not a property of the fields but of there
// being exactly one place that fills them — and the first version of this change
// broke its own rule: a second, hand-written literal appeared for the
// row-creation path (the one that stores a brand-new session before it has any
// history). A caller with nothing to say about a field passes "" or nil here
// rather than assembling the struct itself.
func newWriteScope(channel, accountID, chatID, projectID, chatterUserID string, fence *TurnFence) WriteScope {
	return WriteScope{
		Channel:       channel,
		AccountID:     accountID,
		ChatID:        chatID,
		ProjectID:     projectID,
		ChatterUserID: chatterUserID,
		Fence:         fence,
	}
}

// SetChatter binds the per-turn conversation participant to this
// Session so the next Append / SaveSession write stamps the
// chatter_user_id column. Called by the agent loop at the top of each
// turn from the resolved chatterUID. Passing "" clears it (the next
// write goes back to ” which readers fall back to user_id for).
func (s *Session) SetChatter(uid string) {
	s.mu.Lock()
	s.chatterUserID = uid
	s.mu.Unlock()
}

// SetProviderModel binds the current LLM provider and model to this
// Session so Append stamps them onto assistant messages. Called by the
// agent loop alongside SetChatter.
func (s *Session) SetProviderModel(prov, mdl string) {
	s.mu.Lock()
	s.provider = prov
	s.model = mdl
	s.mu.Unlock()
}

// RunReceiptMetadataKey is the metadata key Append stamps onto assistant
// messages: the world this turn started in. It lives here, next to the stamp,
// so the writer and the reader cannot drift apart.
const RunReceiptMetadataKey = "run_receipt"

// SetRunReceipt binds the turn's world snapshot to this Session so Append stamps
// it onto the assistant message. Called by the agent loop alongside
// SetProviderModel, with the document the environment sampler produced.
func (s *Session) SetRunReceipt(doc string) {
	s.mu.Lock()
	s.runReceipt = doc
	s.mu.Unlock()
}

// RunReceiptOf reads back the document Append stamped onto a message ("" when
// this message carries none — a user/tool row, a turn that ran before the stamp
// existed, or a history that was rewritten by compaction).
//
// An empty receipt is the signal's "not sampled", never a default: a fabricated
// baseline would make every turn announce changes that did not happen.
func RunReceiptOf(m provider.Message) string {
	if m.Metadata == nil {
		return ""
	}
	v, _ := m.Metadata[RunReceiptMetadataKey].(string)
	return v
}

// Manager manages sessions for one (user, agent). Sessions are keyed
// internally by an opaque session_key; the (channel, accountID, chatID)
// triple is what callers use to address "the conversation thread the
// user is in right now". The active session for that triple is the
// most recently updated row — `/new` mints a fresh one to start over.
//
// SessionStore is the optional persistence interface (DB-backed in
// production; nil in file-only mode for single-binary dev installs).
//
// Two parallel persistence shapes:
//   - GetSession / SaveSession operate on the LLM-facing working set
//     (post-compaction). This is what the agent loop reads/writes every
//     turn.
//   - AppendMessage / ListMessages operate on the append-only per-turn
//     archive (session_messages table). Compaction never touches it, so
//     UI history / audit reads see the original conversation regardless
//     of how many times the working set has been pruned/summarized.
type SessionStore interface {
	GetSession(ctx context.Context, agentID, sessionKey string) ([]provider.Message, error)
	// SaveSession / AppendMessage carry the turn's write fence when one
	// applies: the (holder, epoch) pair the store must still find live in the
	// same statement that lands the write (docs/session-turn-integrity.md
	// A1.4, obligation L4a). A nil fence means "no lease governs this write" —
	// single-instance installs, the doctor, migrations, tests.
	//
	// The type is this package's, not the store's: the port names its own
	// vocabulary and the adapter translates it, so the inner interface never
	// depends on an outer package's types.
	SaveSession(ctx context.Context, agentID, sessionKey string, messages []provider.Message, scope WriteScope) error
	AppendMessage(ctx context.Context, agentID, sessionKey string, msg provider.Message, scope WriteScope) error
	ListMessages(ctx context.Context, agentID, sessionKey string) ([]provider.Message, error)
	ListWebSessions(ctx context.Context, agentID string) ([]WebSession, error)
	DeleteSession(ctx context.Context, agentID, sessionKey string) error
	RenameSession(ctx context.Context, agentID, sessionKey, title string) error
	// MoveSession reassigns a session to a different project (or
	// detaches when projectID is ""). Used by the sidebar drag-and-drop
	// affordance; workspace file migration is the caller's job.
	MoveSession(ctx context.Context, agentID, sessionKey, projectID string) error
	// ResolveActiveSessionKey returns the most recent session_key for the
	// (channel, accountID, chatID) triple, or empty string if none.
	ResolveActiveSessionKey(ctx context.Context, agentID, channel, accountID, chatID string) (string, error)
	// LookupSessionTriple is the inverse — given a session_key, return
	// the conversation it belongs to. Returns ("","","",nil) when the
	// session doesn't exist (manager treats that as "not yet stored").
	LookupSessionTriple(ctx context.Context, agentID, sessionKey string) (channel, accountID, chatID string, err error)
	// LookupSessionProject returns the project_id stamped on the session
	// row, or "" for loose chats. Used by the agent runtime to thread
	// project context onto inbound messages so the workspace store and
	// sandbox both route to projects/<pid>/.
	LookupSessionProject(ctx context.Context, agentID, sessionKey string) (string, error)
}

// The session cache is a CACHE, not a store: the store (or, in file-backed
// mode, the file) is authoritative and every Get rebuilds from it. So its size
// must be bounded by work in flight, not by how much history this process has
// served — otherwise a long-lived pod's memory grows with every session and
// every turn (the serverless invariant S1).
const (
	// sessionCacheMaxIdle is now a DROP rule as well as an ordering preference: an entry no
	// one has touched for this long is dropped even when both budgets are satisfied, because
	// 'nobody is using it' is a reason to let it go on its own — the store rebuilds it on the
	// next Get, and the alternative is a warm cache that outlives the conversation merely
	// because the process happened to be under budget.
	sessionCacheMaxIdle = 30 * time.Minute
	// sessionCacheMinIdle marks entries too fresh to prefer dropping; it never
	// prevents reaching the bound (see evictIdleLocked).
	sessionCacheMinIdle = 2 * time.Minute
	// sessionSnapshotMaxAge is how long an undo snapshot may keep pinning history. The snapshot is
	// a shallow copy (Go copies the struct headers; Content/Thinking/Arguments are strings, so the
	// BYTES are shared), which is why its own cost is ~200 B per line and not a second copy. What it
	// does cost is TIME: every message that later replaced the ones it holds stays reachable until the
	// snapshot goes, and it used to go only on Undo or eviction. Measured shape it is meant to stop:
	// a long conversation where someone took a snapshot once and never undid it.
	sessionSnapshotMaxAge = 30 * time.Minute
	// agentSessionCacheMaxSessions is one AGENT's cache budget, counted in
	// **sessions**: that agent's Manager may hold this many, and the LRU drops the
	// rest. "Per agent" is in the name because it is a real scope — a Manager is
	// built per agent (`internal/agent/manager.go:246`), so a pod holding K loaded
	// agents may hold K × this many entries. That is the intended shape (each
	// agent's own conversations are the ones worth keeping warm); a pod-wide quota
	// would be a different mechanism, see docs 10 §10.7.
	//
	// Two knobs, not one: this count, and agentSessionCacheMaxMessages below. The count
	// alone cannot bound bytes (one session's size is not fixed), which is what the
	// measurements in this block were already saying.
	//
	// What it buys and what it does not, stated rather than implied:
	//   - it bounds the *bookkeeping* (identity, in-flight state, lastTouched)
	//     by concurrency rather than by how much history the pod has served, so
	//     a long-lived process stops accumulating entries forever (S1);
	//   - it does **not** bound bytes, because one session's size is not fixed.
	//     Measured on this repo (docs 10 §10.3): the same cache costs 2.3 MiB
	//     with small chats and 77.1 MiB with tool-output-heavy ones. Production's
	//     own shape on 2026-09-19 was 108 sessions / 8366 lines / 31 MiB of text,
	//     and its largest single session held 1.85 MiB.
	//
	// 10, set 2026-09-19. The number is small on purpose: what the cache saves is
	// only allocation work, because `Get` re-reads the working set from the store
	// on every call anyway — so keeping ten conversations warm per agent costs a
	// bounded amount of memory and buys the same thing as keeping a hundred. The
	// eviction path is now the normal path (production has more than ten sessions
	// per agent), and docs 10 §10.6 records the field-by-field proof that dropping
	// an entry is unobservable.
	agentSessionCacheMaxSessions = 10
	// agentSessionCacheMaxMessages is one AGENT's cache budget counted in **lines** — the knob
	// that bounds bytes. Why the session count is not enough, in this file's own numbers: the
	// same ten-entry cache measured 2.3 MiB with small chats and 77.1 MiB with tool-output-heavy
	// ones, and production's largest single session held 1.85 MiB (docs 10 §10.3). A long
	// conversation is exactly the shape that grows, so a count of conversations bounds the
	// bookkeeping and nothing else.
	//
	// 20_000 lines is ~2.4× production's whole-cache figure (8366 lines across 108 sessions,
	// 2026-09-19) and ~10× its average session; it is a ceiling on what the process KEEPS, never
	// on what it can serve — the store is authoritative and `Get` re-reads the working set.
	agentSessionCacheMaxMessages = 20_000
)

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	// cacheGets counts Get() calls. The session cache is a CACHE (the store is
	// authoritative — Get reloads from it every time), so its footprint must be
	// bounded by concurrency, not by how much history this pod has served
	// (serverless invariant S1). This counter exists so that claim is measured
	// rather than assumed: a periodic line in the log says how many sessions and
	// messages the process is holding.
	cacheGets int64
	// lastKey is the session the most recent Get asked for; the sweeper never
	// drops it (the caller is holding it right now).
	lastKey string
	dataDir string
	store   SessionStore
	userID  string
	agentID string
}

func NewManager(dataDir string) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		dataDir:  dataDir,
	}
}

// NewManagerWithStoreForUser is the user-scoped constructor. Caller MUST
// supply a real user_id resolved from auth. We log and keep the Manager
// alive on empty input so a bad request cannot crash the whole gateway;
// downstream store calls will fail closed under the empty owner.
func NewManagerWithStoreForUser(dataDir string, st SessionStore, userID, agentID string) *Manager {
	if userID == "" {
		fmt.Fprintf(os.Stderr, "session.NewManagerWithStoreForUser: empty userID for agent %q\n", agentID)
	}
	return &Manager{
		sessions: make(map[string]*Session),
		dataDir:  dataDir,
		store:    st,
		userID:   userID,
		agentID:  agentID,
	}
}

// ctx returns a context tagged with this Manager's user for store calls.
func (m *Manager) ctx() context.Context {
	if m.userID == "" {
		return context.Background()
	}
	return config.WithUserID(context.Background(), m.userID)
}

// generateSessionKey mints an opaque session_key for a fresh
// conversation thread. The same generator is used regardless of channel
// — `s-<unix_ms>-<rand>`. The (channel, accountID, chatID) triple is
// stored alongside in dedicated columns; the literal session_key string
// no longer encodes channel info, so a `/new` command in IM can mint a
// second key under the same triple without colliding.
func generateSessionKey() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	var rand6 [6]byte
	if _, err := cryptorand.Read(rand6[:]); err != nil {
		// fall back to time-derived bytes — collision is extremely
		// unlikely once the timestamp prefix is in play
		now := time.Now().UnixNano()
		for i := range rand6 {
			rand6[i] = byte(now >> (i * 8))
		}
	}
	suffix := make([]byte, len(rand6))
	for i, b := range rand6 {
		suffix[i] = alphabet[int(b)%len(alphabet)]
	}
	return fmt.Sprintf("s-%d-%s", time.Now().UnixMilli(), suffix)
}

// resolveOrMintKey picks the active session_key for (channel,
// accountID, chatID) from the store, or mints a fresh one when nothing
// exists yet (the very first message in a conversation). Pre-existing
// rows from before the channel-triple migration may carry a key like
// `web_<sid>` or `wechat_<openid>` — they're matched by the backfilled
// triple, not by parsing the key, so the legacy format keeps working.
//
// New-row mint policy:
//   - web: session_key == chatID. Web's chatID *is* the per-conversation
//     identifier (the frontend generates one per "+New chat") so making
//     it equal the session_key keeps the URL `?session=` token stable
//     across reloads — no "URL changed after first message" surprises.
//   - everywhere else: mint an opaque `s-<unix_ms>-<rand>`. IM channels
//     reuse one chatID (the user's openid / chat_id) across many
//     sessions, so the session_key has to be independent for `/new` to
//     produce a sibling row.
func (m *Manager) resolveOrMintKey(channel, accountID, chatID string) string {
	if m.store != nil {
		if k, err := m.store.ResolveActiveSessionKey(m.ctx(), m.agentID, channel, accountID, chatID); err == nil && k != "" {
			return k
		}
	}
	if channel == "web" && chatID != "" {
		return chatID
	}
	return generateSessionKey()
}

// Get returns or creates the active session for the (channel, accountID,
// chatID) triple. The session_key is resolved server-side rather than
// derived from the inputs — see resolveOrMintKey.
//
// projectID is the "this chat belongs to project X" hint from the chat
// request (URL `?project=<pid>`). It only matters on first save: if the
// session row already has project_id stored, that wins; if the row is
// brand new, this hint is what gets persisted.
//
// In multi-replica deployments (store-backed mode), every Get() reloads
// Messages from the store so a request served by pod B sees writes made
// by pod A. Without this, each pod's in-memory cache drifts away from
// Postgres: the first refresh after a cross-pod write returns whichever
// pod-local snapshot happened to be warm. We deliberately overwrite
// Messages on the cached Session rather than re-creating the struct so
// the transient field (snapshot) survives. `LastConsolidated` used to be listed
// here; it was dead state — nothing read it, nothing persisted it, and its two
// accessors had zero references repo-wide (docs 10 §10.6, G29) — so it is gone
// rather than being kept alive by a comment.
//
// File-backed mode stays cache-first since there's only one process.
func (m *Manager) Get(channel, accountID, chatID, projectID string) *Session {
	key := m.resolveOrMintKey(channel, accountID, chatID)
	return m.getByKey(key, channel, accountID, chatID, projectID)
}

// GetByKey loads a specific session by its session_key. Used when the
// caller already has a key in hand (e.g. web history fetch from a URL
// `?session=…`) and wants to bypass the active-session lookup.
func (m *Manager) GetByKey(sessionKey string) *Session {
	return m.getByKey(sessionKey, "", "", "", "")
}

// LookupSessionProject returns the project_id of a session row (or ""
// if loose / not yet stored). Used by the agent runtime to populate
// InboundMessage.ProjectID so workspace IO routes to projects/<pid>/.
func (m *Manager) LookupSessionProject(sessionKey string) string {
	if m.store == nil || sessionKey == "" {
		return ""
	}
	pid, err := m.store.LookupSessionProject(m.ctx(), m.agentID, sessionKey)
	if err != nil {
		return ""
	}
	return pid
}

// LookupSessionTriple forwards to the store's session_key → triple
// lookup. Returns ("","","",nil) when the row doesn't exist, mirroring
// the SessionStore implementation. Callers should use SessionExists
// first if they need to distinguish "no row" from "row with empty
// triple" (e.g. file-backed dev mode where the store is nil).
func (m *Manager) LookupSessionTriple(sessionKey string) (channel, accountID, chatID string, err error) {
	if m.store == nil {
		return "", "", "", nil
	}
	return m.store.LookupSessionTriple(m.ctx(), m.agentID, sessionKey)
}

// SessionExists reports whether a session row already exists under the
// given session_key. Used by agent-side URL resolvers: a `?session=…`
// token can be either a canonical session_key or a legacy web chat_id,
// and the lookup needs a cheap way to tell which.
func (m *Manager) SessionExists(sessionKey string) bool {
	if m.store == nil {
		// File-backed mode has no negative-lookup primitive — assume
		// yes so the legacy chat_id fallback isn't preferred over the
		// caller's intent. The follow-up GetByKey will load whatever's
		// on disk (empty file → empty Session, harmless).
		return true
	}
	msgs, err := m.store.GetSession(m.ctx(), m.agentID, sessionKey)
	return err == nil && msgs != nil
}

// ResolveSessionKey turns a URL token (`?session=…`) into the
// canonical session_key. Accepts either:
//   - a session_key directly (the ID surfaced by ListWebSessions)
//   - a legacy web chat_id (older URLs and the frontend's freshly-
//     generated id on the *first* turn of a "+New chat")
//
// Returns the input unchanged when nothing matches — callers' downstream
// load/save will then create the row, which is correct for brand-new
// web chats where the URL token is the about-to-exist session_key.
func (m *Manager) ResolveSessionKey(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	if m.SessionExists(sessionID) {
		return sessionID
	}
	if m.store != nil {
		if k, err := m.store.ResolveActiveSessionKey(m.ctx(), m.agentID, "web", "", sessionID); err == nil && k != "" {
			return k
		}
	}
	return sessionID
}

// OpenNewSession mints a brand new session under the same (channel,
// accountID, chatID) triple and returns its session_key. The next Get
// for that triple will pick it up (it has the freshest updated_at).
// Used by IM `/new` / `/reset` commands and any future "start new
// conversation" UI affordance.
func (m *Manager) OpenNewSession(channel, accountID, chatID string) string {
	key := generateSessionKey()
	if m.store != nil {
		// Persist an empty row immediately so the active-session lookup
		// for the next inbound message resolves to this key, not the
		// previous (still-newer-than-not-existing) row. IM `/new` is
		// always a loose chat (project_id=""); project chats are
		// minted lazily by the chat handler on first message.
		// A row with no history yet: the scope carries only where it lives, and
		// it is built by the one constructor like every other scope.
		_ = m.store.SaveSession(m.ctx(), m.agentID, key, nil, newWriteScope(channel, accountID, chatID, "", "", nil))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &Session{
		filePath:   filepath.Join(m.dataDir, key+".jsonl"),
		store:      m.store,
		userID:     m.userID,
		agentID:    m.agentID,
		sessionKey: key,
		channel:    channel,
		accountID:  accountID,
		chatID:     chatID,
	}
	m.sessions[key] = s
	return key
}

func (m *Manager) getByKey(key, channel, accountID, chatID, projectID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.sessions[key]; ok {
		if m.store != nil {
			if msgs, err := m.store.GetSession(m.ctx(), m.agentID, key); err == nil {
				s.mu.Lock()
				s.Messages = msgs
				s.mu.Unlock()
			}
		}
		// Late-bind the triple + project on cached entries created via
		// GetByKey or earlier hint-less paths. Once stamped, project_id
		// on the persisted row is authoritative — we only ever fill in
		// the empty case so a hint mismatch can't overwrite the truth.
		if channel != "" || projectID != "" {
			s.mu.Lock()
			if s.channel == "" && channel != "" {
				s.channel, s.accountID, s.chatID = channel, accountID, chatID
			}
			if s.projectID == "" && projectID != "" {
				s.projectID = projectID
			}
			s.mu.Unlock()
		}
		return s
	}

	filePath := filepath.Join(m.dataDir, key+".jsonl")

	s := &Session{
		filePath:   filePath,
		store:      m.store,
		userID:     m.userID,
		agentID:    m.agentID,
		sessionKey: key,
		channel:    channel,
		accountID:  accountID,
		chatID:     chatID,
		projectID:  projectID,
	}

	// Load from store (DB) if available, otherwise from file
	if m.store != nil {
		msgs, err := m.store.GetSession(m.ctx(), m.agentID, key)
		if err == nil && len(msgs) > 0 {
			s.Messages = msgs
		}
	} else {
		s.load()
	}

	m.lastKey = key
	s.lastTouched = time.Now()
	m.sessions[key] = s
	m.evictIdleLocked(time.Now())
	// Occasional footprint line (every 100 cache-touching calls). Cheap, and the
	// only way to tell "bounded by concurrency" from "growing with history".
	m.cacheGets++
	if m.cacheGets%100 == 0 {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		fmt.Fprintf(os.Stderr, "session cache footprint: sessions=%d messages=%d (LINES, not bytes; bytes=heapAllocMiB) sessionBudget=%d messageBudget=%d heapAllocMiB=%.1f gets=%d\n",
			len(m.sessions), m.cacheMessagesLocked(), agentSessionCacheMaxSessions, agentSessionCacheMaxMessages, float64(ms.HeapAlloc)/(1<<20), m.cacheGets)
	}
	return s
}

// Append adds a message to the session and persists it.
//
// Store-backed mode writes to TWO places:
//   - SaveSession overwrites the LLM-facing working set in the sessions
//     table (the array the agent loop reads next turn);
//   - AppendMessage inserts the new turn into session_messages, the
//     append-only archive that survives compaction.
//
// The archive write is best-effort (logged on failure but not surfaced)
// — losing one archive row is recoverable from the working set, and we
// don't want history to silently drop chat replies if the audit table
// hiccups.
// Key returns the opaque session_key this Session is bound to.
// Exposed so callers that need to tag external records by session
// (e.g. usage metering's per-session token rollup) don't have to
// reach into the struct.
func (s *Session) Key() string { return s.sessionKey }

func (s *Session) Append(msg provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Auto-set timestamp if not provided
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixMilli()
	}
	// Stamp provider/model on assistant messages so the archive
	// records which LLM produced each response.
	if msg.Role == "assistant" && msg.Provider == "" && s.provider != "" {
		msg.Provider = s.provider
		msg.Model = s.model
	}
	// The same receipt also carries the world this turn started in. It is
	// metadata rather than a new column because that is exactly what "which LLM
	// produced this response" already is: a fact about THIS turn, recorded with
	// it, instead of a separate record someone has to keep in sync (docs 10 §4,
	// G9 + G20). Metadata is never sent to the model, so this cannot leak into
	// the prompt — the agent learns about a change from the environment signal,
	// which is the exit that exists for exactly this.
	if msg.Role == "assistant" && s.runReceipt != "" {
		if msg.Metadata == nil {
			msg.Metadata = map[string]any{}
		}
		msg.Metadata[RunReceiptMetadataKey] = s.runReceipt
	}

	s.Messages = append(s.Messages, msg)

	if s.store != nil {
		// Every fact about who is writing travels in one value: the fence (the
		// write's precondition, L4a) and the chatter (per-turn identity) take the
		// same route, because they are the same kind of fact.
		scope := s.writeScope()
		s.store.SaveSession(s.ctx(), s.agentID, s.sessionKey, s.Messages, scope)
		if err := s.store.AppendMessage(s.ctx(), s.agentID, s.sessionKey, msg, scope); err != nil {
			// A refused write means the lease moved to another turn: remember
			// it so the loop stops and says so (A1.4) instead of appending
			// into a history it no longer owns.
			if errors.Is(err, ErrSessionFenceLost) {
				s.fenceLost = err
			}
			fmt.Fprintf(os.Stderr, "session archive append error: %v\n", err)
		}
	} else {
		s.appendToFile(msg)
	}
}

// ArchivedMessages returns the full append-only history for this session.
// Falls back to the in-memory working set when no store is configured or
// the archive is empty (e.g. file-backed mode, or a session created
// before the archive table existed).
func (s *Session) ArchivedMessages() []provider.Message {
	s.mu.Lock()
	// Named `st`, not `store`: the previous name shadowed the `store` package,
	// which made a package-level dependency look like a method call here (and
	// fooled a grep, and me, into reporting one that did not exist).
	st := s.store
	agentID := s.agentID
	sessionKey := s.sessionKey
	s.mu.Unlock()
	if st == nil {
		return s.GetMessages()
	}
	msgs, err := st.ListMessages(s.ctx(), agentID, sessionKey)
	if err != nil || len(msgs) == 0 {
		return s.GetMessages()
	}
	return msgs
}

// GetMessages returns a copy of all messages.
func (s *Session) GetMessages() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	return msgs
}

// AcquireTurn blocks until this caller holds the session's single turn slot,
// or until ctx ends. It returns true only when the caller owns the slot and
// MUST eventually call ReleaseTurn.
//
// Why a session-level gate and not a per-chat-key lock: the session is the
// unit that owns history, and several (channel, accountID, chatID) triples
// can resolve to one session (shared-identity channels, URL-token recovery).
// Serialising on the chat tuple leaves exactly the hole that poisoned
// production: a dashboard turn and a cron-fired turn writing one history.
//
// Waiters are served first-in-first-out so a queued conversation keeps the
// order the user typed it in. A caller that gives up (ctx canceled) leaves
// the queue without stranding the slot, including when the cancellation
// races the handoff.
func (s *Session) AcquireTurn(ctx context.Context) bool {
	s.mu.Lock()
	if !s.turnActive {
		// The uncontended path still has to look at ctx. Without this check a caller whose context was
		// canceled while the slot was free (or while its goroutine had not been scheduled yet — the
		// window is real: CI hit it under `-race` with the whole-tree gate on 2026-09-23) takes the
		// slot and returns true, so the caller that was refused is now holding a slot nobody expects
		// it to release. The test that found it, `TestAcquireTurnCancelAtHandoffDoesNotStrandSlot`,
		// documents the invariant: cancel racing release must leave the slot free.
		if ctx.Err() != nil {
			s.mu.Unlock()
			return false
		}
		s.turnActive = true
		s.mu.Unlock()
		return true
	}
	waiter := make(chan struct{})
	s.turnWaiters = append(s.turnWaiters, waiter)
	s.mu.Unlock()

	select {
	case <-waiter:
		// The slot was handed to us. If the caller already gave up, pass it
		// on instead of holding a slot nobody will release.
		if ctx.Err() != nil {
			s.ReleaseTurn()
			return false
		}
		return true
	case <-ctx.Done():
		s.mu.Lock()
		removed := false
		for i, w := range s.turnWaiters {
			if w == waiter {
				s.turnWaiters = append(s.turnWaiters[:i], s.turnWaiters[i+1:]...)
				removed = true
				break
			}
		}
		s.mu.Unlock()
		if !removed {
			// Lost the race: the slot was handed over while we were
			// canceling. Give it to the next waiter.
			s.ReleaseTurn()
		}
		return false
	}
}

// ReleaseTurn frees the turn slot and hands it to the longest-waiting caller.
// Calling it without holding the slot is a no-op rather than a way to let a
// second turn in.
func (s *Session) ReleaseTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.turnActive {
		return
	}
	if len(s.turnWaiters) > 0 {
		next := s.turnWaiters[0]
		s.turnWaiters = s.turnWaiters[1:]
		// Ownership transfers: turnActive stays true so a new caller cannot
		// slip in between the release and the woken waiter's first append.
		close(next)
		return
	}
	s.turnActive = false
}

// TurnActive reports whether a turn currently holds this session's slot.
func (s *Session) TurnActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnActive
}

// TurnInFlight reports whether a turn of this session is running (BeginTurn not
// yet paired with EndTurn) or waiting for the slot. It is the signal the user
// space release uses to decide that an MCP client may still be in use: the only
// caller of an MCP tool is a turn (docs 10 §3.4).
func (s *Session) TurnInFlight() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnDepth > 0 || s.turnActive || len(s.turnWaiters) > 0
}

// TurnWaiters reports how many turn-start callers are queued behind the
// current holder. Used by metric/log surfaces and tests.
func (s *Session) TurnWaiters() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.turnWaiters)
}

// AnyTurnInFlight reports whether any session this manager holds has a turn
// running or waiting for the slot. Callers must be able to tolerate a turn
// starting right after it returns false — it is a probe, not a lock.
func (m *Manager) AnyTurnInFlight() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		if s.TurnInFlight() {
			return true
		}
	}
	return false
}

// BeginTurn marks a HandleMessage turn as in-flight for this session.
// Paired with EndTurn. Steering messages are only accepted while at
// least one turn is active.
func (s *Session) BeginTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnDepth++
}

// EndTurn marks a turn as finished. When the last in-flight turn ends it
// returns any steer messages still buffered (the end-of-turn race: a
// message pushed after the loop's final drain). Callers redispatch the
// leftovers as a fresh turn.
func (s *Session) EndTurn() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnDepth > 0 {
		s.turnDepth--
	}
	if s.turnDepth > 0 || len(s.steerBuf) == 0 {
		return nil
	}
	leftover := s.steerBuf
	s.steerBuf = nil
	return leftover
}

// PushSteerIfActive buffers a steering message iff a turn is currently
// in-flight. Returns false when no turn is active, so the caller can
// fall back to dispatching the message as a normal new turn. The return
// value is the single source of truth — there is deliberately no
// separate "is running" probe to race against.
func (s *Session) PushSteerIfActive(msg provider.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnDepth == 0 {
		return false
	}
	s.steerBuf = append(s.steerBuf, msg)
	return true
}

// DrainSteer atomically returns and clears the buffered steer messages.
// The running loop calls this between tool iterations.
func (s *Session) DrainSteer() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steerBuf) == 0 {
		return nil
	}
	drained := s.steerBuf
	s.steerBuf = nil
	return drained
}

// ReplaceMessages replaces all session messages with the given list.
// This is used after context compaction to trim the session.
func (s *Session) ReplaceMessages(msgs []provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Messages = make([]provider.Message, len(msgs))
	copy(s.Messages, msgs)

	if s.store != nil {
		s.store.SaveSession(s.ctx(), s.agentID, s.sessionKey, s.Messages, s.writeScope())
	} else {
		s.rewriteFile()
	}
}

// Clear resets the session messages.
func (s *Session) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = nil
	if s.store != nil {
		s.store.DeleteSession(s.ctx(), s.agentID, s.sessionKey)
	} else {
		os.Remove(s.filePath)
	}
}

func (s *Session) load() {
	f, err := os.Open(s.filePath)
	if err != nil {
		return // file doesn't exist yet
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var msg provider.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		s.Messages = append(s.Messages, msg)
	}
}

func (s *Session) rewriteFile() {
	dir := filepath.Dir(s.filePath)
	os.MkdirAll(dir, 0o755)

	f, err := os.Create(s.filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session rewrite error: %v\n", err)
		return
	}
	defer f.Close()

	for _, msg := range s.Messages {
		data, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		f.Write(data)
		f.Write([]byte("\n"))
	}
}

func (s *Session) appendToFile(msg provider.Message) {
	dir := filepath.Dir(s.filePath)
	os.MkdirAll(dir, 0o755)

	f, err := os.OpenFile(s.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session persist error: %v\n", err)
		return
	}
	defer f.Close()

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	f.Write(data)
	f.Write([]byte("\n"))
}

// WebSession holds metadata for one chat session surfaced to the
// dashboard. Despite the historical name it now spans every channel —
// the Channel field tells callers which one to render the icon for.
//
// ID is the session_key (the row's PK), not the chat_id. Older URLs
// pointing at a chat_id still resolve via the agent-side fallback
// (ResolveSessionKey) so existing bookmarks don't break.
type WebSession struct {
	ID        string `json:"id"`
	Channel   string `json:"channel,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	// ProjectID groups this chat under a per-(user, agent) project
	// folder. Empty = loose chat. Surfaced so the sidebar can section
	// chats by project.
	ProjectID string `json:"projectId,omitempty"`
	Title     string `json:"title"`
	Preview   string `json:"preview"`
	CreatedAt int64  `json:"createdAt"` // unix ms
	UpdatedAt int64  `json:"updatedAt"` // unix ms
	// ThumbnailURL is the first image_url attached to the FIRST user
	// turn of the session, surfaced so the sidebar can show "image +
	// text" instead of just the text label for multimodal chats.
	// Empty for sessions whose opening message had no image.
	ThumbnailURL string `json:"thumbnailUrl,omitempty"`
	// ChatterUserID is the actual conversation participant (app_user
	// for IM channels). Differs from user_id when an IM sender is
	// resolved to a per-sender app_user under the channel owner's space.
	ChatterUserID string `json:"chatterUserId,omitempty"`
}

// ListWebSessions scans session files for web chat sessions and returns
// a list with id, title, preview, and timestamps.
func (m *Manager) ListWebSessions() []WebSession {
	if m.store != nil {
		sessions, err := m.store.ListWebSessions(m.ctx(), m.agentID)
		if err == nil {
			return sessions
		}
	}
	pattern := filepath.Join(m.dataDir, "web_*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}

	var sessions []WebSession
	for _, f := range files {
		base := filepath.Base(f)
		// "web_<sessionId>.jsonl" -> "<sessionId>"
		sessionId := strings.TrimPrefix(base, "web_")
		sessionId = strings.TrimSuffix(sessionId, ".jsonl")

		info, err := os.Stat(f)
		if err != nil {
			continue
		}

		// Read first user message as preview
		preview := ""
		thumb := ""
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(fh)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			// Multimodal user turns store text inside content_parts and
			// leave content empty — read both shapes so the preview
			// doesn't latch onto a later plain message and mislabel the
			// session, and pull the first image_url so the sidebar can
			// surface a thumbnail.
			var msg struct {
				Role         string                 `json:"role"`
				Content      string                 `json:"content"`
				ContentParts []provider.ContentPart `json:"content_parts"`
			}
			if json.Unmarshal(scanner.Bytes(), &msg) != nil || msg.Role != "user" {
				continue
			}
			text := msg.Content
			img := ""
			if text == "" {
				var parts []string
				for _, p := range msg.ContentParts {
					if p.Type == "text" && p.Text != "" {
						parts = append(parts, p.Text)
					}
				}
				text = strings.Join(parts, "\n")
			}
			text = provider.StripAttachedPrefix(text)
			for _, p := range msg.ContentParts {
				if p.Type == "image_url" && p.ImageURL != nil && p.ImageURL.URL != "" {
					img = p.ImageURL.URL
					break
				}
			}
			if text == "" && img == "" {
				continue
			}
			preview = text
			if preview == "" {
				preview = "[image]"
			}
			if len([]rune(preview)) > 100 {
				preview = string([]rune(preview)[:100]) + "..."
			}
			thumb = img
			break
		}
		fh.Close()

		if preview == "" {
			continue // skip empty sessions
		}

		// Read title from metadata file, fallback to preview
		title := m.readSessionTitle(sessionId)
		if title == "" {
			title = preview
			if len([]rune(title)) > 60 {
				title = string([]rune(title)[:60]) + "..."
			}
		}

		sessions = append(sessions, WebSession{
			ID:           sessionId,
			Title:        title,
			Preview:      preview,
			ThumbnailURL: thumb,
			CreatedAt:    info.ModTime().UnixMilli(),
			UpdatedAt:    info.ModTime().UnixMilli(),
		})
	}

	// Sort by updatedAt descending (newest first)
	for i := 0; i < len(sessions); i++ {
		for j := i + 1; j < len(sessions); j++ {
			if sessions[j].UpdatedAt > sessions[i].UpdatedAt {
				sessions[i], sessions[j] = sessions[j], sessions[i]
			}
		}
	}

	return sessions
}

// resolveWebSessionKey maps a web sessionId (the URL `?session=` token,
// which is the conversation's chat_id) to its current session_key. New
// rows have an opaque session_key (different from chat_id); legacy rows
// still carry the `web_<sid>` form. Falls back to the legacy literal
// when no row exists yet so file-backed mode and brand-new sessions
// don't error on rename/delete.
func (m *Manager) resolveWebSessionKey(sessionId string) string {
	if m.store != nil {
		if k, err := m.store.ResolveActiveSessionKey(m.ctx(), m.agentID, "web", "", sessionId); err == nil && k != "" {
			return k
		}
	}
	return "web_" + sessionId
}

// DeleteSessionByID resolves a URL token (session_key or legacy web
// chat_id) and deletes the matching session. Channel-agnostic — used
// by the dashboard to delete any-channel chats.
func (m *Manager) DeleteSessionByID(sessionId string) error {
	key := m.ResolveSessionKey(sessionId)
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()
	if m.store != nil {
		return m.store.DeleteSession(m.ctx(), m.agentID, key)
	}
	// File-backed mode only had a "web_<sid>" filename convention; non-
	// web sessions don't reach this path in dev mode, so the legacy
	// fallback in DeleteWebSession is sufficient.
	return m.DeleteWebSession(sessionId)
}

// RenameSessionByID resolves a URL token and renames the matching
// session.
func (m *Manager) RenameSessionByID(sessionId, title string) error {
	key := m.ResolveSessionKey(sessionId)
	if m.store != nil {
		return m.store.RenameSession(m.ctx(), m.agentID, key, title)
	}
	return m.RenameWebSession(sessionId, title)
}

// MoveSessionByID reassigns a session to a different project (or
// detaches when projectID is ""). Resolves either a session_key or a
// legacy web chat_id.
//
// It re-stamps the cached entry *in place* if one exists, and the store
// write is the authority either way: an evicted-and-rebuilt entry takes
// project_id from the caller's hint, and `SaveSession`'s ON CONFLICT clause
// deliberately does not touch project_id on an existing row — so a rebuilt
// entry can neither resurrect the old project nor blank the new one.
//
// File-backed mode is a no-op (no project concept) — callers that
// only run dev mode shouldn't reach this path.
func (m *Manager) MoveSessionByID(sessionId, projectID string) error {
	key := m.ResolveSessionKey(sessionId)
	m.mu.Lock()
	if s, ok := m.sessions[key]; ok {
		s.mu.Lock()
		s.projectID = projectID
		s.mu.Unlock()
	}
	m.mu.Unlock()
	if m.store != nil {
		return m.store.MoveSession(m.ctx(), m.agentID, key, projectID)
	}
	return nil
}

// DeleteWebSession removes a web chat session file and its metadata.
func (m *Manager) DeleteWebSession(sessionId string) error {
	key := m.resolveWebSessionKey(sessionId)

	// Remove from in-memory cache
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()

	if m.store != nil {
		return m.store.DeleteSession(m.ctx(), m.agentID, key)
	}

	safeId := strings.ReplaceAll(sessionId, "/", "_")
	safeId = strings.ReplaceAll(safeId, "..", "_")
	sessionFile := filepath.Join(m.dataDir, "web_"+safeId+".jsonl")
	metaFile := filepath.Join(m.dataDir, "web_"+safeId+".meta.json")
	os.Remove(metaFile)
	return os.Remove(sessionFile)
}

// RenameWebSession sets a custom title for a web chat session.
func (m *Manager) RenameWebSession(sessionId, title string) error {
	if m.store != nil {
		return m.store.RenameSession(m.ctx(), m.agentID, m.resolveWebSessionKey(sessionId), title)
	}

	safeId := strings.ReplaceAll(sessionId, "/", "_")
	safeId = strings.ReplaceAll(safeId, "..", "_")
	metaFile := filepath.Join(m.dataDir, "web_"+safeId+".meta.json")
	data, _ := json.Marshal(map[string]string{"title": title})
	return os.WriteFile(metaFile, data, 0o644)
}

// readSessionTitle reads the title from a session metadata file.
func (m *Manager) readSessionTitle(sessionId string) string {
	safeId := strings.ReplaceAll(sessionId, "/", "_")
	safeId = strings.ReplaceAll(safeId, "..", "_")

	metaFile := filepath.Join(m.dataDir, "web_"+safeId+".meta.json")
	data, err := os.ReadFile(metaFile)
	if err != nil {
		return ""
	}
	var meta struct {
		Title string `json:"title"`
	}
	json.Unmarshal(data, &meta)
	return meta.Title
}

// Snapshot saves the current message list as a restore point (for undo).
func (s *Session) Snapshot() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Shallow on purpose: the structs are copied, the strings they point at are shared. A deep copy
	// would be the one thing that actually doubles bytes, and nothing here needs it (Undo puts the
	// same values back).
	s.snapshot = make([]provider.Message, len(s.Messages))
	copy(s.snapshot, s.Messages)
	s.snapshotAt = time.Now()
}

// Undo restores the last snapshot. Returns false if no snapshot exists.
func (s *Session) Undo() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot == nil {
		return false
	}
	if s.snapshotExpiredLocked(time.Now()) {
		return false
	}
	s.Messages = make([]provider.Message, len(s.snapshot))
	copy(s.Messages, s.snapshot)
	s.snapshot = nil
	s.snapshotAt = time.Time{}
	s.rewriteFile()
	return true
}

// HasSnapshot returns true if an undo snapshot exists.
func (s *Session) HasSnapshot() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot != nil && !s.snapshotExpiredLocked(time.Now())
}

// snapshotExpiredLocked reports whether the snapshot has outlived its window, and releases it if so.
// Called from the reader paths (so a command never sees a stale one) and from the cache walk below
// (so a session nobody asks about still lets its history go).
func (s *Session) snapshotExpiredLocked(now time.Time) bool {
	if s.snapshot == nil || s.snapshotAt.IsZero() {
		return false
	}
	if now.Sub(s.snapshotAt) < sessionSnapshotMaxAge {
		return false
	}
	s.snapshot = nil
	s.snapshotAt = time.Time{}
	return true
}
