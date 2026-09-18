# 08 · The state observability principle (a harness design constraint)

> Status: principle + field audit · last verified: 2026-09-18
> Origin: the 2026-09-17 deliverable-revert incident ([04](./04-incident-workspace-2026-09-17.md)) and the
> six rounds of repair that followed, during which the same judgement kept reappearing until it was
> distilled into this principle. Filesystem sync is its first application, but what it constrains is
> the whole harness (see the audit scope in §5).
> Prerequisite reading: [07 §3.11](./07-formal-rootcause-and-fix.md) (how the mechanism converged to four).

## 1. The principle

> **A state change inside the harness must be perceivable by the agent.**
> **Otherwise the agent reasons about a world that no longer exists — and has no way to notice.**

This is not a restatement of "write logs". The reader of a log is an operator; **the reader of this
principle is the agent**, so the signal has to appear in a channel the agent actually consumes
(tool results, the next turn's context) — not in slog.

## 2. The formal statement

Let the agent's belief about its own world at time `t` be `Belief(t)` — everything it holds to be
true about "what the files contain, which skills exist, what is remembered, which tools it can
call". Let the world's real state be `World(t)`.

Actions of the harness change `World`. The principle requires:

```
∀ change δ : World(t) → World(t')
    if δ was triggered by the agent's own action
        then Belief is updated through that action's result (the tool result is the receipt) — satisfied by construction
    if δ was triggered by the harness itself (sync write-back, eviction flush, background compaction, memory update, skill refresh, …)
        then there must exist an agent-consumable signal σ(δ) such that
              ¬∃t'' : World and Belief stay inconsistent before σ is delivered and the agent cannot notice
```

**The cost of violating it is measurable**: in the incident the user saw three files reverted while
the agent reported "restored" in two consecutive turns — because nothing told it that the store had
been written back. Its `Belief` stayed on a `World` that no longer existed, so every later inference
("what do I do next") rested on a false premise. Note this is worse than losing a file: **losing a
file is data loss; a belief/reality mismatch is inference corruption**, and the latter spreads into
every subsequent decision.

### 2.1 Formal symbols ↔ code identifiers

The code no longer uses `report` / `notice` as mechanism names: **δ is the fact, σ is the sentence
the agent reads**, and identifiers follow the same symbols so reading the code needs no second
translation.

| Formal symbol | Meaning | Code identifier |
|---------------|---------|-----------------|
| `World(t)` / `Belief(t)` | the world's real state / the agent's belief about it | `envSnapshot` (the per-turn sample, i.e. the `World` the harness believes in); `delta` (the world change one sync observed) |
| `δ` (delta) | **one fact that the world changed**, structured and accumulable | `sandbox.delta{moved, blocked, problem}` (`lifecycle.go:615`), `sandbox.WriteThroughOutcome` (`lifecycle.go:1244`) |
| `σ(δ)` (signal) | δ turned into **one sentence the agent can read** | `signalsFor(delta)` (`lifecycle.go:1015`), `Registry.writeThroughSignal` (`file.go:894`), `envTracker.signal` (`env_changes.go:69`) |
| exit | the single delivery point for σ inside one category | the result of `lazyExecutor.Exec` (appended), `Registry.workspaceSignalExit` (`registry.go:1094`, append/prefix), `ContextBuilder.SetEnvironmentSignal` (`context.go:89`, appended to the end of the system prompt) |
| queue | δ happened but there is no delivery point right now | **no in-process queue any more**: the `sandbox.SignalStore` port with `parkSignal` / `takeSignals`, implemented by `gateway.sandboxSignalStore` (a scope-keyed `configs_kv` row: across processes and replicas, deleted as it is delivered). It carries only the facts that **cannot be recomputed** (paths an eviction sync wrote into the store); refusals and failures are not queued |

Discipline: **a mechanism only produces δ (the fact); wording and placement (σ) belong to that
category's single exit**. Nothing outside the exit may build its own string (`addSignal` warns
instead of silently dropping when it cannot find the exit). Test names follow the same vocabulary:
`TestExecObservesSandboxChanges` (a δ was observed), `TestEvictionSignalReachesNextToolResult`
(a σ was delivered once), `TestWriteFileStaysQuietWhenMirrorIsUneventful` (no δ, no σ).

