# 09 · Audit: the sandbox's full lifecycle and its interaction with the filesystem

> Status: audit (2026-09-18) · Method: the formal model of [07](./07-formal-rootcause-and-fix.md)
> and the observability principle of [08](./08-state-observability-principle.md), applied cell by
> cell to every state transition of a sandbox from creation to destruction and to its interaction
> with the filesystem (store / `/workspace`).
> Evidence: code + 18 hours of production logs (8 create/hydrate, 9 binds, 1 cross-pod adopt,
> 1 rebuild, 18 pauses, 216 renewals, 2 closes, 64 snapshot failures).

## 1. States and transitions (as-built)

A sandbox only ever has 7 states, and the transitions are driven by **three classes of trigger** —
which is the point of the audit: **the trigger is often not the agent**.

| # | State | Triggered by | What it does to the filesystem | What it does to the store |
|---|-------|--------------|--------------------------------|---------------------------|
| S1 | does not exist (no instance) | — | — | — |
| S2 | being created (provisioned / bound) | the agent's first tool call (lazy), or a rebuild before eviction | — | — |
| S3 | hydrated (`/workspace` = a snapshot of the store) | inside the creation flow | store → `/workspace` (including `/skills`) | read-only |
| S4 | in use (exec / file tools) | the agent | exec rewrites `/workspace`; a host write also writes through into `/workspace` | host writes land in the store directly |
| S5 | syncing (post-exec / evict) | **the harness** (after every exec, before every eviction) | reads a snapshot of `/workspace` | writes sandbox changes back (or refuses) |
| S6 | asleep (paused / sleeping) | **the harness** (a 10-minute idle sweeper) | the filesystem and processes are frozen in place | the sync already ran once |
| S7 | destroyed (expired / closed / release) | **a provider timeout** or **harness eviction/close** | the whole filesystem disappears | — |

**Cross-replica**: `sandbox_leases` lets an S6 instance be **adopted by another pod** (the logs say
`adopted from shared lease`), so "who handles this turn" and "who created this sandbox" can differ.

## 2. Observability verdict per transition

| Transition | The fact that changed | Agent-side signal | Verdict |
|------------|----------------------|-------------------|---------|
| S1→S3 first creation + hydrate | `/workspace` appears (content = the store) | no signal needed (the agent has only ever seen an empty world) | ✅ |
| S3 hydrate **failed** | `/workspace` may be empty | `workspaceUnhydratedSignal` (pre-existing) | ✅ |
| S4 host write + write-through succeeded | the two copies agree | the write result (only mentions a replaced version if one existed) | ✅ |
| S4 write-through failed (sandbox unreachable/rebuilt) | the store has been written, the sandbox has not | write result `[workspace]` (pre-existing) | ✅ |
| S5 sync writes back | sandbox changes enter the store | `exec` result `[workspace] the sandbox changed …` | ✅ added in this round |
| S5 sync **refused** | the two copies differ | `exec` result `[workspace] NOT synced …` | ✅ added in this round |
| S5 sync **could not run** (snapshot over the cap) | sandbox changes did not reach the store | `[workspace] … could NOT be synced …` | ✅ added in this round (64 times in production) |
| S5 sync happened during **idle eviction** (no tool result to attach it to) | as above | the part that must be carried goes into the **durable** `SignalStore`; the next tool result collects it | ✅ 2026-09-18 (was ⚠️ G3) |
| S6 sleep | content unchanged (frozen) | no signal needed | ✅ |
| S6→S4 wake | content unchanged (same filesystem) | no signal needed | ✅ |
| S6 **adopted by another pod** | content unchanged | no signal needed; an undelivered signal now waits in the **durable** carrier and the adopter collects it | ✅ 2026-09-18 (was ⚠️ G3) |
| S7 destroyed by **harness eviction** | unsynced content disappears | the sync was attempted before eviction; on failure the S5 "sync could not run" signal is emitted | ⚠️ see G1 |
| S7 destroyed by a **provider timeout** | unsynced content disappears | `[workspace] the sandbox was REPLACED …` | ✅ 2026-09-18 (was ❌ G1) |
| S3 rebuilt, re-hydrated from the store | `/workspace` is overwritten by the store | as above (set at rebuild time, delivered once on the next tool call) | ✅ 2026-09-18 (was ❌ G2) |
| a file **deleted** inside the sandbox | the store still has it, the sandbox does not | **no signal** | ❌ **G4** |

