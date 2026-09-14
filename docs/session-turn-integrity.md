# Session turn integrity: one writer per session, non-pollutable history

> **Status**: P0–P6 landed, plus Q4 (no persisted synthetic replies). P0–P3 are
> deployed to dev + prod (`20260914015438-deploy-54b07f7`); P4, P5 (grace +
> per-source budgets), P6, Q4 and the timing-margin test fix landed after that
> deploy. Q6 (cross-replica session lease) is **decided-deferred**, with its
> reopen triggers written down in
> [Deferred: cross-replica session lease](#deferred-cross-replica-session-lease-q6).
> **Progress**: P0 wire dedupe + pad scoping + compaction ctx + production data
> repair · P1 session turn gate (`Session.AcquireTurn/ReleaseTurn`, wired into
> `HandleMessage` and `HandleMessageStream`) · P1b `queued` event +
> Codex-style queue block with Edit/Cancel and a withdraw endpoint ·
> P2 `TurnMode`/`RunTurn` + gateway parking of
> automatic turns · P3 `normalizeForPrompt` applied to the prompt in both
> loops · Q4 the loop persists no synthetic "interrupted" reply (the projection
> writes it at prompt-build time) · P5 tool grace · P6 NUL-safe archive +
> `fastagent doctor sessions`.
> Test names live in [Implementation plan](#implementation-plan). Convention:
> every `Test…` name cited in this document exists in the tree as
> `func Test…` **unless it is marked `(planned)`** — those are the
> design-first cases named but not written yet, and they are collected in
> [Integration / e2e](#integration--e2e). Before closing a phase, grep the
> names it claims rather than trusting this page:
> `comm -23 <(grep -oh 'Test[A-Z][A-Za-z0-9_]*' docs/*.md | sort -u) <(grep -rho 'func Test[A-Za-z0-9_]*' --include='*_test.go' . | sed 's/func //' | sort -u)`
> — every name it prints must carry `(planned)`, `(not in the tree)` or
> `（已移除）` at the citation (same table row or bullet).
> **Scope**: how a turn is admitted for a session, and how that session's
> history stays structurally valid for every provider.
> **Storage**: `sessions.messages` (working set the agent loop reads) plus
> `session_messages` (append-only archive the UI reads), Postgres in prod.
> **Last updated**: 2026-09-14
> **Decision owner**: mengmengmengqiang@gmail.com
> **Reviewed by**: pending review (this document)
> **Incident**: 2026-09-13 production, agent `agt_cda27bbfbf4a84e2dfa6`,
> session `hJKMWwtOp3mJOtqN8Uz2mW` (see [Appendix A](#appendix-a--incident-evidence)).
> **Reference design**: Codex (`/Users/reina/Project/tokenaissance/codex`,
> `openai/codex`), cited inline.

## Problem

Production surfaced a provider 400 that made one chat session permanently
unusable:

```text
API error 400: {"error":{"message":"Messages with role 'tool' must be a
response to a preceding message with 'tool_calls'", "type":"invalid_request_error"}}
```

It repeated on **every** turn that session ran on a DeepSeek
(OpenAI-compatible) model — 40 error lines across 10 failed turns (two
retries + the terminal failure + the turn error, four lines per turn)
between 11:35Z and 13:45Z on 2026-09-13, one turn per cron tick — while the
same session ran normally whenever the agent was switched to Claude. The
session history had been written into a shape the OpenAI-compatible
validator rejects: one assistant declaring two tool calls followed by
**four** tool replies (the synthetic "stopped" pad for each call, then the
real result for each call).

Two things made a single bad write permanent:

1. `sessions.messages` **is** the prompt. There is no derived, normalized
   projection, so any structural damage is replayed verbatim on every later
   turn.
2. Two turns can write the same session concurrently, and one of the writers
   (`padOrphanToolResults`, removed in Q4) reasoned about the session globally
   ("the last assistant with tool calls") rather than about its own turn.

## Root cause

### Layer 1 — two writers, no admission

A turn can be started from several entry points, and only some of them
serialize against each other:

| Entry point | Path | Serialized by |
|---|---|---|
| IM / cron / goal / heartbeat / subagent delivery | bus → `processInbound` → `taskQueue.Submit` → `ag.HandleMessage` (`internal/gateway/gateway.go:521`) | `taskqueue.Queue`, per `chatKey(channel, accountID, chatID)` (`internal/taskqueue/queue.go:94`) |
| Dashboard chat POST (streaming) | `handleChatStream` → goroutine → `HandleWebChatStream` → `HandleMessage` (`internal/setup/handlers.go:1254`) | **nothing** |
| Dashboard chat POST (non-streaming), webhook, API-key `/v1/chat/completions` | `HandleWebChat` / `HandleMessage` / `HandleMessageStream` | **nothing** |

`HandleWebChatStream` and the queue worker both end up in
`Agent.HandleMessage` (`internal/agent/loop.go:2239`) or `HandleMessageStream`
(`:3092`), and neither path takes a per-session lock. Two turns on one
session can therefore interleave their appends.

### Layer 2 — the pad is a global scan, executed by a *different* turn

`padOrphanToolResults` (`internal/agent/loop.go:2937`) runs from each turn's
`defer` and, before P0, looked for "the last assistant message carrying
tool calls in the whole session" and padded every unresolved id it found
there. In the incident the two overlapping turns were:

* **A** — the cron-fired task `task-1789299000292-1`, started 11:30:00.292,
  killed by the 300 s task-queue timeout at 11:35:00.897
  (`duration_ms=300604`);
* **B** — a dashboard turn started 11:34:32.776.

Because A's tool round was still in flight (`exec` inside the sandbox,
`list_cron_jobs`), B's defer padded **A's** tool_use ids:

```text
11:35:00.439 WARN msg="padding orphan tool_use with stopped result"
             toolCallID=call_00_38dFV2Ml48bAd3FgPCS13513 tool=list_cron_jobs
11:35:00.567 WARN msg="padding orphan tool_use with stopped result"
             toolCallID=call_01_3nApOZmdQE6lQFv22vhH6960 tool=exec
```

A's real results then landed after those pads, leaving two replies for each
`tool_call_id`.

### Layer 3 — the wire sanitizer modelled two shapes, not three

`findOrphanToolCalls` (`internal/provider/openai.go:148`) only knew:

1. an assistant whose declared `tool_calls` are not answered by the
   immediately following run of tool messages (strip the call), and
2. a tool message whose id no earlier assistant declared (drop the reply).

A duplicate answer violates neither: the id *is* answered in the immediate
run, and it *was* declared earlier. So the request shipped a second answer
to an already-closed call. DeepSeek rejects that; the Anthropic conversion
path tolerates it (`internal/provider/anthropic.go:61` coalesces tool
results per assistant), which is why switching models made it look like a
provider or cron problem rather than history corruption.

### Amplifiers

* **300 s turn budget.** `taskTimeoutSec` defaults to 300
  (`internal/gateway/gateway.go:418`) and kills a turn mid-tool — the exact
  condition that produced a pad. Long sandbox work (`setsid nohup …`,
  multi-minute `exec`) exceeds it routinely.
* **Compaction that never compressed.** The summarizer was called with a nil
  context (`internal/agent/compaction.go:190`, now fixed), so every
  compaction fell back to pruning-only and the session stayed at ~130 k
  tokens, re-running compaction on every tick and spending its budget before
  the real work.
* **Archive writes that fail silently for NUL-bearing tool output.**
  `session archive append error: ERROR: invalid byte sequence for encoding
  "UTF8": 0x00 (SQLSTATE 22021)` appears next to the incident: the real
  reply never reached `session_messages`, so the archive and the working set
  disagree (see P6).

### Why "session pollution" is the right name

We use the term for a persisted, propagating, easy-to-miss violation of the
history's structural contract:

| Property | Why it matters here |
|---|---|
| **Persisted** | the bad shape is written to `sessions.messages`, not a transient buffer |
| **Propagating** | every later turn sends the same history, so one bad write breaks all future turns on that session |
| **Hidden** | providers disagree about tolerance (Anthropic ok, DeepSeek 400), so the symptom looks provider-specific |

Note the distinction the design has to preserve: an *incomplete* history
(a tool call whose result never arrived because the turn was interrupted) is
**truth, not pollution**. Codex persists exactly that and repairs it at
prompt-build time. Pollution is when the *derived* prompt is structurally
invalid, or when two turns' intents are interleaved in one history.

## Reference design (Codex)

Codex separates the two concerns we currently conflate: *who may write a
turn* and *what the model is allowed to see*.

**1. One active turn per thread, with explicit admission semantics.**
`session.active_turn` is a single `Option<ActiveTurn>` slot; a turn is
created through `active_turn.get_or_insert_with(ActiveTurn::default)`
(`codex-rs/core/src/tasks/mod.rs`, around the `run_turn` path). Every
producer must pick a submission mode
(`codex-rs/protocol/src/turn_input.rs:133`):

```rust
enum TurnInputMode {
    StartOrSteer,                      // idle → start; busy → steer the running turn
    StartIfIdle,                       // idle → start; busy → NotSubmitted{NotIdle}
    Steer { expected_turn_id: String },// steer only that exact turn
}
```

and must handle the refusal
(`NotSubmittedReason`, same file, `:217`): `NotIdle`, `NoActiveTurn`,
`ExpectedTurnMismatch`, `ActiveTurnNotSteerable{Review|Compact}`, `PlanMode`,
`EmptyInput`, `ActiveTurnOutputSchemaMismatch`. `session/turn_input.rs` is
documented as *"the one place Core decides whether submitted input starts a
turn, steers an active turn, or is rejected"*; `start_if_idle` (`:327`)
returns `NotSubmitted{NotIdle}` instead of starting a second turn, and
queued work is picked up at an idle boundary
(`maybe_start_turn_for_pending_work`). Automatic sources (scheduled work,
mailbox/`trigger_turn` deliveries, memory writebacks) use `StartIfIdle`;
user input uses `StartOrSteer`; an active `Review`/`Compact` turn is
explicitly not steerable.

**2. History invariants are enforced on a derived prompt, not on the log.**
`History::normalize_history`
(`codex-rs/core/src/context_manager/history.rs:466`) states them:

> 1. every call (function/custom) has a corresponding output entry
> 2. every output has a corresponding call entry or names an external tool event
> 3. unsupported image and audio content is stripped

It runs inside `for_prompt()` (`history.rs:218`), i.e. the persisted rollout
may contain an unfinished call, and the prompt never does.

**3. Repair is deterministic and idempotent.**
`ensure_call_outputs_present`
(`codex-rs/core/src/context_manager/normalize.rs:21`) inserts a synthetic
`FunctionCallOutput` **immediately after** the call (`items.insert(idx + 1,
…)`, applied in reverse index order) and only when that `call_id` has no
output **anywhere** in the list — so it cannot double-answer, and it cannot
duplicate on a second pass. Its id is derived from the call id
(`uuidv5(namespace, "fco:<call id>")`) with an explicit comment that
changing the namespace would change model-visible ids and invalidate prompt
caches. `remove_orphan_outputs` drops outputs with no call. Tests:
`context_manager/history_tests.rs:1664+`
(`normalize_adds_missing_output_for_function_call`,
`normalize_removes_orphan_function_call_output`, …).

**4. Removal is pair-aware.** `History::remove_first_item()`
(`history.rs:293`) deletes the counterpart of the item it drops
(`normalize::remove_corresponding_for`), so truncation cannot split a pair
— and compaction, which replaces the item list wholesale, is re-normalized
at the next prompt build rather than needing its own pair logic.

## Decision

| # | Decision | Rationale |
|---|---|---|
| **D1** | One turn at a time per **session** (not per chat key). Every turn-start entry point passes through one admission gate owned by the session. | The session is the unit that has history and memory; `shared_identity` channels and URL-token recovery already map multiple `(channel, accountID, chatID)` triples onto one session, so a chat-key lock is not sufficient. |
| **D2** | Default policy for a turn-start request that finds a turn in flight is **queue and run after it** — not steer, not run concurrently. | Chosen 2026-09-14: the dashboard keeps steering as a deliberate user action. Deterministic, and it is what makes a single writer possible without changing what the model sees mid-turn. |
| **D3** | Steering stays explicit: the web UI keeps its `/api/chat/steer` button; inbound IM messages keep today's best-effort auto-steer (`trySteer`, `internal/gateway/routing.go:258`). Both fold into the running turn instead of starting one. | Steering is not the defect — concurrent *turns* are. IM responsiveness is a product property we are not changing in this doc (see Q1). |
| **D4** | The prompt is a **derived, normalized projection** of history. A pure `normalizeForPrompt` becomes the authoritative guard that every provider sees valid calls/replies. | Provider-specific wire sanitizers are a second line of defence, not the contract; Anthropic already needed an extra sweep the OpenAI path did not have. |
| **D5** | Persisted history stays truthful. We do not rewrite the model's own record except to repair pollution (as in Appendix B); structural fixes are applied on the way *out*. | Keeps audits, prompt-cache stability and the UI's "what actually happened" intact. |
| **D6** | Truncation/compaction never splits a pair. | `safeCompactionCutoff` today only handles the tail starting with a tool message; normalisation makes the whole class unrepresentable. |

### Non-goals

* Not a rewrite of the task queue or of session storage.
* No change to what an IM user sees while a turn is running (typing indicator
  stays; steer behaviour unchanged).
* No cross-session or cross-agent coordination: the invariant is per session.
  Two agents, or two sessions of one agent, may still run in parallel.

## Invariant

Written as four separately falsifiable clauses, each with the mechanism that
enforces it and the test that would catch a violation.

| # | Clause | Violated when | Enforced by | Guarding test |
|---|---|---|---|---|
| **W** · single writer | At most one turn is executing against a session's history at any instant | two `HandleMessage` bodies are past admission for one session | session turn gate (P1, landed): FIFO waiter queue owned by the session | `TestAcquireTurnSerializesCallers`, `TestAcquireTurnHandsOffFIFO`, `TestAcquireTurnContextCancelDoesNotLeakSlot`, `TestHandleMessageWaitsForInFlightTurn`, `TestHandleMessageSerializesQueuedTurns`, `TestQueuedTurnRunsAfterLongTool`, `TestCronTickDoesNotInterleaveWithWebTurn`, `TestConcurrentWebAndCronTurnSerialize`, `TestGoalContinuationDoesNotDeadlock` (each of the last four was verified to fail with the gate neutered) |
| **P** · pair integrity | For the model, every tool call has exactly one reply, and every reply belongs to a call | a request ships N replies for a call id, an unanswered call, or an orphan reply | `normalizeForPrompt` (P3, landed) + wire builder (`internal/provider/openai.go:148`, P0) | `TestNormalizeForPromptShapes` (7 shapes + idempotence + no mutation), `TestNormalizeForPromptReadsRawAssistantCalls`, `TestNormalizeForPromptStripsDuplicateCallDeclaration`, `TestToAPIMessagesDropsDuplicateToolReplies`, `TestToAPIMessagesDropsDanglingToolReplies` |
| **O** · ordering | A turn's own messages append in order and are never interleaved with another turn's | a user message or tool reply from turn B lands between turn A's call and its reply | clause W (there is no other writer) | `TestHandleMessageSerializesQueuedTurns` (asserts the exact role sequence `user,assistant,user,assistant` and the user-message order, not just counts), `TestQueuedTurnRunsAfterLongTool`, `TestCronTickDoesNotInterleaveWithWebTurn`, `TestConcurrentWebAndCronTurnSerialize` (the cross-source pair, through the real chat handler) |
| **T** · truthful pad | Stored history carries **no** synthetic "interrupted" reply: an interrupted turn leaves its call open, and the projection answers each open call with exactly one synthetic reply at field-build time | a synthetic reply is persisted at all, an open call gets two of them in the projection, or a reply is emitted for a call that is already answered | Q4 (pad path removed) + `normalizeForPrompt` (P3) | `TestInterruptedTurnLeavesNoSyntheticReplyInHistory` (loop detection breaks out mid-tool: history has the open call, no pad; the projection is doctor-clean), `TestNormalizeForPromptShapes` (unanswered call indexed in place, duplicate collapses to one, idempotent), `TestTurnBudgetExpiryLetsInFlightToolRecordItsResult` |

Accepted windows / known gaps (to keep the table honest):

* W is per **process**. Two gateway replicas serving the same session can
  still both admit a turn (the sandbox pool solved the same problem with a
  Postgres lease). Accepted deliberately rather than solved: decided
  2026-09-14, with the evidence that would reopen it written down in
  [Deferred: cross-replica session lease](#deferred-cross-replica-session-lease-q6).
* P is guaranteed for OpenAI-compatible and Anthropic wire builds; other
  providers inherit `normalizeForPrompt` because it runs before the provider
  split.
* A synthetic reply exists only in the projection. Stored history therefore
  shows an *unanswered call* for every interrupted turn, which the doctor
  scanner reports as expected and does **not** gate on (Q4, decided
  2026-09-14 — Codex parity). Sessions written before Q4 keep their pads until
  the next `doctor sessions --fix`; the projection collapses those duplicates
  at request time either way.

## Design

### P1 — Session turn gate (single writer) ✅ landed

Owned by `session.Session` because the session already owns its history,
steer buffer and turn depth, and because `Manager.Get` is the single place
every entry point resolves a session through.

Landed API (`internal/session/manager.go`): `AcquireTurn(ctx) bool`,
`ReleaseTurn()`, plus `TurnActive()` / `TurnWaiters()` for logs and tests.
Handoff keeps `turnActive` set and closes the waiter's channel, so the slot is
never momentarily free and a fresh caller cannot jump the queue; `ReleaseTurn`
without the slot is a no-op rather than a way in.

```go
// AcquireTurn blocks until this caller holds the session's single turn slot,
// or ctx ends. Callers MUST run exactly one turn between AcquireTurn and
// ReleaseTurn, and MUST NOT start another turn for the same session while
// holding it.
func (s *Session) AcquireTurn(ctx context.Context) bool

// ReleaseTurn frees the slot and hands it to the longest-waiting caller.
func (s *Session) ReleaseTurn()
```

* FIFO waiter queue (`[]chan struct{}`), so queueing is fair and the order of
  user messages is preserved.
* `ctx` cancellation while waiting removes the waiter and does **not** leak
  the slot (including the race where the slot is handed over at the same
  moment the ctx ends).
* Acquisition points: `HandleMessage` (after the slash-command and quota
  gates, before the plan-mode branch so plan mode is covered too) and
  `HandleMessageStream`. Everything else — `HandleWebChat`,
  `HandleWebChatStream`, webhook, API — reaches those two.
* `defer sess.ReleaseTurn()` sits **outermost** so it runs after the existing
  defers (`flushLeftoverSteer`) — a parked steer is part of the turn that
  owned the session, not of the next one.
* Steering is unaffected: `PushSteerIfActive` keeps using the existing
  in-flight window; the gate and the steer window are, by construction,
  held by the same turn.
* Re-entrancy is forbidden: a turn must never call back into
  `HandleMessage`/`HandleMessageStream` for the same session synchronously.
  Goal continuations satisfy this by construction — `goal.TryFireContinuation`
  publishes onto the bus (`internal/agent/goal/continue.go:43,54`) instead of
  calling back into the agent, so the follow-up turn runs on the gateway's
  goroutine and simply waits for the slot. The explicit no-deadlock e2e test
  is still on the [Integration / e2e](#integration--e2e) list.

### P1b — Queued-state UX (dashboard), modelled on Codex ✅ landed

Codex renders pending input in a dedicated `PendingInputPreview` widget above
its composer (`codex-rs/tui/src/bottom_pane/pending_input_preview.rs`), not in
the transcript:

```text
• Queued follow-up inputs
  ↳ Hello, world!
  ↳ This is another message
    ⌥ + ↑ edit last queued message
```

(snapshot `render_two_messages`; sections above it read *"Messages to be
submitted after next tool call (press Esc to interrupt and send immediately)"*
and *"Messages to be submitted at end of turn"* — Codex keeps steers and
plain queued follow-ups visually separate, dim/italic per message, 3 lines
max, FIFO, and submits exactly one queued message when the turn goes idle.)

Mapping to fastagent:

| Codex | fastagent dashboard |
|---|---|
| queued message never shown as a turn until it starts | unchanged: the optimistic bubble stays where it is; the queue block lives above the composer (the transcript keeps only the turn that is producing output) |
| `• Queued follow-up inputs` header, `↳ text`, dim + italic, 3-line cap | same header/arrow/italics; the text comes from the `queued` event and is truncated to one line by the composer's width |
| `⌥ + ↑ edit last queued message` | **Edit queued message** link — withdraws the queued turn server-side and restores the text into the composer |
| interrupt / queued-message removal | **Cancel** link — withdraws the queued turn; nothing is written to the session |
| one queued message submitted at a time, FIFO | the session turn gate (P1) + `(n ahead)` in the header |
| submitted when the turn goes idle | automatic: the waiting POST acquires the slot the moment the current turn releases it |

Backend surface for the two actions: `POST /api/chat/cancel` with
`{agentId, sessionId, turnId}` → `200 {"canceled":true}` while the turn is
still queued, `409 {"reason":"already_started"}` once it holds the slot (the
client then falls back to plain Stop semantics — detach this stream, the
server keeps the turn), `404 {"reason":"not_queued"}` when nothing is
registered for that turn id. The "started" bit comes from
`agent.WithAdmissionSignal` (closed by the agent right after it acquires the
session's turn slot), not from the session event hub — the hub is
session-scoped and also carries *other* turns' events (cron ticks stream into
the same chat panel).

Not implemented yet: pausing auto-send after an interrupt the way Codex does
(`suppress_queue_autosend`). We have no "interrupt and keep queued" state —
Cancel removes the queued turn outright.

### P2 — Admission result and per-source policy ✅ landed

P1 blocks; P2 makes the refusal explicit so an automatic producer never holds
a queue worker (and its 300 s budget) behind a user turn.

```go
// internal/agent/admission.go
type TurnMode int
const (
    TurnStartOrQueue TurnMode = iota // user-facing: wait for the slot
    TurnStartIfIdle                  // automatic: refuse instead of waiting
)
var ErrTurnNotAdmitted = errors.New("turn not admitted: session is busy")

// RunTurn is the admission-aware entry point; HandleMessage stays the
// waiting behaviour every existing caller already had.
func (a *Agent) RunTurn(ctx context.Context, msg bus.InboundMessage) (string, error)
```

`turnModeForSource` maps `bus.SourceCron`, `bus.SourceGoalContext`,
`bus.SourceHeartbeat` and `bus.SourceSubAgent` to `TurnStartIfIdle`; everything
else (including the empty source = a real user turn) is `TurnStartOrQueue`.

The gateway consumes the refusal: the task handler calls `RunTurn`, and on
`ErrTurnNotAdmitted` it **parks** the message
(`internal/gateway/deferred_turns.go`) and returns the queue slot immediately.
A drain loop re-submits a parked message once its session looks idle
(`Gateway.sessionBusy`), keeping per-chat FIFO and dropping anything that has
waited longer than five minutes with a warning. The agent re-checks on every
attempt, so a race just parks the message again rather than running it into a
busy session.

Policy matrix as landed:

| Source | Mode | Busy behaviour |
|---|---|---|
| dashboard POST (streaming / non-streaming) | `StartOrQueue` | queue (D2) |
| `/api/chat/steer` | `Steer` | steer the running turn (unchanged) |
| IM DM / group inbound | `StartOrSteer` | steer (unchanged, Q1) |
| cron tick | `TurnStartIfIdle` | `ErrTurnNotAdmitted` → parked, retried every second for up to 5 min |
| goal continuation / heartbeat / subagent | `TurnStartIfIdle` | same |
| webhook / API-key completion | `StartOrQueue` | queue |
| plan mode | `StartIfIdle` on a session with an active turn | queue, never preempt |

### Product contract: who queues and who steers (decided 2026-09-14)

The table above is a *product* decision, not an implementation detail, so it is
stated as one here. Users should be able to predict what happens when they
write into a session that is already working:

| Surface | While the session is busy | What the user should expect |
|---|---|---|
| Dashboard chat | **the message queues** behind the running turn; it runs when that turn ends | your message is kept, in order, and answered in its own turn — the running turn is not redirected |
| Dashboard "steer" (explicit action) | folds into the running turn | the mid-flight instruction reaches the model at its next tool boundary; the current turn answers it |
| IM DM / group message | **steers** the running turn | the bot reacts to your newest message inside the work it is already doing (the IM convention: latest message wins) |
| cron tick / goal continuation / heartbeat / subagent | never starts a second turn; parked and retried when the session is idle | scheduled work is deferred, not run in parallel with a human's turn |
| webhook / API-key completion | queues like the dashboard | one turn per session, always |

Rationale for the asymmetry: dashboard users can *see* the running turn and
have an explicit control (the steer button), so queueing is the predictable
default (D2); IM users cannot see it and expect a bot to react to their newest
message, so steering stays automatic there (D3). Steering is not what caused
the 2026-09-13 incident — steering writes into the running turn's own message
list, it does not create a second writer — which is why this contract keeps it
where it is useful.

Changing either half is a product change, not a bug fix: making IM queue would
mean routing IM messages through `submitTask` instead of `trySteer`
(`internal/gateway/routing.go`); making the dashboard auto-steer would mean
calling `PushSteerIfActive` in `handleChatStream` before starting a turn.

### P3 — `normalizeForPrompt` (the authoritative guard) ✅ landed

A pure function applied where the prompt is assembled
(`internal/agent/loop.go:2404` and `:3164`, after compaction, before the
system messages are prepended):

```go
// normalizeForPrompt returns a prompt-safe copy of msgs:
//   - every tool call has exactly one reply, inserted immediately after it
//     when missing (synthetic provider.StoppedToolResult reply — the only
//     synthetic reply left in the system, and it is never persisted)
//   - replies whose call id is unknown are dropped
//   - second and later replies for one call id are dropped (first wins)
//   - a call re-declared after it was answered loses the later declaration
//   - input is never mutated
func normalizeForPrompt(msgs []provider.Message) []provider.Message
```

Rules taken from Codex and adapted:

| Rule | Note |
|---|---|
| insert synthetic reply at `call index + 1`, not at the end | keeps adjacency even when another message was appended after the call |
| only when the id has no reply anywhere in the slice | cannot double-answer |
| first reply wins when an id is answered twice (the one adjacent to the call) | same choice the wire builder makes, so the two layers agree |
| a reply is identified by its `tool_call_id` (= the call's id), so the synthetic reply is stable by construction | repeated runs produce byte-identical prompts (prompt cache); nothing extra to derive |
| a call re-declared after it was answered loses the later declaration (`RawAssistant` cleared so serialisers cannot re-introduce it) | a duplicated assistant append cannot create a second unanswered call |
| drop unknown-id and duplicate replies | the third shape the current sanitizer misses |
| never mutate the input slice | the session keeps the truthful record (D5) |

### Q4 — the synthetic reply is prompt-only ✅ landed

The pad was answering two needs: a well-formed prompt (every call has a
reply) and a terminal UI ("interrupted, not still running" instead of a
forever-spinning tool). P3 took over the first — the projection inserts the
reply at request time — and the chat UI already derives the second from the
call itself (`web/src/components/chat-screen.tsx`, the "any tool_use that
still has no result … mark them stopped" sweep on history rebuild and on
abort). That left only the collision risk: a persisted pad is a second write
on a call a *late real result* can still answer, which is the incident.

Landed: `padOrphanToolResults` and its two defer call sites are gone, along
with the per-turn `turnToolCallIDs` bookkeeping and its test file. An
interrupted turn now ends with the call **open**:

* the session keeps the truth (the tool never returned);
* the next request is valid because `normalizeForPrompt` fills exactly one
  reply per open call, in place, idempotently;
* `provider.StoppedToolResult` remains the shared literal, but its only
  producer is the projection;
* the doctor scanner classifies an unanswered call as *expected* — reported,
  never gated on (`doctor.Finding.Expected`, `doctor.Unexpected`), because it
  is now the normal shape of an interrupted turn rather than history debt.

Guarding tests: `TestInterruptedTurnLeavesNoSyntheticReplyInHistory` (drives
the loop detector into breaking out mid-round on the streaming path, then
asserts no pad in history, a genuinely open call, and a doctor-clean
projection), `TestNormalizeForPromptShapes`, `TestExpectedCoversOpenCallsOnly`,
`TestDoctorSessionsTreatsOpenCallAsExpected`.

### P4 — Truncation, compaction and stable ids

* Any compaction/truncation result is re-normalized before send (P3 makes
  this automatic), so `safeCompactionCutoff`'s special case becomes an
  optimisation rather than correctness.
* Any future "drop oldest item" path must drop the counterpart with it
  (Codex `remove_first_item` → `remove_corresponding_for`).
* Synthetic ids are derived, not random no-increment: `synthetic:<call id>`
  hashed into a stable token so two normalizations of the same history are
  identical, and so the API never sees a synthetic id that collides with a
  real one.

### P5 — Turn budgets and interruption semantics ✅ landed

One number (`taskTimeoutSec`, default 300 s) used to delimit an IM turn *and*
hard-kill any tool execution in flight, which is what manufactured pads. The
split that landed:

* **Grace before the pad**: on budget expiry the turn stops being fed (its own
  ctx is cancelled, so no further model round starts) while an in-flight tool
  keeps running for `toolGraceDefault` (60 s) and lands its real result. The
  call is left open when the grace also expires — the projection answers it
  with one synthetic reply at request time (Q4), and nothing is written to
  history that a late result could collide with.
* **Per-source budget** via the system `taskqueue` namespace
  (`TaskQueueCfg`): `maxConcurrent` (global), `taskTimeoutSec` (every queued
  turn — IM, cron, goal, webhook), `cronTimeoutSec` (cron ticks only; 0 = same
  as `taskTimeoutSec`). Web turns never reach the queue: the dashboard handler
  carries its own 45-minute budget.
* **Hot reload**: saving that namespace (`POST /api/config` →
  `{"taskQueue":{…}}`, system scope) re-reads it into the running queue —
  `handleUpdateConfig` → `reloadSystemTaskQueue()` → `Gateway.ReloadTaskQueue()`
  → `Queue.SetMaxConcurrent` / `SetDefaultTimeout` + the cron budget. No pod
  roll needed. Semantics: a new default applies to the next task (in-flight
  tasks keep the budget they started with), and a resize cannot strand a slot
  because each task releases into the semaphore it acquired from.
* **One policy site**: `Gateway.taskTimeoutFor` decides which budget an inbound
  gets, and every routing path queues through `Gateway.submitTask` (including
  the deferred-turn drain, so a parked tick keeps its budget when admitted).

### P6 — Archive integrity and operations ✅ landed

* NUL bytes are stripped at the persistence boundary (`sanitizeNUL` on
  `AppendSessionMessage` and `AppendSessionEvent`), so a tool result that
  carries `\x00` — sandbox exec frames its stream with four of them — can no
  longer vanish from the archive while the JSON-escaped working set keeps it.
* The incident's manual analysis is a command now:

```bash
fastagent doctor sessions                      # whole deployment, exits 1 on findings
fastagent doctor sessions --agent <id> --json  # narrow + machine-readable
fastagent doctor sessions --session-key <key> --fix   # withdraw duplicates, backing the row up
```

It reads `sessions.messages` through `ListSessionSnapshots`, reports the three
pairing shapes (`duplicate_tool_reply`, `orphan_tool_reply`,
`unanswered_tool_call`), and with `--fix` removes duplicate replies — the same
repair applied by hand during the incident — after writing
`<backup-dir>/<sessionKey>.json`. Orphan and unanswered findings are reported
only: the prompt projection (`normalizeForPrompt`) handles those without
rewriting history.

## Alternatives considered

* **Route every entry point through `taskqueue.Queue` (per chat key).**
  Rejected as the primary mechanism: the queue keys on
  `channel:accountID:chatID`, and the session — the thing that owns history —
  can be shared across channels (`shared_identity`) or reached through
  several URL tokens (`recoverWebTriple`). Keying on the chat tuple would
  leave exactly the hole we are closing. It also forces one timeout for all
  sources (P5) and gives no place to express "queued" vs "rejected" (P2).
  The queue stays as the IM/cron transport; the gate becomes the authority
  everyone passes.
* **Auto-steer everything while busy (Codex's `StartOrSteer` for user input).**
  Rejected by decision D2 for the dashboard: a mid-turn user message changing
  the running turn's direction is a product choice, and the UI already offers
  it explicitly. Not rejected for IM (Q1).
* **Advisory lock in the store (Postgres) instead of in-process.**
  Deferred by decision (2026-09-14): it is the only way to make W hold across
  gateway replicas, but it adds a round-trip to every turn and couples the
  agent loop to the store for a property that has only ever been violated
  inside one process. Q6 records the reopen triggers and the fix shape
  ([Deferred: cross-replica session lease](#deferred-cross-replica-session-lease-q6));
  the sandbox-lease design is the precedent to copy when that trigger fires.
* **Make the provider layer (wire sanitizer) the only defence.**
  Rejected: it is per-provider (Anthropic needed its own sweep), it runs after
  compaction and truncation have already shaped the prompt, and it silently
  rewrites the request without telling anyone — the incident survived a
  release precisely because the OpenAI builder looked "defensive enough".
* **Rewrite history on every anomaly (self-healing store).**
  Rejected: destroys the audit trail and prompt cache stability, and hides
  the concurrency defect instead of removing it.

## Implementation plan

Each phase is independently shippable and test-first. P0 is already in the
working tree.

| Phase | Change | Files | Tests first |
|---|---|---|---|
| **P0** ✅ | Drop duplicate tool replies at wire build; scope pads to the turn's own ids; thread a real ctx into the compaction summarizer; repair the incident session's stored history | `internal/provider/openai.go`, `internal/provider/provider.go`, `internal/agent/loop.go`, `internal/agent/compaction.go`, `internal/agent/slash.go` | `openai_dangling_tool_test.go` (+2), `pad_orphan_tool_test.go` (new, +2), `compaction_test.go` (+1) |
| **P0.5** | Build and deploy P0 (`./build-image.sh dev` → verify → `prod`). Prod still runs `6345e2b`, i.e. the old pad path. | — | Appendix C checklist |
| **P1** ✅ | `Session.AcquireTurn/ReleaseTurn` (FIFO, ctx-aware, no leak); acquired in `HandleMessage` + `HandleMessageStream` before the plan-mode branch, released outermost so the leftover-steer writer stays inside the turn (the pad writer it originally also covered is gone — Q4); admission waits >1 s logged | `internal/session/manager.go`, `internal/agent/loop.go` | `internal/session/turn_gate_test.go` (serialize, FIFO, cancel-while-queued, cancel-at-handoff race), `internal/agent/turn_gate_test.go` (waits for in-flight turn; queued turns serialized, roles `user,assistant,user,assistant`), plus the four e2e-level cases in [Integration / e2e](#integration--e2e): long tool, cron-vs-web, real POST vs cron tick, goal continuation |
| **P1b** ✅ | Queued-state UX modelled on Codex's `PendingInputPreview`: `queued` event, queue block above the composer with `↳ text`, `(n ahead)`, and Edit/Cancel actions backed by a new withdraw endpoint (`/api/chat/cancel`, `agent.WithAdmissionSignal` marks the point of no return) | `internal/agent/loop.go`, `internal/agent/admission_signal.go` (new), `internal/setup/handlers.go`, `internal/setup/handlers_chat_cancel.go` (new), `internal/setup/server.go`, `web/src/components/chat-screen.tsx`, `web/src/lib/api.ts` | `TestRunTurnQueuesUserSourceAndEmitsQueuedEvent`, `TestWithAdmissionSignalClosesWhenTurnStarts`, `TestPendingTurnRegistryWithdrawContract`, `TestPendingTurnKeyIsolatesTabsAndSessions`, `TestQueuedChatTurnIsAnnouncedAndWithdrawableE2E`, `TestStartedChatTurnCannotBeWithdrawnE2E`; `tsc --noEmit` clean |
| **P2** ✅ | `TurnMode` + `ErrTurnNotAdmitted` + `RunTurn`; gateway parks refused automatic turns and retries them at the next idle point instead of blocking a queue worker | `internal/agent/admission.go` (new), `internal/gateway/deferred_turns.go` (new), `internal/gateway/gateway.go` | `admission_test.go` (refusal, queued event + position, source policy), `deferred_turns_test.go` (FIFO drain, busy skip, budget expiry) |
| **P3** ✅ | `normalizeForPrompt` applied to the prompt in both loops; `provider.Message.EffectiveToolCalls()` added so a call declared only inside `RawAssistant` is still recognised, and the OpenAI wire scanner reuses it instead of parsing raw a second time | `internal/agent/normalize.go` (new), `internal/agent/loop.go`, `internal/provider/provider.go`, `internal/provider/openai.go` | `internal/agent/normalize_test.go` (7 shapes incl. the incident's legacy duplicate, raw-assistant declaration, duplicate declaration; each case also asserts idempotence and input immutability) |
| **Q4** ✅ | The loop stops persisting synthetic "interrupted" replies: `padOrphanToolResults`, its defer sites and the per-turn `turnToolCallIDs` bookkeeping are deleted, so an interrupted turn leaves its call open and `normalizeForPrompt` answers it at request time. The doctor scanner keeps reporting an unanswered call but stops gating on it (`Finding.Expected`/`Unexpected`) | `internal/agent/loop.go`, `internal/provider/provider.go` (doc), `internal/provider/anthropic.go`, `internal/doctor/scan.go`, `cmd/fastclaw/cmd_doctor.go`, `web/src/components/chat-screen.tsx` (comments; the UI sweep was already the renderer) | `internal/agent/interrupted_turn_test.go` (new), `internal/doctor/scan_test.go` (+1), `cmd/fastclaw/cmd_doctor_test.go` (+1); `pad_orphan_tool_test.go` deleted with the code it pinned |
| **P4** ✅ | Truncation cannot split a pair *and* the claim is now tested: compacted history, after `normalizeForPrompt`, carries no pairing findings — verified with the doctor scanner as the oracle, for a cutoff landing inside a pair, for a retained tail that itself holds a duplicate, and for a retained tail that holds an open call (Q4's shape). `safeCompactionCutoff` is documented as an optimisation (it keeps the prompt byte-identical to last turn's) rather than the correctness guarantee | `internal/agent/compaction.go` (comment), `internal/agent/compaction_pairs_test.go` (new) | `TestCompactionOutputNormalisesToAPairingCleanHistory` (three shapes, through prune + compress) |
| **P5** ✅ | **Grace** (bounded, in-flight tools land their real result), **per-source budgets** (`TaskQueueCfg.CronTimeoutSec`; one policy site `Gateway.taskTimeoutFor` behind `submitTask`), and **hot reload** of the whole `taskqueue` namespace (`ReloadTaskQueue` + the `taskQueueReloader` hook, no pod roll; resize-safe semaphore swap) | `internal/agent/tool_grace.go` (new), `internal/agent/loop.go`, `internal/taskqueue/queue.go`, `internal/config/config.go`, `internal/gateway/gateway.go`, `internal/gateway/routing.go`, `internal/gateway/taskqueue_reload.go` (new), `internal/setup/handlers.go`, `cmd/fastclaw/main.go` | `TestToolGraceContextSurvivesCancellationForGrace`, `TestToolGraceContextStopEndsImmediately`, `TestToolGraceContextDisabled`, `TestTurnBudgetExpiryLetsInFlightToolRecordItsResult`, `TestSubmitWithTimeoutOverridesQueueDefault`, `TestTaskTimeoutForSourcePolicy`, `TestSetDefaultTimeoutAppliesToTheNextTask`, `TestSetMaxConcurrentResizesWithoutStrandingInFlight`, `TestReloadTaskQueueAppliesSystemConfig`, `TestReloadTaskQueueDegradesQuietly`, `TestTaskQueue_HotReloadCloudPathE2E` |
| **P6** ✅ | `sanitizeNUL` at the persistence boundary (session_messages + session_events) so a NUL-bearing tool result can no longer vanish from the archive; `fastagent doctor sessions` reports duplicate/orphan/unanswered pairings, exits non-zero on the *actionable* ones (duplicates and orphans — an unanswered call is expected and only reported, see Q4), and `--fix` removes duplicate replies after backing the row up | `internal/store/database.go`, `internal/doctor/scan.go` (new), `internal/store` `ListSessionSnapshots`, `internal/session/store_adapter.go` (`ProviderMessages`), `cmd/fastclaw/cmd_doctor.go` (new) | `TestAppendSessionMessageStripsNUL`, `internal/doctor` shape table + RawAssistant declarations + `TestExpectedCoversOpenCallsOnly`, `TestListSessionSnapshotsOrderingAndFilter`, `TestDoctorSessionsFindsAndFixesDuplicateReplies` (CLI end to end: seed → scan fails → fix → backup → clean), `TestDoctorSessionsTreatsOpenCallAsExpected` |

## Test plan

### Unit

* **Session gate** ✅ — `TestAcquireTurnSerializesCallers`,
  `TestAcquireTurnHandsOffFIFO`, `TestAcquireTurnContextCancelDoesNotLeakSlot`,
  `TestAcquireTurnCancelAtHandoffDoesNotStrandSlot` (200-iteration race).
* **Agent admission** ✅ — `TestHandleMessageWaitsForInFlightTurn` (no
  provider call, no history write, no return while the slot is held),
  `TestHandleMessageSerializesQueuedTurns` (history roles are exactly
  `user,assistant,user,assistant`, contents in arrival order).
* **normalizeForPrompt** ✅ — `TestNormalizeForPromptShapes`: well-formed
  history unchanged, legacy synthetic reply + real result collapsed, the
  incident's 2-calls-4-replies shape, orphan reply dropped, unanswered call
  (Q4's shape: an interrupted turn leaves the call open) answered in place,
  late reply pulled next to its call, empty input; every case also asserts
  idempotence and that the input slice was not mutated.
  `TestNormalizeForPromptReadsRawAssistantCalls` (call declared only in
  `RawAssistant`), `TestNormalizeForPromptStripsDuplicateCallDeclaration`.
* **Interrupted turn (Q4)** ✅ — `TestInterruptedTurnLeavesNoSyntheticReplyInHistory`:
  the loop detector breaks out mid-round, history keeps the open call and no
  synthetic reply, and `doctor.Scan(normalizeForPrompt(history))` is empty —
  the same oracle the provider contract is written against.
* **Admission** ✅ — `TestRunTurnDefersAutomaticSourceWhenSessionIsBusy`
  (refused, nothing written), `TestRunTurnQueuesUserSourceAndEmitsQueuedEvent`
  (queues, emits `queued` with position 1, runs after release),
  `TestTurnModeForSourcePolicy`.
* **Gateway parking** ✅ — `TestDeferredTurnsDrainPolicy` (busy sessions are
  skipped, per-chat FIFO head only, one submission per idle observation),
  `TestDeferredTurnsDropsMessagesPastBudget`.
* **Wire builder** ✅ — the P0 tests stay as defence-in-depth
  (`TestToAPIMessagesDropsDuplicateToolReplies`,
  `TestToAPIMessagesDropsDanglingToolReplies`,
  `TestToAPIMessagesKeepsAnsweredPairAndDropsStrayReply`).
* **Still to write** — a `toolu_*`/`call_*` mixed-provider snapshot fixture
  (the incident's real session mixed Anthropic and DeepSeek ids) and a
  multi-turn transcript with compaction in the middle.
* **Doctor / archive** ✅ — `internal/doctor` shape table +
  `TestScanReadsDeclarationsFromRawAssistant`,
  `TestAppendSessionMessageStripsNUL`,
  `TestListSessionSnapshotsOrderingAndFilter`,
  `TestDoctorSessionsFindsAndFixesDuplicateReplies`.
* **Tool grace** ✅ — `TestToolGraceContextSurvivesCancellationForGrace`,
  `TestToolGraceContextStopEndsImmediately`, `TestToolGraceContextDisabled`
  and `TestTurnBudgetExpiryLetsInFlightToolRecordItsResult`.
* **Budgets** ✅ — `TestSubmitWithTimeoutOverridesQueueDefault` (a cron budget
  outlives the queue default; a default task is still cut on time),
  `TestTaskTimeoutForSourcePolicy` (cron with/without the knob, everything
  else falls back), `TestSetDefaultTimeoutAppliesToTheNextTask`,
  `TestSetMaxConcurrentResizesWithoutStrandingInFlight` (reload safe under a
  running task), `TestReloadTaskQueueAppliesSystemConfig` /
  `TestReloadTaskQueueDegradesQuietly` (gateway), and
  `TestTaskQueue_HotReloadCloudPathE2E` (system-scope save fires the hook once;
  a user-scope save and a resolver without the capability both stay silent).
* **Compaction/pairing** ✅ — `TestCompactionOutputNormalisesToAPairingCleanHistory`.

### Integration / e2e

* **Web POST vs cron tick** ✅ — `TestConcurrentWebAndCronTurnSerialize`
  (`internal/setup/concurrent_turn_e2e_test.go`): a cron tick parked inside a
  long tool while a real dashboard POST arrives for the same session. Asserts
  the POST is announced as `queued` on its own SSE stream, writes nothing while
  the tick holds the slot, and that the history the two turns leave behind is
  `user,assistant,tool,assistant,user,assistant` with one reply per call and a
  clean `doctor.Scan`.
* **Queued chat POST** ✅ — `TestQueuedChatTurnIsAnnouncedAndWithdrawableE2E`
  (real handler + real agent + fake provider: the POST is announced with a
  `queued` event, `/api/chat/cancel` returns 200, the turn never reaches the
  model and the session stays empty) and `TestStartedChatTurnCannotBeWithdrawnE2E`
  (once started, cancel returns 409 and the turn completes).
* **Queued turn behind a long tool** ✅ — `TestQueuedTurnRunsAfterLongTool`
  (`internal/agent/turn_queue_test.go`): turn A holds the slot through a slow
  tool; turn B must not append anything — not even its own user message —
  until A released, and the two turns land as whole turns in arrival order.
* **Goal continuation** ✅ — `TestGoalContinuationDoesNotDeadlock`
  (`internal/agent/goal_continuation_gate_test.go`): the PostTurn hook fires
  while the turn still holds the slot, so it publishes the continuation onto
  the bus; the test asserts the turn returns, the continuation carries
  `Source=goal_context` and the goal's chat id, and that the continuation can
  then take the slot (which is what an inline call would have deadlocked on).
* **The incident's timing, agent level** ✅ — `TestCronTickDoesNotInterleaveWithWebTurn`
  (`internal/agent/turn_queue_test.go`): cron source in a long tool + a user
  message during it; asserts the same serialized shape and that no
  `tool_call_id` is answered twice — the second answer is what 400s a session.
* Replay harness `(not in the tree)` `TestZZReplay`: run as
  `FA_DIAG_HISTORY=<jsonl> go test ./internal/provider/ -run TestZZReplay`
  (offline, never part of CI) ad hoc against the incident's snapshots — they
  replay clean under both the rules deployed that day and the P3 rules. The
  harness itself was not kept; re-create it from this line if a replay is
  needed again.

### Verification against production

* `fastagent doctor sessions --session hJKMWwtOp3mJOtqN8Uz2mW` returns clean
  after the P0 repair (it did: 517 → 512 messages, 0 duplicate ids).
* After deploy: grep the gateway logs for
  `must be a response to a preceding message` over a 24 h window and for
  `padding orphan tool_use` paired with a later real result for the same id
  (pre-Q4 images only — that log line is gone now, and its absence is the
  point: nothing writes a synthetic reply into a session any more).
* `sessions.messages` scan for duplicate `toolCallId` across all sessions.
  Since Q4, `doctor sessions` exits 0 when the only findings are unanswered
  calls, so this scan is now a clean gate again.

## Rollout, verification, rollback

1. **P0.5** — ship the landed fixes to `development`, replay the incident
   session through a live conversation, then `production`.
2. **P1** — ship behind a config flag (`turn_gate_enabled`, default on in
   dev) so it can be disabled without a rollback; watch
   `turn admission wait` logs for p99 and for waits that exceed a turn
   budget (P5 will make those a first-class outcome).
3. **P2–P4** — no flag needed; each is covered by unit + integration tests
   and by the doctor checker before/after.
4. **Rollback** — the gate holds no persisted state, so rolling the image
   back is complete; the data repair is independent and reversible from the
   backup taken during the incident (`/tmp/fa-diag/backup_session_*.json`).

## Open questions for review

| # | Question | Current default in this doc |
|---|---|---|
| **Q1** | IM inbound while a turn is running: keep auto-steer, or queue like the dashboard? | keep auto-steer (D3) |
| **Q2** | Does the dashboard need a visible "排队中" state, or is the typing indicator enough? | yes, add one (P1b) |
| **Q3** | Cron/goal on a busy session: block the queue worker (P1) or `NotAdmitted` + re-queue (P2)? | P1 blocks (bounded by the queue timeout), P2 re-queues |
| **Q4** | Keep synthetic "interrupted" replies persisted, or move them to prompt-only (Codex parity) and render them in the UI from the call? | **prompt-only, decided 2026-09-14** (Codex parity): the projection writes the reply, stored history keeps the call open, the UI renders `(stopped)` from the call, and the doctor scanner treats the open call as expected. The persisted pad's only remaining effect was the incident's collision risk |
| **Q5** | Per-source turn budgets and the graceful-interrupt semantics (P5) | web 15 min, IM 300 s, grace 60 s |
| **Q6** | Do we need cross-replica session locking (store lease) now, or is in-process enough? | **not now, decided 2026-09-14**: the in-process gate is enough until one of the measurable triggers appears — a post-Q4 `duplicate_tool_reply`, a cross-turn interleave in stored history, or two pods waiting on one session. Triggers, rationale and the fix's shape: [Deferred: cross-replica session lease](#deferred-cross-replica-session-lease-q6) |
| **Q7** | Should the checker ship as a CLI subcommand or a test-only harness? | CLI subcommand (`doctor sessions`), P6 |

## Deferred: cross-replica session lease (Q6)

**Decision (2026-09-14): not now.** Clause W holds per *process*. The gap is
real but unobserved, and closing it costs availability — so the thing that
reopens it is evidence, not a date. This section is the record of that
decision and of what to build when the evidence shows up.

### Why not now

* The incident's symptom — a session that 400s forever — is already gone
  without the lease. A cross-pod interleave today costs a duplicated or
  misordered message inside one turn; the wire builder drops a duplicate
  reply, the prompt projection repairs the request (P3), the loop no longer
  persists a synthetic reply that a late result can collide with (Q4), and
  the doctor reports whatever is left. Nothing writes a permanent hole any
  more.
* Every turn would pay a store round trip for a property no measurement has
  asked for. The only admission contention in production so far is
  same-process: the dashboard POST queued behind the cron tick that started
  the incident, which is exactly what the in-process gate fixed.
* A lease adds a new way to be unavailable: the agent loop would need the
  store to *start* a turn (the sandbox pool accepts that coupling for
  long-lived executors, but a turn is a much hotter path), plus TTL renewal,
  a fencing token so a stale holder's appends cannot land, and a policy for
  "lease store unreachable". Availability is worth more than an unobserved
  concurrency property.

### What reopens it (measurable triggers)

In rough order of how cheap they are to see:

1. **A post-Q4 duplicate.** `doctor sessions` reports a
   `duplicate_tool_reply` on a session whose whole history was written after
   Q4 shipped. No persisted pad exists to explain that shape any more, so
   some other writer produced it.
2. **Cross-turn interleaving.** Stored history shows turn B's user message or
   reply between turn A's call and its reply (the shape
   `TestHandleMessageSerializesQueuedTurns` rejects). One process cannot do
   that.
3. **Two pods on one session.** Two replicas log
   `turn admission: waited for the in-flight turn` for the same session
   within one turn budget. Prep needed before this trigger is usable: that
   log line carries `chat_id` but not the session key or the pod identity —
   add both, or the trigger is invisible.

### Where the overlap would come from

The gateway runs two replicas (`deploy/helm/fastagent/values.yaml`,
`deploy/k8s/fastagent.yaml`). The ingress does pin a *browser* to a pod —
cookie affinity (`fastagent-affinity` in
`deploy/helm/fastagent/templates/ingress.yaml`) with a `ClientIP` Service
fallback — but the server-originated sources (cron tick, goal continuation,
webhook) fire on whichever replica owns the queue. There is no leader
election and no session→pod routing for them, so affinity covers the web half
of the traffic while the combination that actually produced the incident
(dashboard turn + cron tick) is only serialized when both land on one pod.

### Shape of the fix when it is triggered

Reuse the sandbox lease design rather than inventing one
(`docs/sandbox-pool-leases.md`, upstream PR #124; the store slice is measured
in [upstream-pr-split.md](upstream-pr-split.md)):

* scope key `(user_id, agent_id, session_key)` instead of a sandbox pool id;
* owner = pod identity, TTL ≥ turn budget + tool grace, renewed while the turn
  runs; CAS adoption and the `state`/`paused_at` columns already exist in that
  design to copy from;
* the in-process FIFO gate stays the fast path — only a caller that finds a
  live lease held by *another* pod pays the round trip, and the `queued` event
  (P1b) is already the user-facing half of "you are waiting for the other
  writer";
* a fencing token per acquisition, checked where the session appends, so a
  holder that lost its lease (crash, partition, TTL expiry) cannot keep
  writing when its goroutine resumes.

If the trigger fires before that work is ready, the cheaper intermediates are
all partial:

* **Stickiness** — route a session's turns to one pod by hashing the session
  key. It closes the web half only, unless the server-originated sources hash
  the same way.
* **Write fence without queueing** — a monotonic generation per session,
  compared on append so a stale writer is rejected rather than interleaved.
  This still needs the store to arbitrate a compare-and-append, i.e. most of
  the lease's cost without its ordering semantics.
* **Park automatic sources by policy** — P2 already parks them per process; a
  cross-replica version needs the same shared state, so it is not cheaper.

## Appendix A — incident evidence

Session: agent `agt_cda27bbfbf4a84e2dfa6`, `session_key` /
`chat_id` `hJKMWwtOp3mJOtqN8Uz2mW`, owner
`u_396a1f8812880c67e6ff`, model `deepseek/deepseek-flash` at failure time
(`claude-sonnet-4-6` earlier and later); cron job `kronos-crypto-resume`;
gateway image `20260913111345-fastagent-6345e2b`.

```text
11:30:00.011 firing store-backed cron job  id=5498f0ea-… name=kronos-crypto-resume
11:30:00.292 task submitted  task-1789299000292-1 chat_key=web::hJKMWwtOp3mJOtqN8Uz2mW
11:34:32.776 turn: refreshing skills   chat_id=hJKMWwtOp3mJOtqN8Uz2mW   ← second turn (dashboard)
11:34:41.381 hook: before tool call    tool=list_cron_jobs / tool=exec
11:35:00.294 tool execution error      tool=exec  (e2b snapshot failure, tool still in flight)
11:35:00.439 WARN padding orphan tool_use with stopped result toolCallID=call_00_38dF…S13513
11:35:00.567 WARN padding orphan tool_use with stopped result toolCallID=call_01_3nA…hH6960
11:35:00.897 task completed            duration_ms=300604      ← 300 s timeout killed turn A
11:35:32.146 openai request            api.deepseek.com/v1/chat/completions
11:35:32.957 API error 400: Messages with role 'tool' must be a response to a
             preceding message with 'tool_calls'
…same 400 on every cron tick through 13:45:02 (40 error lines on the pod that
served the ticks; the second replica logged the 19:50 repeat below)…
19:50:06.124 same 400 after the agent was switched back to deepseek-flash
```

History snapshot analysis (13 `history_*.jsonl` snapshots written by
compaction, replayed through the repo's own wire builder):

| Snapshot | Duplicate replies | Provider | Outcome |
|---|---|---|---|
| 11:30:01, 11:34:33 | 0 | deepseek | ok |
| 11:35:32 … 13:45:02 (11 snapshots) | 1 run with 4 replies for 2 calls | deepseek | 400 every time |
| same snapshots | 1 | anthropic | ok |
| 19:50:05 | 1 | deepseek | 400 |

Stored history after the incident: 5 `tool_call_id`s answered twice
(`…OfGixT0207`, `…GCBlDA3546`, `…FgPCS13513`, `…v22vhH6960`, `…kP8wt7ABwG`);
in every one of the five the first reply is the
`(stopped — execution was interrupted before the tool returned)` pad and the
second is the real result.

## Appendix B — the P0 repair (already applied)

1. `internal/provider/openai.go` — a `tool_call_id` may be answered at most
   once on the wire; later replies are dropped (first reply wins, i.e. the one
   adjacent to the declaring assistant).
2. `internal/agent/loop.go` — `padOrphanToolResults(sess, turnToolCallIDs)`
   pads only ids the current turn declared, resolved against the whole
   session; the pad literal is shared as `provider.StoppedToolResult`.
   **Superseded by Q4**: the function is gone; the literal now belongs to the
   projection. This entry is kept because it describes the binary that was
   deployed on 2026-09-14 and the repair that ran against production data.
3. `internal/agent/compaction.go` — summariser receives the live `ctx`
   (`API error`-free compaction; previously `net/http: nil Context`).
4. Production data — the incident session's `sessions.messages` went from 517
   to 512 entries (5 duplicate pads removed, real results kept) and
   `session_messages` from 573 to 572 rows; the row was backed up first to
   `/tmp/fa-diag/backup_session_20260913T202427Z.json`. Verification: the
   repaired history replays through the wire builder with 0 rejected replies
   and 0 runs with extra replies under **both** the deployed and the new
   rules — i.e. the repair alone unblocked the session on the old binary.

## Appendix C — deployment checklist for P0.5

```bash
cd /Users/reina/Project/tokenaissance/fastagent
go test ./internal/provider/ ./internal/agent/ ./internal/session/ -count=1
./build-image.sh dev                       # tag: <ts>-fastagent-<sha>, namespace development
# smoke: run one turn in the incident session, check logs for the 400 string
./build-image.sh prod                      # requires typing 'production'
```

Post-deploy verification:

```bash
kubectl logs -n production -l app=fastagent --since=24h | grep -c "must be a response to a preceding"
# expect 0

# History debt behind the request-time repairs (duplicates are what --fix removes):
FASTAGENT_STORAGE_DSN=... fastagent doctor sessions --json
```

### Build-time gotcha (2026-09-14)

`docker build` needs the **amd64** base images (`node:22-alpine`,
`golang:1.25-alpine`, `alpine:3.21`) because the helm images are built with
`--platform linux/amd64`, while a developer Mac may only hold arm64 copies.
When `registry-1.docker.io` is unreachable (it was, twice: `Bad Gateway`, then
`context deadline exceeded`), the build dies in "load metadata" before any
layer runs. Working around it without touching the Docker daemon:

```bash
for img in node:22-alpine golang:1.25-alpine alpine:3.21; do
  docker pull --platform linux/amd64 "docker.m.daocloud.io/library/$img"
  docker tag "docker.m.daocloud.io/library/$img" "$img"     # digest-identical upstream layers
done
```

That replaces the local arm64 tags — re-pull them (`docker pull <img>`) if an
arm64 build is needed later. The in-container `pnpm install` uses npmjs
(reachable) and `go mod download` falls back to `direct` → github.com
(reachable), so only the base images need the mirror.
