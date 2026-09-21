# 12 · The formal design of leases: the contract induced from the existing implementations

> Status: induction (as-built evidence + design rules) · last verified: 2026-09-19
> Trigger: the cross-replica double-turn incident of 2026-09-18
> ([../session-turn-integrity.md](../session-turn-integrity.md)) — designing `session_turns` forced the
> question "what *is* a lease in this repository, formally?".
> The answer is not new: it is induced from the **four implementations already in the tree**
> (`channel_leases`, `rediscoord.Leaser`, `sandbox_leases`, `RedisRefreshLocker`), and one probe
> experiment falsified a claim that had been written into a code comment.
> Home: **F1's (preconditions / zero migration) mechanism layer** — a lease is how a precondition is
> manufactured, not a fourth formal system ([00 §1](./00-formal-systems.md)).
> Chinese edition: [`../文件系统形式化证明/12-lease-formal-design.md`](../文件系统形式化证明/12-lease-formal-design.md)

## 1. Why formalise it

This repository contains **four** lease mechanisms that do not know about each other, each of which
reinvented the same three things:

| Mechanism | Key | Value | Code |
|---|---|---|---|
| channel singleton (relational store, default) | `(channel, account_id)` | `holder_id` + `expires_at` | `internal/gateway/channels.go:16-30` → `internal/store/database.go:4975-5055` |
| channel singleton (Redis, optional) | same | holder value + TTL, Lua-checked | `internal/rediscoord/lease.go:28-82` |
| sandbox instance lease | `scope_key` | `owner` + instance identity + `expires_at` + `epoch` | `internal/store/sandbox_leases.go` |
| MCP OAuth refresh mutex (Redis, optional) | oauth store key | the constant `"1"` + TTL | `internal/mcp/oauth/adapter/redis_locker.go` |

Each of them **works** (it is sufficient for its own scenario), but the reason it works was never
written down; so when a fifth one (`session_turns`) had to be added, there was no criterion saying
which clauses are mandatory and which are droppable. This document supplies that criterion and aligns
it with the existing formalisation of F1/F2/F3.

## 2. Definition: a lease is a **precondition witness** in shared storage

Fix a **key** `κ` (the identity of the guarded resource), a **holder** `h`, a **fencing token** `e`
(an integer) and an **expiry** `τ`. One row in shared storage is one lease:

```
L(κ) = (h, e, τ)          live(L(κ), now)  ≜  L(κ) ≠ ⊥ ∧ τ > now
```

Three operations (`Acquire` / `Renew` / `Release`) and one read (`Get`). The guarded action is
`A(κ)` — it can be a **continuous** exclusivity (a long-poll loop, a turn) or a **discrete effect**
(a destroy request, an append to the transcript).

**It is not a lock.** A lock's semantics are maintained by the holder ("I am holding it"); a lease's
semantics are maintained by **the store** ("this row says who holds it, until when"). The predicate is
always evaluated on the store's side — that is the ground for L4/L5 below, and the reason all three
existing implementations put the truth in the row.

## 3. Obligations L1–L7

| # | Obligation | Statement | Cost of violating it |
|---|---|---|---|
| **L1** | exclusion | per `κ`, at most one `live` row at any time; `Acquire` is a CAS (not read-decide-write) | two holders execute `A` at once |
| **L2** | attribution | the row records `h`, and `h` is attributable (into logs, into signals) | nobody can answer "who wrote this"; "same holder re-entering" is indistinguishable from "a different one" |
| **L3** | freshness | `τ` is the **only** invalidation channel (natural expiry or voluntary release). Corollary: `TTL > sup(duration(A))` is a **safety precondition of A, not a performance knob** | a live `A` is declared dead: a second holder enters and both write |
| **L4** | fencing | (a) `A`'s effect **carries** the token and the **resource** validates it in the same atomic step; (b) what is validated is the **pair** `(h, e)` against the live row; (c) that **pair** must be unique per acquisition — either `h` embeds a one-shot nonce, or `e` is strictly monotonic over the **whole life of the row** | a delayed effect lands on a newer generation's resource (the classic "paused writer wakes and overwrites") |
| **L5** | guarded release | `Release`/takeover carry the same `(h, e)` predicate; a release without one is a hole | a lapsed holder's late cleanup deletes the **current** holder's lease |
| **L6** | verdict observability | the result of `Acquire`, the failure of `Renew`, every loss is a σ (F2) and must be delivered (F3) | the consumer reads silence as "I am alone" ([08 §2.2](./08-state-observability-principle.md), P1′) |
| **L7** | preconditions must be evaluable | a write's precondition must be evaluable **at the effect's linearization point**, and its **witness must be co-located with the actor performing the effect** (precondition: the witness must be **evaluable** — the store holding it must be reachable; see the condition (c) in §3.2). Holds ⇒ conditional write (family B); does not hold ⇒ client-side comparison (family A), and that must be **declared as detection, not prevention** | the predicate is evaluated on a stale observation: when the conflict is missed the mechanism migrates while `pre` is false (**R2 fails silently**) and emits **no σ at all** (O1 fails in the silence direction — the hardest kind to notice) |