### 2.2 Delivery, stated formally: who produces, who places, who takes

> **This document carries two formal systems**, and they are the two predicates of one sentence:
> "a **state change** inside the harness must be **perceivable** by the agent" — the first half is answered
> by **§2 / §2.1 / §3 (F2, observability)**: which changes must be stated, and that a σ must be true; the
> second half by **§2.2 (F3, delivery)**: who places it, on D₁ or D₂, who takes it, and whether it can be
> lost on the way.
> The document set carries a **third** system — **F1, preconditions / zero migration**
> ([06](./06-cordis-review.md) / [07](./07-formal-rootcause-and-fix.md)) — which answers "may this
> migration happen at all". Their division of labour: **F1 decides whether the mechanism may act, F2
> decides whether it speaks, F3 decides whether the speech arrives**; a design is only complete when all
> three questions have answers. The full index is **[00-formal-systems.md](./00-formal-systems.md)**.

§2 only says "there must exist an agent-consumable σ" — and "exist" is vague: a σ can be **rendered** and
never arrive (§6.1 lost one exactly once). This section tightens "exist" into a decidable method: three
roles, one invariant, five obligations, and a proposition that says only one shape is reachable.

#### 2.2.1 Three roles and invariant I1 (separation of the three powers)

```
produce(δ) → σ   whoever changed the world owns the complete fact, and renders the sentence
place(σ)         put σ where the agent is bound to read
take(σ)          carry it away on the agent's next read
```

> **Invariant I1 (separation of the three powers)**: `produce` and `place` belong to the **change side**;
> `take` happens on the **consumer side**, at its own read boundary. `place` may not be pushed onto the
> consumer ("some subsystem will come and collect it"), and `produce` may not reach into the consumer's
> internals ("tell the model that is currently thinking").

| Role | Owner | Where it lives here |
|------|-------|---------------------|
| `produce(δ) → σ` | the change side | `signalsFor(delta)`, `writeThroughSignal`, `envTracker.signal`, the rendering of the rebuild note |
| `place(σ)` | the change side | `parkSignal` (nobody is reading right now), appending inside a tool result, `SetEnvironmentSignal` (turn entry), the **user-side** outbound note for a dropped cron turn |
| `take(σ)` | the consumer side (its read boundary) | `takeSignals(...) + signalsFor(d)` inside the `lazyExecutor.Exec` result; `BuildSystemPromptAs` folds the environment signal into the prompt |

#### 2.2.2 There are only two delivery points (plus a "waiting area")

**A delivery point is a place the agent is bound to read.** The whole harness has two:

```
D₁  the call receipt (a tool result)   answers "what happened to the thing I just called"  exists only while the agent is calling
D₂  the turn entry (the turn prompt)   answers "what happened while I was away"             exists only when a new turn starts
```

**The waiting area is not a delivery point**: between `place` and the arrival of D₁/D₂, σ needs somewhere
that outlives a process — otherwise O4 fails. Here that is `sandbox.SignalStore` (a scope-keyed
`configs_kv` row). **An in-process queue is not a waiting area**, because it dies with the process
(09 §3, G3).

#### 2.2.3 The five obligations

