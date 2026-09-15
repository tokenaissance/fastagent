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
| **Third terminal state in the panel** ("this turn ended without a result") | **Both sides landed, and now agree** (2026-09-16: webui `ccba1ca`, cloud `28ef00a5`). The signal is the bubble's own flag — exactly "still being written" — so no new state was needed. The server cannot supply this: `normalizeForPrompt` pads an interrupted call only in the *prompt* projection (*"the stored session is left untouched"*). The earlier note here ("the reference webui still says `Executing...`") was already wrong when written: the webui has derived a client-side terminal state since Q4 — `chat-screen.tsx` writes `"(stopped)"` into its own row on abort and again when folding history. What it did *not* have was a truthful rendering of it: the sentinel counted as a result, so the row drew a green check and the header said "Executed N tools", while the cloud said "Interrupted" and spun forever. Both now render the same amber exclamation, the same "Interrupted — X of Y tools returned" summary, and the same sentence in the Output slot. Note for anyone reading the cloud parity audit: its D15 entry ("remove cloud's inferred stopped") rested on the same two stale claims and is superseded | 
| `content_delta` across replicas | Would need persisting deltas or a real pub/sub; both cost more than the gap (typing feel only) |
| `subagent_progress` volume | Currently one row per iteration per sub-agent. If the table grows, persist only `start`/`done` and keep iterations live-only — decided by measurement, not now |
| Same session written by two replicas (Q6) | Decided-deferred; `seq` dedupe hides duplicates, not semantic interleaving |

## 8. Where the code lives (index)

* `internal/setup/chat_event_tail.go` — the tail query + interval + live-only type guard.
* `internal/setup/chat_event_writer.go` — the cursor and the SSE frame renderer.
* `internal/setup/handlers.go` — `emitEventRecord`, replay, hub and tail paths.
* `internal/setup/chat_event_delivery_e2e_test.go` — cross-replica delivery.
* `internal/agent/event_hub.go` — the same-pod fast path (unchanged).

## 9. The other side of the same gap: the webui's catch-up rows (D4, 2026-09-15)

Delivery landing on the wire was only half of it. On the webui, `tool_call` and
`tool_result` had **no case** in the `chat/subscribe` handler: the rows appeared
only after `done` triggered the history reload. So the browser's half of the
cross-replica story was "the event arrives and is dropped" — a watching tab sat
on `Executing…` with no record of what was executing, and a turn that never
wrote a closing message showed nothing at all until a manual refresh.

cloud was the reference (its reducer renders both), so the webui grew the same
two cases, grouped the way its POST path groups them, and `done` now owes a
canonicalising reload whenever this connection painted tool rows — even when no
content bubble was built.

* Implementation: `web/src/components/chat-screen.tsx` — the subscription
  handler's `tool_call` / `tool_result` cases (`resetToolRows` / `paintToolGroup`).
* Gate: `web/src/__tests__/chat-subscribe-tool-rows.test.tsx` — two cases. The
  first proves the connection renders (`content`) before requiring the running
  row and its result (red before the change: the row never appeared); the second
  pins the other half — a tool-only turn still owes the canonicalising history
  read on `done` (red when `|| renderedToolRows` comes out of that condition).
* Contract: cloud `docs/fastagent/design/10-chat-client-parity.md` §2 D4.

This is also where the webui got a test runner at all (vitest + happy-dom +
testing-library, mirroring cloud's stack; `pnpm test`). The four cases in
`web/src/lib/mcp-servers.test.ts` had been dead — `node:test` imports Node's ESM
resolver rejects, and no script pointed at the file — and were ported so the
runner starts with real assertions rather than none.

The runner is wired to CI in `.github/workflows/web-test.yml`: it runs
`pnpm typecheck`, `pnpm lint` and `pnpm test` on every push and pull request,
with the pnpm pin (10.15.0) taken from the Dockerfile so a lockfile only one of
them accepts cannot pass here and break the image build later. That workflow is
the first web gate in this repo — `docker.yml` owns the web *build* but only
fires on `dev`/`main` pushes and tags, so before it a pull request could merge a
broken web tree silently.

Lint joined the gate only after it was green, and it is green in both senses:
the tree carried 25 `react-hooks/set-state-in-effect` errors (all one shape — a
synchronous `setState` in an effect body: a spinner raised for a value it
already had, an error cleared before the answer arrived, a dialog reset while
watching `open`, expansion seeded once) plus 22 warnings, and both counts are
now zero. The gate runs `pnpm lint --max-warnings=0` so a new one cannot land
silently.

Two decisions inside that cleanup are worth knowing, because both chose a
structural answer over a silencing one:

* `next/image` is a no-op in this app (`output: "export"` +
`images.unoptimized: true`), so `@next/next/no-img-element` is off in
`eslint.config.mjs` with that premise written down, and the sixteen now-dead
inline disables went with it — one policy instead of sixteen private ones.
* The subscription effect reads its steer handler through a ref rather than
listing it: the declaration is 250 lines below (listing it is a
temporal-dead-zone error) and the connection's identity must not depend on a
handler anyway.

The same lint also runs **before the commit exists**, from `.githooks/pre-commit`
(install with `make hooks`). It is the narrow half of the gate: staged files
under `web/` that ESLint handles, `--max-warnings=0` so the bar matches CI, no
autofix, and a warning (not a block) when `web/node_modules` is absent. A commit
that never touches `web/` costs nothing — the workflow above is what catches
everything a local hook can be talked out of with `--no-verify`.

The cleanup itself — the four shapes those 25 errors had, the one approved
semantic change, and the two visible behaviour deltas (file-tree expansion, the
theme store) — is written up in [webui-lint-cleanup.md](./webui-lint-cleanup.md).