### 3.1 What L7 implies: how the eight file-change seams should choose a family (W1–W8; the heading said "seven" until 2026-09-20, corrected against the table)

> L4(a) says the **lease's** token must be checked by the resource in the same atomic step; L7 generalises it — **every** write's precondition obeys the same rule:
> **the witness must be in the hands of whoever performs the effect.** In hand ⇒ family B is free; only in-band inside a copy ⇒ family A is free.
>
> **Why "manufacture a consistent fact" does not fix it** (do not read this as "such a fact cannot be built":
> **a database lease *is* exactly such a cross-replica consistent independent fact** — one row per scope, CAS, cheap).
> Whether you can buy **prevention** depends on two things:
>
> | Condition | Satisfied (prevention is buyable) | Not satisfied (you only buy detection) |
> |---|---|---|
> | **(a) the resource can check it inside the same atomic step** (= L4a) | row CAS in a database; an object store's conditional PUT on ETag | a POSIX file inside a container: no conditional write, no atomicity between the check and the write |
> | **(b) that resource has a chokepoint every writer must pass** | the storage API is the only entry | a container is not: the agent's `exec`, scripts, snapshot unpacking all bypass the mediator |
>
> Both satisfied ⇒ one round trip buys prevention; either missing ⇒ whatever fact you build, you only buy **detection**
> (which L7 requires you to **declare as detection**). W2/W3 sit in family A because they fail these two conditions,
> not because "nothing could be thought up". A further cost: even if you build that fact, it must be born and die with
> the container's lifecycle (rewritten on every hydrate, every eviction) — and it describes precisely the thing that can be bypassed.