| # | Obligation | The cost of violating it | Instances here |
|---|------------|--------------------------|----------------|
| **O1 produce** | whoever changed it renders it, and says only what is true | a false σ teaches the model to skip the whole class | the four `CompareResult` states (G5); "an unreadable list says so" (G10) |
| **O2 place** | σ must land on D₁ or D₂ | the signal may as well not exist | the dropped eviction report in §6.1; G7a's `list_dir` divergence line |
| **O3 take** | taking happens on the **consumer's next read**; the consumer may not be required to subscribe | requiring a subscription implies an unimplementable interface (see P1) | `takeSignals` at exec; the environment signal at prompt assembly; `bash_output` (the exit status is recomputed from the world on every read ⇒ a recomputable criterion, place 1) |
| **O4 no loss** | the waiting area between `place` and `take` must span processes and replicas | a restart or a lease hand-off drops it | G3: recomputable facts (refusals/failures) are not queued, the one that cannot be recomputed (moved) goes to the durable carrier |
| **O5 no noise** | no δ, no σ; and never interrupt reasoning in progress | wall-paper noise (C3); interrupting is structurally impossible anyway (see P1) | silence on docker; an unreadable list never reported as a deletion |

**The second form of O4 (added 2026-09-18)**: O4 is not only "σ was lost in the waiting area". **Keeping
the σ's own criterion — its baseline — in process violates it too**, because when the baseline dies with
the instance the σ is not lost in transit, it **cannot be produced at all**. G9 is the cleanest example:
a configuration change takes effect **by rebuilding the Agent**, so the tracker that should report it is
destroyed by the very change it should report — and "first observation is not a change" swallows exactly
that change.

There are three legitimate places for a criterion, in increasing cost — **try them in order**:

| # | Where the criterion lives | The test | Instance |
|---|--------------------------|----------|----------|
| 1 | **nowhere** (recomputable) | it can be derived from the world on the next read | G3: refusals/failures are not queued, they are recomputed |
| 2 | **reuse an existing durable record** | the fact already has an owner and that record is already being written | G9 + G20: before = the conversation's own turn receipt (`provider`/`model` columns + `run_receipt`, carrying the whole world snapshot) — no new storage, no new write path, and five families cross restarts and replica hand-offs together |
| 3 | **a new durable carrier** | neither of the above holds; the fact has no other owner | G3: `moved` goes to `sandbox.SignalStore` (a new row, deleted on take) |

Row 2 only got used on the third pass of 2026-09-18: the first version created a `cfg_seen` row for the
config baseline (place 3). It worked, but it was a second copy of one fact — the shape this document set
keeps running into. **Ask whether the fact already has a home before building one.**

#### 2.2.4 Proposition P1: only the pull shape is reachable (push is not implementable)

> **P1**: there is no implementation that delivers σ *while the agent is thinking*.
>
> **Proof**: the consumer is a **synchronous model call** — it takes (messages, tools) and returns a
> response; the protocol has no "inject mid-generation" slot, and the driver layer (the model provider)
> exposes no receiving end. Delivering mid-reasoning would require the consumer to expose an inbox, i.e.
> the driver to offer an injection channel — which means **writing a driver detail into the policy**
> (violating the dependency direction; 02 §1.3/§1.4). So the push shape is neither implementable nor
> necessary. ∎
>
> **The one near-miss is user steering**: it buffers a user message on the session, and the running loop
> takes it **between two tool iterations** (`appendSteer`). Two qualifications: ① it is still "checked
> between two model calls", not injected into a generation; ② what it carries is **user input**, not a
> harness state change. So it is not a counter-example.
>
> **Corollary P1′**: every harness δ must land on D₁ or D₂. "Later than the fact" is acceptable
> (09 §4, caution A); "no delivery point" is not.

#### 2.2.5 The decision procedure (run a new mechanism through it)

```
Given a mechanism M that changes the agent's world:

1 produce  what did M change (δ), and who did it?
     the agent itself → the tool result is the receipt, stop here (C1)
     the harness      → continue
2 σ        render δ as one TRUE sentence; if it cannot be said truly, change the wording (O1)
3 place    which delivery point does the sentence land on?
     a call is in flight → D₁ (the tool result)
     only a turn boundary → D₂ (the turn prompt)
     neither              → do not stop here: design a future delivery point explicitly (§6.1)
4 take     who takes it, and when? The answer must be "the consumer's next read"
5 O4       what does it cross between place and take?
     inside one call       → the call stack ✔ (e.g. the rebuild note)
     across calls, one pod → an in-process queue ✘ (a restart drops it, G3) → recompute or persist
     across pods           → a durable carrier, or recomputable (blocked / problem)
6 O5       silent when there is no δ? (C3)
```