## 3. The four gaps

### G1 (fixed): a timeout used to discard unsynced content silently

```
sandbox in use (S4) → the changes exist only in /workspace
   ├─ normal path: the idle sweeper syncs first, then sleeps (S6)      ✅
   └─ abnormal path: the sync fails (snapshot over the cap), or the sandbox expires before the sweeper runs
        → the provider destroys the instance outright (S7)
        → the next call sees "expired, recreating" and re-hydrates from the store
        → whatever lived only in the sandbox is gone for good, and nobody is told
```

Production evidence: 64 snapshot failures (the sync never completed throughout that window) plus one
`expired, recreating`. Same family as the incident: **a change happened and left no trace in the
agent's world**.

### G2 (fixed, same mechanism as G1): the rebuild itself used to be silent

After a successful rebuild the next `exec` **succeeds normally** — the agent has no way to know its
`/workspace` was just overwritten from the store and that unsynced changes may have vanished in the
meantime. The pre-existing `[sandbox replaced: …]` only covered the path where the instance was
unusable and the command failed.

### G3 (fixed, 2026-09-18): the signal queue used to be in-process and was not a delivery guarantee

`pendingSignals` lives in pod memory. If a signal produced by idle eviction meets **a pod restart** or
**another pod adopting the scope** before delivery, the signal is gone — the same lesson as removing
the baseline for cross-replica reasons: **in-process state is not a guarantee**.

**Landing (2026-09-18)**: the formal rule is 08 §2.2 (O4, "no loss"). First split the three kinds of fact the queue was mixing, and give each
exactly the carrier it needs — "make them recomputable" turned out to be true for only two of the three:

| Fact in the queue | Recomputable? | What happens now |
|-------------------|---------------|------------------|
| **blocked** (paths the sync refused) | ✅ yes: a refusal **changed neither side**, so the next sync sees the same two copies and says the same thing | **not queued**. The next post-exec sync derives it (which also removes the old double delivery: one queued at eviction, one re-derived by the next sync) |
| **problem** (the sync could not run, e.g. over the snapshot cap) | ✅ yes: the cause is still there, the next sync fails again | **not queued**, as above |
| **moved** (paths the eviction sync just wrote into the store) | ❌ no: the write **erased its own evidence**; afterwards the two copies agree and nothing can derive it again | **a durable carrier**: the `sandbox.SignalStore` port (`parkSignal` writes, `takeSignals` reads and clears), implemented by `gateway.sandboxSignalStore` as a scope-keyed `configs_kv` row (`kind=ws_signal`, isomorphic to the `mcp_undo` cursor) |
| **the sandbox was replaced** (one-shot) | ❌ no: `TakeWorkspaceReplaced()` clears on read | **the call stack**: after `getInner`, the call that discovered it renders the note into its own result (`takeReplacedNote`). A call that fails does not consume it, leaving it for the next call that can carry it |

So `pendingSignals` / `addSignal` / `drainSignals` and the map behind them are **deleted**: the sandbox
package holds no delivery state in process memory any more. Costs and boundaries, recorded honestly:

1. the durable carrier costs one scope-level KV read/write, but **only when an eviction sync actually wrote
   something** (rare); the read happens at the next exec's sync point (which already does a tar + find + N stats);
2. a runtime with no carrier wired (local/CLI without the relational store) **logs the signal and warns**
   instead of pretending it will be delivered — "no delivery point, no claim";
3. `AppendSignal` is a read-modify-write: under an unlikely race it can duplicate a sentence, never lose one
   (the same side of the trade the reconcile takes).

### G4 (fixed on 2026-09-18 as far as "the fact is stated"): a sandbox-side deletion cannot be detected (introduced when the baseline was removed)