| seam | where the witness is | consequence of all-A | consequence of all-B | answer |
|---|---|---|---|---|
| **W1** tool→store (write_file / edit_file / apply_patch) | **in hand** (the tool just read the object) | a window; and `write_file` is blind ⇒ its precondition is the **empty set** (unfenced) | **free and exact** (+1 HEAD, 0 bytes) | **B** (B1–B11) |
| **W2** store→sandbox mirror (write-through) | **in-band**: the sandbox copy's mtime is the delivery stamp | stamp + byte fallback; adequate (a same-second, same-size edit is missed) | the ETag has to travel into the sandbox ⇒ a durable manifest or a sidecar | **A** |
| **W3** sandbox→store write-back (T1's reconcile) | **only in the in-band stamp** | as W2 (a narrow window) | **the "which version does the sandbox hold" memory must be rebuilt** ⇒ back to pre-09-18; a wider refusal surface livelocks (07:520) | **A** (+ an optional one-line tightening) |
| **W4** panel upload/delete | **in hand** (it just listed them) | a window | **free** | **B** (B11) |
| **W5** attachments | none (a new file) | can only mean "must not exist" | same semantics, cheap | **B** (`VersionAbsent`) |
| **W6** skills publish | none (overwriting is the **intent**) | no predicate | no predicate | **neither** — declare the posture |
| **W7** hydrate | it **produces** the witness (writes the stamp) | the stamp is the witness ✅ | the witness has to be exported into the sandbox | **A**'s stamp is the answer |
| **W8** Move | the destination must be empty | the server already does it B-shaped (non-atomic on S3) | same | **B**, with "non-atomic" written into the strength table |

**Conclusion**: **both "all A" and "all B" are strictly worse than this partition.** All-A downgrades W1/W4 from a closed window to
an open one and buys nothing (B is free in those two cells); all-B, to give W2/W3 a witness, must introduce metadata that itself needs to be
consistent across replicas, and it brings back the livelock shape recorded at 07:520 (R3 fails to converge).

**L4(c) is the one demand this round added, and it was measured into existence**: the other five all
have instances in the existing code; only this one was forced out by the sandbox lease's observed
behaviour while designing `session_turns` (§5).

### 3.2 The degradation path (verdict on gap-register #2): when the lease store is unavailable, today the system **lets it through**

> Source: row 2 of the gap register in *认知哲学的数学原理* (The Mathematical Principles of Cognitive Philosophy) 19.5.8.7
> ("the degradation path when the database is unavailable **has not been judged cell by cell**"). This section is that verdict.
> **Evidence date: 2026-09-21.**

**As-built evidence (the status quo).** The sandbox pool is fail-open on **every** lease **admission** read path:

| Site | Failure | What the code does |
|---|---|---|
| `internal/sandbox/e2b_executor.go:2182` | `AcquireSandboxLease` errors | `slog.Warn("e2b lease acquire failed (keeping local sandbox)")` and **keeps using the local sandbox** |
| `:2434-2435` | `GetSandboxLease` errors | comment verbatim: `Registry unavailable: fail open on the local executor.` |
| `:2445` | reclaim after expiry errors | keeps the local executor |
| `:2489` | a rebuilt sandbox was never published | keeps the local executor |

And this is a **contract pinned by a test**: `internal/sandbox/lease_pool_test.go:415`
`TestE2BPoolFreshGetLeaseErrorsFailOpen`, whose comment reads
`Fail-open contract (design doc: "Registry errors fail open: the sandbox is left alive")`.

**Which kind of cell this is.** The witness (the lease row) **lives in the DB**; the effect (handing out / reusing a sandbox) happens on a pod.
While the DB is reachable this cell is family B with (a)(b) both satisfied — exactly what §3.1 means by "a database lease is cheap".
When the DB is **unreachable**, what breaks is not "the witness is not in hand" but **the witness cannot be evaluated at all**:
two worlds ("nobody holds it" vs "someone holds it but we cannot see the row") produce **the same observation** (an error).
So this cell is not ③ (impossible), it is **②’s sibling: undecidable**.

**Hence a third implicit condition on L7.** L7’s first two conditions (atomic check + chokepoint) are about **where the witness is**;
this section adds the third, which is about **whether the witness can be evaluated**:

> **(c) The witness must be evaluable** (the store holding it must be reachable).
> When (a)(b) hold but (c) does not, the predicate is **not evaluable** — "carry on" and "refuse" are then not decided by the witness
> but are an **availability choice**, and therefore must be **declared**.

**Walking 19.4.6.1’s three actions.**
① Where is the witness? — In the DB, and **out of reach** right now. ② Can it be relocated? — This system’s only chokepoint is the DB
(a sandbox instance in object storage has no conditional-write semantics), so **no**. ③ Neither works ⇒ **hold back + label**.
**The current implementation takes a fourth option: let it through, and say nothing** — the ✗ "silently degrading" entry in 19.4.6.1’s negative list.

**Two landable paths (pick one; either way it must be named).**

- **Path A (hold back)**: a lease that cannot be read ⇒ **do not hand out, do not reuse** a writable sandbox; refuse this turn.
  Cost: DB jitter = a failed turn (availability). Benefit: two writers cannot appear inside that window.
- **Path B (let it through + declare)**: keep fail-open, but **produce a σ** — one true sentence, e.g.
  "this turn did not obtain cross-replica admission: the lease store was unreachable, so there may be two writers" — and **place it at `D₁` or `D₂`**
  (an in-flight call takes `D₁`, the tool result; otherwise `D₂`, the turn entry; if it cannot be taken, design the future delivery point explicitly per 08 §6.1).
  At the same time, declare this cell’s admission **as let-through** in the strength table (neither prevention nor detection).

**Why Path B is this section’s recommendation.** It costs **nothing** (one true sentence) and yet extends A1’s discipline from the refusal direction
to the **let-through direction** — **refusal ≠ silence, and letting it through ≠ silence either**. Its output also lands in machinery that already exists:
no new table, no extra round trip.

**Boundaries (all four must be written together).**

1. **Not every fail-open is the same thing.** `:2609` in the same file (`ReleaseSandboxLease` errors ⇒ **leave the sandbox alive**) points the other way:
   that is the conservative choice that **avoids irreversible damage**, it **does not violate L7**, and it must not be changed along with the rest.
   So "fail-open" needs **two senses here**: **admission-side fail-open (let it through)** and **lifecycle-side fail-open (do not destroy)** —
   one word carrying two opposite meanings inside one file, a new member of the single-source / one-expression family (same shape as G23 / G16).
2. **When the DB is entirely unavailable**, the turn itself cannot start and σ’s landing site may be gone too ⇒ that cell degrades into "② no landing site",
   where R4 / P1′ requires **designing the future delivery point** (a later `D₂` report), not pretending it was said.
3. **This section judges; it does not build.** No code was changed; the **G31** row in `10 §4` is a registration, not a fix.
4. **`releaseExecutor` has a third branch that fits neither sense above (added 2026-09-21; see G32).**
   The `!deleted` branch at `:2615` is **not** "the store errored" (that is `:2609`) — it is **the store successfully answering "no row matches the key you gave"**.
   When the pool’s **in-process epoch mirror** (`internal/sandbox/e2b_executor.go:1868` `leaseEpochs`) holds no epoch for that scope,
   the release presents `epoch=0`, `DELETE ... AND epoch = ?` matches no row, and the code reads that `false` as
   "another holder fenced me out" — **while in the other world (the truth) the row still names the releasing pod itself**.
   See G32 for the controlled experiment (`internal/sandbox/lease_epoch_gap_test.go`: returns `nil`, destroys **0**, logs **0** lines,
   **observationally indistinguishable** from the release that a sibling correctly fenced out). ⇒ That branch is **not** the conservative choice;
   it is **a release silently dropped**, and by this section’s own discipline (letting it through must be declared) it must at least produce a σ.
   **This section does not pick the path** — it is registered as **G32** in `10 §4`.

## 4. The existing implementations vs the six obligations (as-built)

| | `channel_leases` | `redis` channel lease | `sandbox_leases` | `session_turns` (design) |
|---|---|---|---|---|
| L1 exclusion | ✅ `ON CONFLICT … WHERE expires_at < now OR holder_id = me` (`database.go:4986-4996`) | ✅ `SETNX` + renew Lua | ✅ one `UPDATE` to claim + `INSERT … DO NOTHING` + read-back (`sandbox_leases.go:52-101`) | ✅ the `channel_leases` shape + `epoch = epoch + 1` |
| L2 attribution | ✅ `holder_id` | ✅ value = holderID | ⚠️ `owner = host:pid` (repeatable, see §5) | ✅ `holder = <pod>/<uuid>` (unique per acquisition) |
| L3 freshness | ✅ TTL 30s / renew 10s (`internal/channels/lease.go:32-34`) | ✅ same | ✅ TTL 15m > one tool call (`internal/sandbox/lease.go:134`) | ✅ TTL = turn budget 45m + 60s grace, renewed on a timer at TTL/3 |
| L4 fencing | — (the guarded action is **continuous**: a failed renew cancels the ctx; there is no "delayed discrete effect") | — same | ✅ **fixed (2026-09-19, G25)**: the claim branch is `epoch = epoch + 1`, so the token is unique per acquisition (`sandbox_leases.go:69-80`; UT `TestSandboxLeaseEpochNeverResetsAcrossTakeover`) | ✅ strictly monotonic + `(holder, epoch)` unique |
| L5 guarded release | ✅ `WHERE … AND holder_id = ?` (`:5044-5051`) | ✅ the Lua compares the value | ✅ `WHERE scope_key = ? AND owner = ? AND epoch = ?` (`:224-235`), but weakened by L4's reset | ✅ same shape |
| L6 verdict observability | ⚠️ the loser just retries (channels are silent infrastructure; acceptable) | ⚠️ same | ⚠️ only some facts ride the row (`unhydrated`, `state`) | ✅ `turnActive{holder, epoch, expiresAt}` + `queued{holder, ETA}` (A4.1) |

Beyond that fourth row there is one more **family-level inconsistency**, the same shape as this
directory's "single source" family: `RedisRefreshLocker`'s comment says "Release drops the lock
(guarded by the token value)", but the code is `l.Client.SetNX(ctx, key, "1", ttl)` +
`l.Client.Del(ctx, key)` — the value is always `"1"` and the release is an **unconditional DEL**
(`redis_locker.go:29-36`). It is dormant today (Redis is off in both environments,
[session-turn-integrity A1.1](../session-turn-integrity.md)), but it shows that **L5 does not grow
naturally — it has to be written down**.

## 5. The counterexample (G25): the sandbox lease's fencing token resets to 1 each generation — **fixed 2026-09-19**

`AcquireSandboxLease`'s two write statements hard-code `epoch` to `1`:

```sql
-- claiming an expired row (sandbox_leases.go:69-76; epoch = 1 is :72)
UPDATE sandbox_leases SET owner = ?, sandbox_id = ?, …, epoch = 1, …
WHERE scope_key = ? AND expires_at <= ?
-- the first insert (:80-87)
INSERT INTO sandbox_leases (…, epoch, …) VALUES (…, 1, …)
-- only renew / replace increment (:120-124 / :153-160)
UPDATE sandbox_leases SET …, epoch = epoch + 1, … WHERE …
```

So `epoch` means "how many renewals **this possession** received", not "which generation of this row
this is". Two existing documents **contradict each other** here: `docs/sandbox-pool-leases.md:87-89`
is honest — "`epoch` is monotonic within a lease cycle only … a delayed destroy from an earlier cycle
by the same owner is not covered" — while the same file's `:237-240` says "The version column makes any
stale destroy request fail closed". **The probe experiment rules the latter false** (a temporary test,
deleted after the run):