#### 2.2.6 Layer attribution (Clean Architecture)

| Layer | Content | Criterion |
|-------|---------|-----------|
| Entities | the invariant: `Belief` may not drift from the world | §1 |
| Use Cases | **the delivery policy**: I1 + O1–O5 + one exit per category | the policy depends only on ports it defines |
| Interface Adapters | `SignalStore` (durable waiting), `ReplacedWorkspace` (a one-shot fact), `UnhydratedWorkspace` (a state declaration), the two projections | a port must declare **semantics**, not just a capability bit (02 §1.3) |
| Frameworks & Drivers | `configs_kv` rows, the sandbox HTTP API, **the model provider (no receiving end ⇒ P1)** | dependency direction: adapter → port → policy |

```
Frameworks & Drivers ──implements──▶ Interface Adapters ──▶ Use Cases ──▶ Entities
(configs_kv / the provider)          (ports like SignalStore)  (delivery policy)  (the invariant)
```

In one line: **placing is the change side's active obligation, perceiving is the agent's passive
mechanism — "active" means the change side must not stay silent, not that it may interrupt.**

## 3. Three corollaries

| # | Corollary | Design consequence |
|---|-----------|--------------------|
| C1 | **The signal must enter a channel the agent really reads** | tool results / the next turn's context; slog does not count (readable by operators ≠ readable by the agent) |
| C2 | **No signal ≠ no change** | whenever the harness may have moved the world away from the agent's belief, it must say so; silence asserts "the world is as you think" |
| C3 | **A signal must be an exception channel** | it does not appear when nothing changed. A line attached to every call teaches the model to skip it — which is the same as having no signal |

## 4. Which changes need a signal

| Source of change | Naturally perceivable? | Disposition |
|------------------|------------------------|-------------|
| the agent's own tool call | ✅ the tool result is the receipt | no extra mechanism |
| the harness changed the workspace (sync write-back, eviction flush) | ❌ | **must be signalled** (including the actions it refused) |
| the harness changed the agent's identity / memory / skills | ❌ | **must be signalled** (especially removals, see C2) |
| the harness changed the tool set / capabilities (MCP load, skill refresh) | ❌ | **must be signalled** (invisible-by-absence is the hardest kind to notice) |
| the harness changed the context (compaction, clipping) | ⚠️ the result is visible, the event may not be | report "what happened", not just the new state |
| the harness changed the execution environment (sandbox replaced, rebuilt, unhydrated) | ❌ | **must be signalled** (precedents exist: `[sandbox replaced]`, `workspaceUnhydratedSignal`) |

## 5. Audit of the current harness

> This section lists changes (which changes need a signal). The component-by-component checkup —
> both directions, "triggered by the agent" and "triggering the agent", plus gaps G5–G13 (G5 being
> a σ that is **always false**, reproduced locally) — is [10](./10-harness-state-audit.md). The two
> sections complement each other: this one answers "does this kind of change have a signal?",
> 10 answers "did this component slip through?".

