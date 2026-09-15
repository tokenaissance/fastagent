# Chat event delivery: the event log is the transport of record

**Status**: ✅ landed (see §6 for the tests that pin it)
**Scope**: `/api/chat/subscribe` — an SSE subscriber must see a turn's events no
matter which replica ran the turn.
**Related**: `docs/session-turn-integrity.md` (turn + event semantics),
`tokenaissance-cloud/docs/fastagent/design/09-delegate-task-design.md` §5.5
(the user-visible symptom this fixes).

## 1. The problem, in one screen

`handleChatSubscribe` (`internal/setup/handlers.go`) does three things:

```
1. hub.Subscribe(uid, agentID, sessionID)                 ← before the replay, so nothing falls through
2. one-shot replay: ListSessionEventsSince(…, sinceSeq)   ← runs ONCE, at connect time
3. for { select { hub / legacy webChan / 30s keepalive / ctx.Done } }   ← after that: hub only
```

The hub is **in-process** (`internal/agent/event_hub.go`: *"In-memory only —
multi-pod deploys need to swap this for redis pub/sub or similar"*). So when a
turn runs on pod A and the SSE connection landed on pod B, **that turn's events
never reach the client — permanently, not slowly**:

* the browser's `EventSource` is long-lived (closed only on unmount / session
  switch; `onerror` says "auto-reconnects … only close on unmount"), and
* the server sends a `: ping` every 30s, so no proxy cuts the idle connection,
* therefore step 2 never runs again.

User-visible shape: a `delegate_task` row sits at `Queued (waiting on prior
sub-agent)…` / `Executing...` until the page is reloaded or the session
switched — **even though the `tool_result` was in the database the whole time**.
That is the 2026-09-14 production report.

## 2. Options, and why this one

| Option | Cost | Verdict |
|---|---|---|
| Redis / pub-sub | new component, new deploy dependency, new failure mode | **No** — the events are already in `session_events` with a monotonic per-session `seq`; we would be adding a second copy of the same fact |
| Client-side polling of history | no server change | **No** — history is messages, not the event stream; the panel's live updates are incremental (`tool_call` → `tool_result` by id), and re-reading history mid-turn fights the streaming bubble |
| Let proxies cut idle SSE so the browser reconnects | none | **No** — latency becomes the proxy's idle timeout, and it breaks the same-pod fast path |
| **Subscriber-side DB tail** | ~20 lines + one ticker, reusing the existing index and `seq` | **Yes** |

One sentence: **promote the event log to transport of record, and demote the hub
to the same-pod fast path (0 ms).** No Redis, no publisher change — that is why
it is cheap.

## 3. Architecture

### Four-layer map

| Layer | What is involved | Where |
|---|---|---|
| Entities | — | — |
| Use Cases | nothing: the semantics already exist (monotonic `seq`, event types, `done`) | `internal/agent/events.go` |
| Interface Adapters | the SSE handler renders *one event record* into one SSE frame — it gains a **second input** (the tail) | `internal/setup/handlers.go`, `internal/setup/chat_event_tail.go` |
| Frameworks & Drivers | the event store (`session_events`, index `idx_session_events_lookup`) and the hub | `internal/store`, `internal/agent/event_hub.go` |

```
turn (any pod) ──► session_events ──(ListSessionEventsSince)──► SSE handler ──► client
       └────────► EventHub (same pod only) ─────────────────────┘
```

### Dependency Rule