```
gen1 acquire(insert): sandbox=sb-1 epoch=1
gen1 renew: epoch=2 then epoch=3
gen2 acquire after expiry, SAME owner: epoch=1
gen3 acquire after expiry, DIFFERENT owner: epoch=1
stale gen1 release(pod-a, epoch=1) against gen3 row: released=false      ← the owner changed; it held

# the dangerous shape (the second probe): the same owner string across generations
gen2 acquire: sandbox=sb-2 epoch=1
stale gen1 release(pod-a, epoch=1) against gen2 row: released=true      ← a live row of the new generation is deleted
after the stale release, the row is: <nil> (err=<nil>)
```

**Verdict**: this violates **L4(c)** (a repeatable `h` **and** a reset `e` ⇒ `(h, e)` is not unique per
acquisition). The conditions are "the same `host:pid` re-acquires the same scope after the previous
generation expired, and the latecomer's token happens to equal the new generation's current value".
Today's callers pass the epoch they last received (`e2b_executor.go:2601-2608`), so the real window is
narrow — **but the guarantee the comment claims ("any stale destroy fails closed") does not hold**, and
the fix is one clause:

```sql
-- the claim branch increments; the insert branch keeps 1 (a first row has no predecessor)
UPDATE sandbox_leases SET …, epoch = epoch + 1, … WHERE scope_key = ? AND expires_at <= ?
```