| State change | Agent-side signal | Verdict |
|--------------|-------------------|---------|
| a tool writes to the store | the tool result | ✅ |
| sync writes sandbox changes into the store (post-exec) | `exec` result `[workspace] the sandbox changed …` | ✅ 2026-09-18 |
| a path the sync refused | `exec` result `[workspace] NOT synced …` | ✅ 2026-09-18 |
| **the sandbox deleted a file** | **no signal**: the walk's domain is the sandbox snapshot, so a deleted path is not in it, and it cannot be told apart from "a store-only upload" | ❌ **G4** ([09](./09-sandbox-lifecycle-audit.md) §3: restoring detection needs a **durable** store-side manifest, in-process state will not do) |
| **a sync that only happened during idle eviction** | previously the signal was dropped → now queued to the next tool result, delivered exactly once | ✅ 2026-09-18 |
| write-through replaced a different version in the sandbox | write result `[workspace]` (byte count, no content) | ✅ 2026-09-18 |
| write-through found a different version and had no earlier copy to compare it against | write result `[workspace]`: "the sandbox held a different version (N bytes), with no earlier copy to compare it against" | ✅ 2026-09-18 (this branch used to claim "over 2 MiB" for every write that reached it; see [10](./10-harness-state-audit.md) §2.1, G5) |
| the sandbox is unreachable / was replaced | write result `[workspace]`, note on the `exec` error | ✅ |
| **a path the store has and the live sandbox does not** (caused by a user upload/delete) | `list_dir` appends `[workspace] N path(s) … NOT in this sandbox …` after the listing | ✅ 2026-09-18 (no signal at all before; see [10](./10-harness-state-audit.md), G7a) |
| workspace not hydrated (the store listing failed) | `workspaceUnhydratedSignal` | ✅ existing precedent |
| a tool result was clipped | an inline clip marker plus "how to see all of it" (`clipMarker`) | ✅ existing precedent |
| the goal budget is exhausted | `BudgetLimitPrompt` delivered into the session | ✅ existing precedent |
| context compaction | both the summary and the clip placeholder **state what happened** ("earlier turns were compacted…it is lossy", "dropped by context compaction…re-run it") | ✅ 2026-09-18 |
| background memory update (heartbeat) | the unified **environment-change signal**: `long-term memory was rewritten/created/CLEARED` | ✅ 2026-09-18 |
| the skill list refreshed between turns | same: `skills added / removed / changed` — **removals carry names too** | ✅ 2026-09-18 |
| **an identity file was edited from outside** (SOUL / IDENTITY / USER / AGENTS / …) | same: `identity files changed: USER.md` — **the file is named, its content never quoted** | ✅ 2026-09-18 (there was no signal at all before: the prompt changed content while the agent believed it had not) |
| the agent's configuration changed (model / prompt mode) | same: `my configuration changed: model=… → model=…` | ⚠️ 2026-09-18, partial: **a config change rebuilds the Agent, so a new tracker's first observation is silent**; crossing a rebuild needs a persisted baseline (see [10](./10-harness-state-audit.md), G9) |
| **the scheduled-job list was changed from outside** (a cron job added, rescheduled or deleted from the panel or another session) | same: `scheduled jobs added / changed / no longer exist: <name>` (only definition fields are fingerprinted; the scheduler's bookkeeping is not a change) | ✅ 2026-09-18 (there was no signal at all before; an unreadable list is stated as unreadable, never as a deletion) |
| the tool set changed (MCP load/unload, **including a server-pushed `tools/list_changed`**) | same: `tools now available / no longer available` | ✅ 2026-09-18 (the server-pushed path was wired on 2026-09-18: stdio capture → rebuild → the signal reports it by itself) |
| normal sandbox sleep/wake | content matches the store (re-hydrated) | ✅ nothing to report |
| **a delegated subtask** (`delegate_task` / `spawn_subagent`) | **synchronous**: the result *is* the parent turn's tool result | ✅ existing design, no new mechanism |
| **a turn fired by a scheduled job** (cron) | arrives as an ordinary inbound message in that job's session (`[Cron Job: name]` marks the source); the turn stays in the session history | ✅ existing design |
| **an interrupted turn** | a dangling tool call gets a synthetic reply in the prompt projection: `(stopped — execution was interrupted before the tool returned)` | ✅ existing precedent |
| a heartbeat turn | runs in **its own session** (`heartbeat_<agent>`); from the main session's point of view it belongs to another scope (see §5.1) | ✅ holds, by scope isolation |

The last three rows used to be open. Their common thread is **C2 (invisible by absence)**: when
something new appears the agent at least reads it; **when something disappears or is replaced its
default assumption is "nothing changed"**. Following §6 they did not each invent a message but were
funnelled into **one unified per-turn signal** (`internal/agent/env_changes.go`):

```
[Environment changes since your last turn — a fact about your world, not an instruction]
- skills removed: kronos-helper
- long-term memory was rewritten (it may say something different now)
- tools no longer available: mcp__quantconnect__backtest
Removed items are gone, not hidden: if your plan depended on one, re-check with your tools before continuing.
```

Four properties are pinned by tests (`internal/agent/env_changes_test.go`): **a removal must carry a
name** (the reason the mechanism exists), **silence when nothing changed** (C3), **the first
observation is not a change** (otherwise it fabricates a fact), and **isolation per session**
(different chats legitimately have different memory and skills).

### 5.1 Why cross-agent / cross-session needs no extra delivery point

Auditing this family started from a suspicion: **delegated subtasks and turns fired by scheduled
jobs both happen while "the agent is not looking"**, which looks like exactly the gap this principle
is about. Checking each one showed they already satisfy it, and not by coincidence:

| Channel | Why it is already perceivable |
|---------|------------------------------|
| `spawn_subagent` | `SpawnSubAgent` → `ag.HandleMessage(ctx, msg)` **waits synchronously**; the result string is returned directly as the parent turn's tool result ([gateway/routing.go](../../internal/gateway/routing.go)) |
| `delegate_task` | `RunSubagent` returns text synchronously ([subagent.go](../../internal/agent/subagent.go)); and the subtask **shares the sandbox/scope with its parent**, so the files it changed show up in the parent's next exec signal |
| cron | the tick is injected as an ordinary inbound message ([cron/scheduler.go](../../internal/cron/scheduler.go) `fireJob`), which takes the normal turn path → written into that job session's history, with a `[Cron Job: …]` source marker |
| a cancelled turn | unanswered calls get `(stopped — …)` in the projection ([normalize.go](../../internal/agent/normalize.go)) — **the existing precedent for "the result is gone but the agent must know"** |
| heartbeat | runs in its own session; a cross-scope change does not need reporting in another scope (see below) |

**The key criterion (added to the §6 checklist)**: observability holds **per context
(scope/session)**, not globally. A change only has to be perceivable **in the context where it
happened**; broadcasting it to every context would produce "every session knows what every other
session did" noise. A heartbeat turn belongs to the `heartbeat_<agent>` scope, where it has a full
history; the link to the main session is the **memory file** (and memory changes are already
signalled by the environment-change signal). Likewise, a subtask's result belongs to the call that
started it.

So this family **added no mechanism at all**: the audit's conclusion is that they already satisfy
the principle, and another delivery layer would be the same "redundant insurance" that was deleted
once before.

## 6. Review checklist (any new mechanism must pass)

When adding any mechanism that changes harness state, answer each line:

- [ ] Was this change caused by **the agent's action**, or by **the harness itself**?
- [ ] If the latter: through **which channel** does the agent learn it? ("slog" is not an answer)
- [ ] **Who `place`s this change?** (§2.2, O2: the answer must be "the change side put it on D₁ or D₂";
      "some subsystem will collect it" means there is no delivery point)
- [ ] **When** is the signal delivered? Could that be later than the agent's next relevant inference?
- [ ] **Can it be lost between `place` and `take`?** (§2.2, O4: in-process state means yes; it must be
      either recomputable or durable)
- [ ] Does **the absence** of the signal mean exactly "nothing changed"? (C2: silence must not be ambiguous)
- [ ] Does the signal appear only when something is wrong? (C3: the normal case is noise)
- [ ] Is there a test asserting **the signal exists**, and **the silence when it should be silent**?
- [ ] If the change is a removal/replacement: can the agent confirm whether the thing is still there?
- [ ] **Which context does this change belong to?** It only has to be perceivable in the
      scope/session **where it happened**. Before broadcasting to all contexts, ask: does the agent
      really need to know there? (Cross-scope broadcast is the over-design §5.1 rules out.)

Reference implementations (inside this directory):
`TestExecObservesSandboxChanges`, `TestExecIsQuietWhenNothingChanged`,
`TestExecObservesRefusedPaths`, `TestEvictionSignalReachesNextToolResult` (delivered once),
`TestWriteFileStaysQuietWhenMirrorIsUneventful`.

### 6.1 Why item 4 deserves its own section: a signal that is *produced* is not a signal that *arrives*

**A real counter-example from this repair.** The sync channel has two triggers; the first one always
carried its report, the second was written like this:

```go
// post-exec: δ becomes σ, attached to the exec tool result — it arrives ✅
d := l.pool.syncSnapshot(ctx, l.scope, ex, "post-exec")
out += l.pool.takeSignals(ctx, l.scope) + signalsFor(d)

// evict (idle eviction): the δ happened, but there is no tool result right now ❌
func (p *LifecyclePool) flushIfSupported(sc sandboxScope) {
    ...
    p.syncSnapshot(context.Background(), sc, ex, "evict")   // nobody takes the return value
}
```

Consequence: **a sync that only happened during idle eviction was never seen by the agent** — and
"the time the agent was idle" is exactly the window in which changes are most likely (background
scripts, heartbeats, another session running). This is not "no signal was produced" but **a signal
produced with no delivery point**: when the second path runs, there is no tool result to attach it to.

The fix is not "write another log line" but **find the signal a future delivery point**: σ is queued
per scope, carried away by that scope's next tool result, and **delivered exactly once**
(`parkSignal` / `takeSignals`, landing in the **durable** `SignalStore`; tests `TestEvictionSignalReachesNextToolResult` and
`TestEvictSignalOutlivesThePoolThatProducedIt` — the latter delivers through a **different pool instance**, the one cell an in-process queue could not cover).

So item 4 really asks:

> **When this change happened, was the premise "the agent is reading something" true?**
> If not (background task, eviction, cross-turn asynchrony) a delivery point must be designed
> explicitly, or the signal may as well not exist.

The same question applies to other subsystems: any state change that happens **between two model
calls** (background memory update, scheduled job, a change triggered by an external webhook) lands on
this item — and their common answer is "queue + a future delivery point", not "report it right now".

## 7. Relation to the other principles

| Principle | Relation |
|-----------|----------|
| **F1** [Cordis preconditions / zero migration](./07-formal-rootcause-and-fix.md) (07 part 2) | complementary: the precondition decides whether the mechanism **may act**; this principle (F2) decides whether the agent **knows** it acted; F3 then decides whether the knowing **arrives**. None is optional — preconditions alone become "refused but unstated", F2 alone becomes "stated but never delivered" (§6.1), F3 alone becomes "delivered but untrue". The index of all three is [00](./00-formal-systems.md) |
| the four mechanisms of [07 §3.11](./07-formal-rootcause-and-fix.md) | this principle applied to filesystem sync: write-through (the two copies agree at the moment of the write) + the memory-free version decision (the criterion for δ) + refuse + signal (the incident's only line of defence) + the exec change signal (the perception channel) |
| the "preserve a copy / choose-a-side tool" dropped from earlier versions | violates the spirit of C3: another layer of insurance for a change that was already announced is a duplicate mechanism (the three-shape comparison in 07 §3.11.1) |

## 8. In one sentence

> **A mechanism's correctness can be guaranteed by preconditions; the agent's correctness can only
> be guaranteed by observability.**
> What the incident destroyed was not just three files but the agent's knowledge of its world — and
> that knowledge is the premise of everything it does next.

## 9. Architecture decision: should there be a "unified state-observation mechanism"?

Reviewed against Clean Architecture's criteria (the question: **should a unified observation exit be
designed?**):

| Criterion | Observation | Verdict |
|-----------|-------------|---------|
| **Is the axis of change proven?** (CCP/SRP: same reason + same rate of change → keep together) | by 2026-09-18 the same seam had changed **6 times**: exec change signal, write-result signal, environment-change signal, unhydrated warning, clip marker, sandbox-replaced warning | **yes**, the boundary is proven by real change; the investment is justified |
| **Is the policy duplicated?** | the policy "when to attach to a tool result / when to queue / when to stay silent" was written once per mechanism — and **was missed once** (the idle-eviction signal was dropped, §6.1) | **yes**; what repeats is the policy, not the rendering |
| **Is an abstraction needed?** (YAGNI: with one implementation, do not invent an interface) | there are only two delivery channels: **the tool result** ("what happened to the thing I just called") and **the per-turn prompt** ("what happened while I was away") | only **one event type + two delivery points**; no bus/subscriber framework |

**Proposed shape** (decision record, not implemented):

1. **Unify δ, not the channel**: subsystems stop writing their own prose and instead produce one
   structured fact (the δ of §2.1: `{kind, paths, bytes, detail}`); **one** use case decides σ's
   delivery and wording;
2. **keep two delivery points** (tool result / per-turn prompt), because they answer different
   questions and merging them loses semantics;
3. **once the policy is centralized, durability must follow** (**landed 2026-09-18**): this item used to say
   "the queue is in-process; a signal must be either recomputable or persisted" — both halves are now done.
   The recomputable ones (refusals, sync failures) are **not queued at all**: the next sync derives them
   again. The ones that cannot be recomputed go through a **durable port** (a path an eviction wrote into
   the store) or ride the **call stack** (a rebuild, in the call that discovered it). The in-process queue
   is gone (see 09 §3, G3).

**Explicitly not doing** (the first two steps of Musk's algorithm):

- no general event bus / pub-sub / observer framework — there is exactly one consumer (the model),
  and an extra layer of indirection only turns "why did this notice not arrive?" into a harder question;
- no new storage: if persisting signals becomes unavoidable, reuse the existing scope-level document
  store rather than adding a table.

### 9.1 The final form of the criterion: **one exit per category**, no cross-package framework

The architectural question is not "is there one global exit?" but:

> **Perceivable by the agent; and for each category (interface/package), the observation exit is unique.**

The two goals live at different levels: the agent can only see its own context (tool results /
prompt / messages), which is the **consumer side**; "unique exit" is a **producer-side** discipline —
several mechanisms in one package must not each decide how to talk to the model, or the policy
duplicates and gets missed (§6.1 lost it exactly once).

The criterion carries one qualification inherited from §2.2: **an exit is a category's single delivery
point; the obligation to place belongs to the change side, the moment of taking to the consumer side**
("active" means the change side must not stay silent — it does not include interrupting reasoning in
progress; P1).

By that criterion this repo has converged to three categories with one exit each:

| Category (package/interface) | Single exit | How it is delivered | Status |
|------------------------------|------------|---------------------|--------|
| `internal/sandbox` (LifecyclePool) | `delta` (moved / blocked / problem) + `signalsFor(delta)`; `SignalStore` (durable) when no tool result can carry it | appended to that `exec` result; the next one across processes and replicas gets it too | ✅ 2026-09-18 (no in-process queue) |
| `internal/agent/tools` (workspace tools) | `Registry.workspaceSignalExit`: tools only `addSignal(ctx, δ)`, the exit decides placement (state marker first, this call's facts after) | appended/prefix to that tool result | ✅ converged 2026-09-18 |
| `internal/agent` (turn level) | `envTracker.signal` → `ContextBuilder.SetEnvironmentSignal`, appended to the end of the system prompt | once per turn | ✅ |

The discipline shared by all three: **tools/mechanisms only produce δ; placement and wording (σ)
belong to the exit**; nothing outside the exit builds its own string (`addSignal` warns instead of
silently dropping when it cannot reach the exit). That keeps "why was this sentence never delivered
to the agent?" a question with **one place to look per package**.