"The sandbox deleted it" and "this file only ever existed in the store (an upload)" are
indistinguishable — **that has not changed**, because the two leave the identical trace. What changed is
that the trace used to be **unexamined**.

**Landing (2026-09-18)**: the walk's domain is the **sandbox snapshot**, so "the store has it, the
sandbox does not" sits outside it. Each sync now lists the store once and reports the difference as a
third shape of δ (`storeOnly`), rendered with the shared sentence `sandbox.StoreOnlyLine` (the same one
`list_dir` uses for G7a):

```
[workspace] N path(s) are in the workspace store but NOT in this sandbox, so anything run with exec will
not find them: … — either they were added to the store after this sandbox started (an upload), or they
were deleted inside it; this runtime cannot tell which. read_file still sees them; if a script needs one,
read it and write it again.
```

**Attribution is still impossible, and explicitly out of scope**: telling "the sandbox deleted it" from
"it was uploaded later" needs a **durable store-side manifest**. Its only remaining value is "who did
it" — what the agent needs is "exec cannot see these", and that is now stated. Cost: one extra `List` per
sync (the same order as the existing tar + find + N stats), with the `skills/` namespace skipped
explicitly (it belongs to the read-only `/skills` mount, not `/workspace`; otherwise every agent-scope
sandbox would report every installed skill as missing).

## 4. Two cautions

**A. A signal may arrive later than the fact.** The G1/G2 signals can only be delivered on the next
tool call, by which time the rebuild has already happened. That does not violate the principle —
perceivable is required, not real-time — but the wording must say **it already happened**, not "it is
about to happen".

**B. `/workspace`'s size cap is also a capability cap.** The 32 MiB snapshot cap disables all of S5
(every sync fails). That is not an observability problem, but it **creates** G1's precondition. The
system prompt already asks for large files to live in `/tmp`, and G1's signal has to point at that too.

## 5. Formal cross-check (the four trigger conditions of 07 §2.4)

| Condition | Where it lives in the lifecycle | Status today |
|-----------|--------------------------------|--------------|
| a separated backend (two copies) | from S3 on | inherent |
| the path exists in the sandbox | created by the S3 hydrate / an S4 exec | inherent |
| the host changed the store and the two copies differ | S4 host write vs the sandbox copy | write-through makes it false **when the mirror succeeds** |
| a Sync happens | S5 (post-exec / evict) | happens after every exec and before every eviction |

**Audit conclusion**: condition three is greatly weakened by write-through but **not eliminated** — it
still holds when "the mirror failed *and* the sandbox copy is stale", and the new decision procedure
(byte comparison + refusal) guarantees that case is **only refused, never overwritten**. The risk that
actually remains is not "a wrong overwrite" but the **silent loss of G1/G2**: the content is in the
sandbox, the sandbox is gone, and the store never had it.

## 6. Proposed order of work

| Priority | Action | Basis |
|----------|--------|-------|
| ~~P0~~ | ~~give the agent a signal on rebuild/expired~~ **landed**: the executor sets `workspaceReplaced`, and the call that discovered it renders the note into its own result (`takeReplacedNote`; moved from the queue to the call stack on 2026-09-18) (`TestRebuiltSandboxIsAnnounced`) | G1/G2, the only class of "silent loss" |
| ~~P1~~ | ~~make `pendingSignals` recomputable~~ **landed (2026-09-18)**, but the original plan was only half right: the recomputable facts (blocked/problem) are **not queued**, the one that cannot be recomputed (moved) goes through the **durable port** (`SignalStore`), and a rebuild rides the **call stack**. `TestEvictSignalOutlivesThePoolThatProducedIt` delivers through a different pool instance — the very cell the plan set out to prove | G3, cross-replica |
| ~~P2~~ | ~~restore deletion detection, using a durable store-side manifest rather than in-process state~~ **downgraded 2026-09-18**: the trace a deletion leaves is now reported (`delta.storeOnly`, see §3 G4), so **detection no longer needs the manifest**; a manifest would only add attribution, which is a product choice rather than an observability gap | G4 |
| P3 | snapshot cap and `/workspace` discipline: give actionable guidance in the signal (move large files out) | caution B |