**Landed (2026-09-19)**: the claim branch is now `epoch = epoch + 1` (`internal/store/sandbox_leases.go:69-80`),
and `TestSandboxLeaseEpochNeverResetsAcrossTakeover` (`internal/store/sandbox_leases_test.go`) pins
"strictly increasing across two takeovers + a stale release refused + the live row still there".
It changes no existing test assertion (the old `sandbox_leases_test.go:185/202` assertions are on the
**insert** branch's 1). **Falsification run for real**: restoring `epoch = 1` ⇒ `gen1=1 gen2=1` fails.

> The value of this counterexample is not the sandbox itself (the `owner` column already absorbs most of
> the damage) but that it proves L4(c) is **not dogma**: a token that "starts at 1 every generation",
> paired with a repeatable holder identity, is no longer a fence.

## 6. Instantiating it for `session_turns` (design rule → concrete value)

| Obligation | `session_turns`' value |
|---|---|
| key `κ` | `(user_id, agent_id, session_key)` — **copied from the guarded resource's key** (the `sessions` primary key, `internal/store/database.go:1721`) |
| holder `h` | `<pod>/<uuid>`, **freshly generated on every `Acquire`** ⇒ L4(c) holds by construction (even if the row is deleted and recreated, the pair `(h,e)` cannot repeat) |
| token `e` | strictly monotonic inside the row (`epoch = epoch + 1`, including the claim branch), never reset |
| expiry `τ` | 45m (the turn budget) + 60s, renewed by the turn's own goroutine at TTL/3 (not the sandbox's activity-driven renewal: a turn can sit silently inside one `delegate_task` for 605 seconds — see the incident) |
| fence point (L4a) | **the write statement itself**: `AppendSessionMessageFenced` / `SaveSessionFenced` carry `(h, e)` and the statement has `EXISTS (SELECT 1 FROM session_turns WHERE … holder_id = ? AND epoch = ? AND expires_at > now)`; 0 rows ⇒ `ErrSessionFenceLost`. **Not "check then write"** — that has a TOCTOU window, i.e. it puts the fence in the caller. Type ownership: the port names `session.TurnFence`, the adapter translates it into `store.SessionFence` (no inner interface names an outer type) |

