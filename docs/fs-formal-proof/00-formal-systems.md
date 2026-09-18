# 00 · The three formal systems: an index

> Status: index · last verified: 2026-09-18
> This directory (`文件系统形式化证明`, formerly `文件系统`) actually carries **three** formal systems:
> its name only describes the first one's application area; the other two constrain the whole harness.
> This document is their **single entry point** — what each defines, how they compose, which code each
> symbol lands on, and which test pins each obligation.

## 1. Three systems, three questions

| # | Formal system | Defined in | Question it answers | Form of the judgement | Cost of violating it |
|---|---------------|-----------|--------------------|-----------------------|---------------------|
| **F1** | **Preconditions / zero migration** (Cordis: reconciler, left inverse, keyed diff, system boundary) | [06](./06-cordis-review.md) · [07 part 2](./07-formal-rootcause-and-fix.md) | **May this migration happen at all?** And what happens when it may not? | Declarative: one migration per key + an explicit precondition; a violation ⇒ **error + zero migration** (R1/R2); idempotent convergence (R3), locality (R4) | Silent corruption: overwriting what someone else wrote (the 2026-09-17 incident) |
| **F2** | **Observability** (`δ` / `σ`, `Belief` / `World`) | [08 §2, §2.1, §3](./08-state-observability-principle.md) | Which changes **must be stated**? And must the statement be true? | A universal proposition: **∀ δ triggered by the harness, ∃ a σ the agent can consume**; corollaries C1/C2/C3 | A missing signal → data loss + **belief corruption** (the agent reasons about a world that no longer exists) |
| **F3** | **Delivery** (produce / place / take, O1–O5, P1) | [08 §2.2](./08-state-observability-principle.md) | How does σ **actually arrive** at the agent? | Constructive: three roles + invariant **I1** + five obligations + "only the pull shape is reachable" (**P1**) | A signal exists but never arrives (§6.1's idle eviction, G3's in-process queue, G12's slog-only drop) |

**In one line**: **F1 decides whether the mechanism may act, F2 decides whether it speaks, F3 decides
whether the speech arrives.** The three are a **conjunction** — if any one fails, the change is not done.

## 2. How they compose: the three systems as one decision procedure

```
For a mechanism M that changes the agent's world:

① F1  What is this migration's precondition? (the tables in 07 §3.3 / §3.11.3)
         does not hold → error + zero migration, and **say it correctly** (refusing ≠ being silent)
② F2  Who made the change?
         the agent itself → the tool result is the receipt; stop
         the harness      → a σ is mandatory, and it must be true (O1)
③ F3  Who places it, on D₁ (the call receipt) or D₂ (the turn entry)? (O2)
④ F3  Who takes it, and when? (O3: on the consumer's next read)
⑤ F3  Can it be lost between place and take? (O4: in-process state means yes)
⑥ F2  Silent when there is no δ? (C3 = O5)
```

## 3. Symbols (all three, with their code)

| Symbol | System | Meaning | Where it lives |
|--------|--------|---------|----------------|
| `World(t)` / `Belief(t)` | F2 | the world's real state / the agent's belief about it | `envSnapshot` |
| `δ` | F2 | one fact that the world changed | `sandbox.delta`, `sandbox.WriteThroughOutcome`, the diffs inside `envSnapshot` |
| `σ(δ)` | F2 | δ rendered as one sentence the agent can read | `signalsFor(delta)`, `Registry.writeThroughSignal`, `envTracker.signal` |
| C1 / C2 / C3 | F2 | a channel it really reads / silence ≠ no change / an exception channel | the three exits + the witnesses in §5 |
| `produce` / `place` / `take` | F3 | produce / place / take | the role table in [08 §2.2.1](./08-state-observability-principle.md) |
| **I1** | F3 | the separation of the three powers: `place` belongs to the change side, `take` to the consumer | — |
| **D₁ / D₂** | F3 | the call receipt (a tool result) / the turn entry (the turn prompt) | the `lazyExecutor.Exec` result / the end of the `ContextBuilder` prompt |
| **O1–O5** | F3 | true statements / landing on a delivery point / the moment of taking / no loss / no noise | the obligation table in [08 §2.2.3](./08-state-observability-principle.md) |
| **P1 / P1′** | F3 | only the pull shape is reachable; every δ must land on D₁ or D₂ | proof by absence: the code has **no** `AgentInbox`-style interface |
| precondition / zero migration | F1 | `set(k,v)` requires `k∉dom`; a violation ⇒ error and no state changes | the branches in `LifecyclePool.WriteThrough` / `syncSnapshot` |
| R2 / R3 / R4 | F1 | zero migration / convergence / locality | `lifecycle_sync_contract_test.go` |
| inside / outside (emission) | F1 | recoverable vs compensatable-only | the boundary table in [07 §3.5](./07-formal-rootcause-and-fix.md) |
| left inverse `g(δ)=γ` | F1 | the inverse is produced **at the application site** | `edit_file`'s `old_string` match; the `<mcp-undo>` pattern |

## 4. Which document carries which system

| Document | Carries | Key sections |
|----------|---------|--------------|
| [01](./01-current-implementation.md) | the factual basis (the premise all three share) | §2 backend physical facts, §3.5 and §8 path mapping |
| [02](./02-semantics-and-architecture.md) | the **layer attribution** for F1/F3 | §1 the four layers, §5 the ownership declaration, §7 Musk's five steps |
| [03](./03-state-machine-and-timing.md) | F1's timing | §3 two registers, §7 the trigger conditions |
| [04](./04-incident-workspace-2026-09-17.md) | the evidence | §2 the evidence chain, §6 historical attribution (leases did not introduce it) |
| [05](./05-remediation-plan.md) | F1's **historical plan** | §2 P0 (corrected by 06), §5 the T1–T7 test matrix |
| [06](./06-cordis-review.md) | where F1's criteria come from | §1 the seven principles, §4 the corrected design |
| [07](./07-formal-rootcause-and-fix.md) | F1's **authoritative definition** | part 2 (domain and constructive proof), §3.3, §3.11.3 |
| [08](./08-state-observability-principle.md) | **F2 + F3** | §2/§2.1/§3 (F2), **§2.2 (F3)**, §5 the audit, §6 the checklist, §9.1 the exits |
| [09](./09-sandbox-lifecycle-audit.md) | F2/F3 applied cell by cell to the **sandbox lifecycle** | §2 the transition verdicts, §3 G1–G4 |
| [10](./10-harness-state-audit.md) | F2/F3 applied to the **whole harness** | §1 the whole picture, **§4 the gap table (with the duty column)** |
| [**11**](./11-change-register.md) | the **delivery index for all three systems**: every change ↔ code anchor ↔ UT ↔ live e2e ↔ deployment status | the row-by-row register (F1 #1–#4 · F2 #5–#18 · F3 #19–#20 · path/scope #21–#27) |

## 5. Obligation ↔ gap ↔ witness (the checkable index)

| Obligation | The gaps that violated it | The tests that pin it |
|------------|---------------------------|-----------------------|
| **O1** true statements | ~~G5~~ (a false σ), ~~G6~~ (never rendered), ~~G8~~, ~~G10~~, ~~G11~~ (stdio half), ~~G4~~ (signal half), ~~G19~~ | `TestWriteFileSignalsUncheckedReplacement`, `TestEnvSignalCarriesIdentityFileChanges`, `TestCronFingerprintIgnoresRunBookkeeping`, `TestStdioClientHandsNotificationsToTheHandler`, `TestE2BLiveUnhydratedFactSurvivesPodHandoff` |
| **O2** placement | ~~G12~~, G1/G2 | `TestDeferredTurnsAnnouncesADroppedScheduledTask`, `TestEvictionSignalReachesNextToolResult` |
| **O3** the moment of taking | no violation; **G13 is its positive instance** (a pull σ: the criterion is recomputable, so the consumer's next read IS the delivery point) | `TestBashOutputTool_DrainsTailOnExit`, `TestSandboxJobOutputReturnsDeltaThenStatus` |
| **O4** no loss | ~~G3~~, ~~G9~~, ~~G20~~ | `TestEvictSignalOutlivesThePoolThatProducedIt` (delivered by a different pool instance), `TestReplacedSandboxNoteRidesTheCallThatFoundIt`, `TestRunReceiptStampSurvivesAReload` |
| **O5** no noise | — (no "spoke without a change" instance yet) | `TestExecIsQuietWhenNothingChanged`, `TestWriteFileStaysQuietOnASharedBackend` |
| **F1** preconditions / zero migration | ~~the incident, D~~ | `TestSyncContract_StoreEditIsNotOverwritten`, `SecondReconcileWritesNothing`, `DomainUnchanged`, `TestE2BLive*` |
| **F1** boundary (inside / outside) | **G4** (a deletion is irreversible; no snapshot) | — (a missing witness is itself part of that gap) |

## 6. What is still open, sorted by formal system

| Item | Belongs to | Status |
|------|-----------|--------|
| **G11** the HTTP side of MCP notifications | F2 · O1 (the transport has no channel at all) | open (stdio half fixed; HTTP needs SSE or a periodic re-list) |
| **G4** the *attribution* of a sandbox-side deletion | F2 · O1 (**fixed as far as "the fact is stated"**) + F1's boundary (an irreversible action with no snapshot) | **decided: no attribution (2026-09-18)** — the consequence is already delivered, and a manifest would buy only the cause at thousands of rows per hydrate; see the decision log in [05 §8](./05-remediation-plan.md) |
| **G7b** uploads/deletes and the live sandbox | **not F1–F3**: write-path symmetry | **upload half: decided a (no write-through, "the panel is the file library")**; **delete half: decided d1 and fixed** (write through to the live sandbox, never creating one) |
| ~~**G21**~~ the panel delete was a silent no-op (the path/scope convention applied twice) | **belongs to F1** ("one path, one key") | **fixed (2026-09-18)**: Fix 0 (delete uses the download endpoint's path convention) + d1 (also drop the live sandbox's copy), landed as a pair; both halves pinned on real E2B (without d1 it comes back; with d1 it does not) |
| ~~**G17**~~ a project's "one file tree, many containers" (the preview container was addressed wrong, sibling containers missed writes, and the sync wrote back into the chat subdir) | **not F1–F3**: a scope invariant | **decided + fixed (2026-09-18: G+H+A)**: the preview container is addressed by project (G); writes and deletes are broadcast to every live container of the project (H); the **sync write-back is collapsed to the project root** (A, `syncStoreScope`); **per-chat shells are kept**. No migration: copies produced before the change remain in the store |
| ~~**G22**~~ the write-through's mtime stamp silently missed in project sessions (it read the wrong store scope) | **belongs to F1** (the third instance of "one path, one key") | **fixed (2026-09-18)**: the writer hands the store scope down (`sandbox.StoreScope`); measured on real E2B, whole-object reads for that path went **1 → 0**; all three defects at this seam (G21 / G17-A / G22) are now closed |
| **G15** `notifications/initialized` is never sent | **not F1–F3**: protocol compliance | open (needs live verification that the handshake is unaffected) |

**Closed the same day (kept here so the index stays comparable; details in [10 §4](./10-harness-state-audit.md))**:
G1–G3 (delivery point / durable carrier), the signal half of G4, G5/G6 (σ telling lies), G7a (the
divergence is named), G8/G9/G10 (outside writers), the stdio half of G11, G12 (drops leave a trace),
G14 (two sources), G16 (the mask written back), **G18** (01 §8 path resolution), **G19** (the unhydrated
declaration lost on an instance hand-off), **G20** (the environment baseline moved into the turn receipt;
`envTracker` deleted). Of these, **G13 was reclassified as "not a defect"**: it is a pull-shaped σ whose
criterion is recomputable (place 1), not an F3 gap.

That table is itself a classification result: **only some of what is still open is a defect in the formal
sense** — G11 is (the transport has no channel) and G4's remaining half is (no durable manifest); the
other three belong to different families (a product decision, a scope invariant, protocol compliance) and
should not be booked as "observability left unfinished".

> **What actually remains (2026-09-18, after the closing pass)**: in the formal sense, **only the HTTP
> half of G11** (the MCP transport has no notification channel; it needs SSE or a periodic re-list).
> G15 (`notifications/initialized`) belongs to the same MCP family and is out of this round's scope by
> decision. **The one non-defect worth recording**: the duplicate copies under the chat subdirs that were
> produced before decision A are still in the store (no longer refreshed, and nobody cleans them) — a
> one-off cleanup script: `fastagent/scripts/workspace_project_chat_duplicate_cleanup.py`
> (`--selftest` needs no deployment; it only lists a copy for deletion when the same bytes survive at the project root — and it should run **after** A ships, or the old sync keeps recreating them).; see the "closed" list
> below and [10 §4](./10-harness-state-audit.md).

## 7. In one sentence

> Three formal systems answer three different questions, and missing any one of them produces its own
> failure: **without F1** a mechanism overwrites what someone else wrote; **without F2** the agent
> reasons in a world that no longer exists; **without F3** a signal is produced and never arrives.
> A change is only designed when all three questions can be answered.