No new dependency and no new port: `ListSessionEventsSince` is already on
`store.Store` (owned by the handler's layer), and the handler already depends on
that interface. This is not a DIP problem — it is **one port with two sources**,
and the second source was always there. Nothing in Use Case or Entities learns
about replicas.

## 4. Design decisions

| # | Decision | Why |
|---|---|---|
| D1 | The cursor is `sinceSeq` — one number shared by replay, hub and tail | Three writers, one truth: without it the tail re-sends whatever the hub already delivered |
| D2 | De-duplication is by `seq` (already done by the client, now also by the server) | The client ignores `seq <= maxSeq`; the server must not even send it |
| D3 | `seq < 0` (live-only, unpersisted) never comes from the tail | It is not in the table; only the hub carries it |
| D4 | `content_delta` never reaches the tail — it is deliberately not persisted (one row per token would dwarf the table) | Cross-replica cost: no typewriter effect, spinner → full answer. Accepted, and now explicit |
| D5 | Fixed 500 ms interval, no knob | See R3: the query is an indexed range scan of a few dozen µs; a knob would be a test-matrix tax for no measured benefit |
| D6 | A tail error is a `Warn`; the stream stays open | Degrade to hub-only (same pod) rather than kill the subscription |
| D7 | The tail's key is `(userID, agentID, sessionKey)` — identical to the hub subscription key | Multi-tenant isolation must not depend on which transport delivered the event |
| D8 | `id: <seq>` is emitted only for persisted rows | Keeps `Last-Event-ID` resume meaningful; a live-only event must not become someone's resume point |

## 5. Review of the first draft (what changed when I re-read it)

| # | Draft | Problem | Now |
|---|---|---|---|
| R1 | `emitRecord` skipped only `seq <= sinceSeq` | If `content_delta` is ever persisted (or any new live-only type appears), the tail would double-render on the active tab | A **type guard**: live-only types are dropped on the tail path as well, with the reason in the comment |
| R2 | "Switch polling on when the turn is in flight" | The subscriber **cannot** know that cross-replica — that knowledge lives in pod A's memory. The optimization is precisely useless in the case this change exists for | Fixed interval; backoff recorded here as the escape hatch if DB load ever shows up |
| R3 | "Branch the interval by dialect because SQLite has one connection" | Measured shape: an index range query returning 0 rows is tens of µs; even 50 open tabs is ~100 trivial reads/s. A dialect branch buys nothing and costs a test path | One interval, no branch |
| R4 | Treating "turn ended without a result" as part of this fix | The tail can only replay *"no result"* — it cannot invent one. That is a **UI** state, not a delivery mechanism | Kept in §7 as the immediate follow-up, with its design, not silently bundled |

## 6. Execution plan (files, order, tests)

**Files**

| File | Change |
|---|---|
| `internal/setup/chat_event_writer.go` (new) | `chatEventWriter` — the cursor (`shouldSend`) and the one SSE-frame renderer used by all three sources |
| `internal/setup/chat_event_tail.go` (new) | `chatEventTailInterval`, `liveOnlyEventTypes` / `isLiveOnlyEventType`, `s.tailSessionEvents(...)` |
| `internal/setup/handlers.go` | use the writer for replay + hub + tail; add the tail ticker to the subscribe loop |
| `internal/setup/chat_event_writer_test.go` (new) | unit: one seq written once, stale seq dropped, live-only seq neither id'd nor cursored |
| `internal/setup/chat_event_delivery_e2e_test.go` (new) | e2e: two Servers sharing one store (two "replicas", separate hubs) — the subscriber on B sees A's turn; plus dedupe, degradation, and the delta guard |

**Order (red first, as usual)**

1. `TestChatSubscribeSeesEventsPersistedByAnotherPod` — red: today the subscriber body has no `tool_result` (hub is empty on B, no tail).
2. `TestChatSubscribeDoesNotDoubleDeliverWhatTheHubAlreadySent` — green before and after by construction (it guards the cursor); the rule itself is red-checked by mutating `shouldSend` (`TestChatEventWriterSendsEachSeqOnce` fails), because the duplicate it prevents is a race, not a sequence a black-box test can force.
3. `TestChatSubscribeNeverTailsLiveOnlyEvents` — red before the guard: a `content_delta` row inserted straight into the store reaches the client.
4. `TestChatSubscribeSurvivesATailQueryFailure` — red before `D6`: the stream dies (the loop returns or the handler 500s).

**Sensitivity (mutations that must turn the suites red)**

| Mutation | Expectation |
|---|---|
| `chatEventTailInterval = time.Hour` | (1) red — verified |
| `shouldSend` always true | `TestChatEventWriterSendsEachSeqOnce` red — verified |
| drop the `isLiveOnlyEventType` guard on the tail path | (3) red — verified |
| let a tail error return from the handler | (4) red — verified |

## 7. Boundaries and follow-ups

| Item | Why it is not in this change |
|---|---|
| **Third terminal state in the panel** ("this turn ended without a result") | Delivery cannot fix a result that was never produced (turn killed by OOM, crash, cancel-without-grace). Needs a UI state derived from *persisted* history so it survives reload, and it is the Q4 contract's UI counterpart. **Next change**, cloud repo |
| `content_delta` across replicas | Would need persisting deltas or a real pub/sub; both cost more than the gap (typing feel only) |
| `subagent_progress` volume | Currently one row per iteration per sub-agent. If the table grows, persist only `start`/`done` and keep iterations live-only — decided by measurement, not now |
| Same session written by two replicas (Q6) | Decided-deferred; `seq` dedupe hides duplicates, not semantic interleaving |

## 8. Where the code lives (index)

* `internal/setup/chat_event_tail.go` — the tail query + interval + live-only type guard.
* `internal/setup/chat_event_writer.go` — the cursor and the SSE frame renderer.
* `internal/setup/handlers.go` — `emitEventRecord`, replay, hub and tail paths.
* `internal/setup/chat_event_delivery_e2e_test.go` — cross-replica delivery.
* `internal/agent/event_hub.go` — the same-pod fast path (unchanged).