> **L4a's precondition, made explicit 2026-09-19 (why the fence cannot ride a context value).**
> "Carry `(h, e)` into the statement" is only enforceable if a *missing* fence is distinguishable from
> *an absent obligation*. A context value cannot distinguish them — "no fence" and "the caller forgot the
> fence" are the same thing at the type level — so the fence must travel in the write's signature, where
> `nil` is an explicit statement ("no lease governs this write"). Everything of that family then rides one
> value (`session.WriteScope`), and the *refusal* is owned the same way: `session.ErrSessionFenceLost` is
> the inner package's, translated by the adapter from the store's sentinel. Judgement for reuse: **within
> one family of facts, the strongest obligation decides the transport** (refusal > record), age only
> tie-breaks. Recorded with witnesses in
> [../session-turn-integrity.md](../session-turn-integrity.md) §A1.4a.
| release (L5) | `DELETE … WHERE holder = ? AND epoch = ?` |
| verdict (L6) | `queued{holder, ETA}` (the loser learns who blocks it and for how long) + `turnActive` on the subscription/history payload (A4.1) |

**Boundary (written down, not pretended away)**: the lease serialises two turns on the same
`session_key`, but it does **not** serialise the minting of that key. When the first message of an IM
conversation arrives at two replicas at once, `resolveOrMintKey`
(`internal/session/manager.go:294-304`) mints a random key on each ⇒ two sessions, two leases. Web (the
incident's path) uses key == `chatID`, which is deterministic, so it is outside this round; that is
another entrance to the "two writers" family, the same family as G24, and it is collected when a real
shape of it shows up.

## 7. Four-layer placement (Clean Architecture)

| Layer | This family's landing point | Dependency direction |
|---|---|---|
| **Entities** | the invariant itself ("at most one turn per session is writing") + the key's identity `SessionKey` (= the session's identity, not the transport's) | depends on nothing; knows neither SQL nor Redis |
| **Use Cases** | admission policy: who queues, who is refused outright (`TurnStartOrQueue` / `TurnStartIfIdle`, `internal/agent/admission.go`), the turn lifecycle | defines the port `SessionLease`, **owned by the inner layer** |
| **Interface Adapters** | assembling a port implementation: `storeLeaser`'s sibling (`internal/gateway/channels.go:16-30`); `session/store_adapter.go` is the **only** place that knows both `session.TurnFence` and `store.SessionFence` (the translation boundary) | implements an interface the inner layer defines (DIP); knows no routes or HTTP |
| **Frameworks & Drivers** | PostgreSQL / SQLite DDL and CAS statements (`internal/store`), Redis (optional), the in-process `NopSessionLease` (single instance) | outermost; replaceable |

Two corollaries, both taken from existing code rather than preference:

1. **The inner layer owns the port's definition**: `channels.Leaser` is defined in the consumer package
   `internal/channels`, the SQL lives in `internal/store`, the adapter in the composition root
   `internal/gateway`. `session_turns` copies that: the port in `internal/agent` (the consumer), the
   implementation in `internal/store`, the wiring in `internal/gateway`
   (`agent.WithSessionLease`, a sibling of `WithSessionStore`, `internal/agent/manager.go:86-150`).
2. **The fence must live in the outermost layer**: L4(a) demands that "the resource validates in the
   same atomic step". Moving the check into Entities or a Use Case degrades it to "check then write".
   That is not layer hygiene — it is that the fence semantics and the storage semantics **must have one
   source**.

## 8. How it composes with F1/F2/F3

```
the lease solves an F1 problem: who may perform this migration ("start a turn" is a migration)
   L1/L3/L5  ⇒ the precondition holds
   L4        ⇒ the precondition still holds at the moment the effect lands (fencing = compensation for the precondition's delay)
the lease's verdict is F2's δ: acquire succeeded/failed, a renew was lost, a takeover happened
   that δ needs a σ (L6): the user sees queued{holder, ETA}; the agent learns "your turn was taken over"
the σ's arrival is F3: the queue event rides the existing exit; a mid-turn takeover rides the tool result / turn prompt
```

In one line: **a lease is F1's mechanism, its verdict is F2's material, and its delivery is F3's
obligation** — so there is no "fourth lease system" among the three formal systems, only one
repeatedly-reimplemented mechanism of F1.

## 9. Evidence index

| Claim | Evidence |
|---|---|
| the channel lease has no token; its release is holder-guarded | `internal/store/database.go:4975-5055`; probe output `channel_leases columns: [channel account_id holder_id expires_at]` |
| the Redis channel lease = `SETNX` + Lua | `internal/rediscoord/lease.go:28-97` |
| `RedisRefreshLocker`'s release is an unconditional DEL | `internal/mcp/oauth/adapter/redis_locker.go:38-45` |
| the sandbox token resets each generation; a late release deletes a live row | probes `TestProbeSandboxLeaseEpochOnTakeover` / `TestProbeSandboxLeaseEpochResetFenceCollision` (run for real on 2026-09-19, output in §5; the probe file was deleted afterwards). **The fix and its witness**: `internal/store/sandbox_leases.go`'s claim branch + `TestSandboxLeaseEpochNeverResetsAcrossTakeover` (falsification run for real) |
| the sandbox doc contradicts itself | `docs/sandbox-pool-leases.md:87-89` vs `:237-240` |
| the turn lease's values | [../session-turn-integrity.md](../session-turn-integrity.md) A1.1–A1.5 |
| **when the lease store is unavailable: let through, or hold back (§3.2 / G31)** | `internal/sandbox/e2b_executor.go:2182` (an acquire error keeps the local sandbox), `:2434-2435` (a get error; comment verbatim `fail open`), `:2445` (reclaim error), `:2489` (rebuild never published), `:2609` (release error keeps it alive — **the opposite direction, and correct**); test `internal/sandbox/lease_pool_test.go:415` `TestE2BPoolFreshGetLeaseErrorsFailOpen` (**evidence taken 2026-09-21**) |
| **a release with no recorded epoch is silently dropped (§3.2 boundary 4 / G32; the measured P-WAD row #1 of gap-register #5)** | carrier `internal/sandbox/e2b_executor.go:1868` `leaseEpochs` (write `:2040`, read `:2111`, use `:2607`), **the drop point `:2615`** (`!deleted` ⇒ `return nil`, no error, no log), the issuance-time fail-open comment `:2215-2216`, the SQL predicate `internal/store/sandbox_leases.go:237`; controlled experiment `internal/sandbox/lease_epoch_gap_test.go` `TestPWAD1_ReleaseWithNoRecordedEpochIsSilent` (**evidence taken 2026-09-21**: `err=nil` / destroys 0 / logs 0, item-by-item identical to a correctly fenced-out release; putting the σ probe back turns it red — measured) |

**Witness status**:
- **landed**: L1 one winner under concurrency (`TestSessionLeaseConcurrentAcquireHasOneWinner`), L4(c) the
token never resetting (`TestSandboxLeaseEpochNeverResetsAcrossTakeover`, sandbox side; the turn side
follows A1), L5 a late release refused (`TestSessionLeaseAcquireRenewReleaseAndTakeover`) — all in
`internal/store`.
- **landed (2026-09-19/20)**: L6's queue payload carries holder + `expiresAt` (`internal/agent/turnlease.go:163`), with the client half X7 done. **Corrected 2026-09-21**: this bullet used to name `TestCancelledTurnStopsAndSignalsOnce` as "the live cross-replica e2e" — it is not one, and never was: its peer stamp is a fake lease in-process. The two-replica pair now exists (`internal/setup/cross_replica_turn_e2e_test.go`: one store, two servers, the lease wired on both — admission *and* a cancel issued from the replica that is not running the turn), each with its falsification run; see [11 §12](./11-change-register.md) row 32. The line that remains open is the *previous* one:
L1/L4/L5 (with A1) — written down in [11 §12](./11-change-register.md).
