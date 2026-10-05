# fastagent: the workspace↔sandbox filesystem in formal terms

> English edition of [`../文件系统形式化证明/`](../文件系统形式化证明/README.md) (the Chinese set is the origin).
> Status: as-built record + **three formal systems** · last verified: 2026-09-18
> **The three**: F1 preconditions / zero migration ([06](./06-cordis-review.md) · [07](./07-formal-rootcause-and-fix.md)) ·
> F2 observability ([08 §2](./08-state-observability-principle.md)) · F3 delivery ([08 §2.2](./08-state-observability-principle.md)) —
> the index is [00-formal-systems.md](./00-formal-systems.md).
> **Change register** (every change ↔ code anchor ↔ UT ↔ live e2e ↔ shipped?): [11-change-register.md](./11-change-register.md).
> **Parent index (L2)**: [../README.md](../README.md) (every document bucketed, one line each. The formal entry point is 00 as well).
> Subject: the sync mechanism between `fastagent`'s workspace (durable store) and the sandbox's
> `/workspace` (execution copy).
> Translation policy: prose is translated 1:1. Code, identifiers, paths, symbols and quoted
> agent-facing strings are kept verbatim. Section numbering matches the Chinese set so the two
> can be read side by side.

## What this directory is for

On 2026-09-17 production saw **deliverables silently reverted**: three files an agent had just
repaired in a session (`byo-account-design.html`, `qc-email-draft.md`, `todo.md`) were overwritten
back to earlier versions *after* the agent reported them restored — with no error anywhere.

The conclusion was that this is not a defect in a particular tool (`apply_patch` / `edit_file`)
but a structural one: **the workspace exists as two copies, the write-back path has a single
direction, and nothing arbitrates versions.** This directory pins down the mechanism, the
semantic mismatch, the timing, the incident evidence and the systematic fix, so later changes and
reviews can cite it.

## Reading order

| File | Content | Reader |
|------|---------|--------|
| [**00-formal-systems.md**](./00-formal-systems.md) | **The index of the three formal systems**: F1 preconditions / zero migration (06/07) · F2 observability (08 §2) · F3 delivery (08 §2.2) — what each answers, how they compose, the symbol table, the document map, obligation ↔ gap ↔ witness, the formal classification of what is still open, and **§7 the full inventory** (formal systems / mechanism layer / subsystem contracts / **single-source family** / models / unformalised) | **anyone** (start here) |
| [01-current-implementation.md](./01-current-implementation.md) | As-built record: ports, backends, writers, sync paths, observability | Anyone changing this code |
| [02-semantics-and-architecture.md](./02-semantics-and-architecture.md) | The system semantics decomposed along Clean Architecture's four layers. Where responsibility is misplaced | Anyone making design decisions |
| [03-state-machine-and-timing.md](./03-state-machine-and-timing.md) | Sync from the angle of state change / timing, and why Docker vs E2B differ | Anyone asking "why is Docker fine?" |
| [04-incident-workspace-2026-09-17.md](./04-incident-workspace-2026-09-17.md) | The incident: evidence chain, root cause, historical attribution (did leases introduce it?) | Triage and post-mortem |
| [05-remediation-plan.md](./05-remediation-plan.md) | Systematic fix, staged rollout, tests and observability, decision log (**its P0/P2 were corrected by 06**. Kept as design history) | Planning and implementation |
| [06-cordis-review.md](./06-cordis-review.md) | The design re-reviewed with Cordis formal principles (revertible effect / left inverse / preconditions / keyed diff / system boundary), yielding P0′–P3′ | Design and review |
| [07-formal-rootcause-and-fix.md](./07-formal-rootcause-and-fix.md) | **Empirically closed root cause (including reading a live sandbox)** + the defect described in Cordis's formal language, a constructive proof, and the fix rules with preconditions | Design authority / implementation basis |
| [08-state-observability-principle.md](./08-state-observability-principle.md) | **The state observability principle**: the formal statement (δ/σ), **delivery stated formally (§2.2: who produces / who places / who takes + O1–O5 + P1 "only the pull shape is reachable")**, three corollaries, a per-item audit of the current harness, a review checklist for new mechanisms, and the architecture decision on a unified exit | Required reading before any harness change |
| [09-sandbox-lifecycle-audit.md](./09-sandbox-lifecycle-audit.md) | **Sandbox lifecycle × filesystem audit**: 7 states, per-transition observability verdicts, four gaps and the order to handle them | Sandbox triage / lifecycle design |
| [10-harness-state-audit.md](./10-harness-state-audit.md) | **Whole-harness state-change audit**: every component that the agent triggers or that triggers the agent, checked δ→σ, gaps G5–G13 (including a locally reproduced P0: a signal that is always false) | Required reading before changing any agent state |
| [11-change-register.md](./11-change-register.md) | **Change register**: every F1/F2/F3 change ↔ code anchor ↔ UT ↔ live e2e ↔ deployment status (working tree vs production `HEAD`) | Release planning / review / delivery checks |
| [12-lease-formal-design.md](./12-lease-formal-design.md) | **The formal design of leases**: the six obligations L1–L7 induced from `channel_leases` / Redis / `sandbox_leases`, the as-built classification, **the G25 counterexample (the sandbox fencing token resets each generation — measured)**, `session_turns`' instantiation and its four-layer placement | Required reading before designing any cross-replica serialisation |

## Conclusions in one line each

> On docker, `/workspace` is a bind mount of the host directory (**one copy**). On e2b / boxlite it
> is an independent copy inside the sandbox (**two copies**). The write-back path `syncSnapshot`
> decided whether to overwrite by comparing byte sizes, which is only sound with a single writer:
> once a host tool (`write_file` / `edit_file` / `apply_patch`) has written to the store, the
> sandbox's stale copy overwrote the new version.

Second conclusion (added in the 2026-09-17 re-check):

> The mapping from "logical path" to "store key / sandbox path" **has no single source** — store
> writes, hydrate, the coding mirror and the write-back mirror each implemented their own. The
> coding mirror hard-coded paths under the `/workspace/` root, disagreeing with hydrate's scope
> expansion, so "keep both places consistent" did not actually hold. See [01](./01-current-implementation.md) §3.5 and §8.

Third conclusion (formal re-review, see [06](./06-cordis-review.md)):

> Sync is not a directed copy but a **reconciler**. The right question is "does this migration's
> precondition hold for this path": if it does, migrate. If not, **fail and migrate nothing**.
> On that basis the "save the loser as a `shadow` file" approach from 05 was ruled invalid (it
> changes the entry set and does not converge) and dropped.

Fourth conclusion (root cause closed, see [07](./07-formal-rootcause-and-fix.md)):

> The root cause is that `Sync` had **no precondition at all**: it inferred the direction of
> intent from a difference in state. That inference is vacuously true on docker (`X ≡ S`, branch
> `T3` unreachable) and false on e2b/boxlite. The fix is not "a better criterion" but making `Sync`
> a preconditioned reconciler **and** pushing the authoritative content back into the cache after
> a host write — neither half works alone.

Fifth conclusion (the rule-(3) criterion, see [07 §3.8](./07-formal-rootcause-and-fix.md)). **That
shape has since been retired**):

> The criterion at the time was a **content digest taken at hydrate time**: the same trace ("the
> store object is still the one we hydrated from, the sandbox bytes differ") could be either a
> sandbox edit or a host edit, and only the fingerprint of "what the sandbox was last handed"
> separated them. That requires **remembering** a fingerprint, and memory yields replica-dependent
> verdicts under cross-pod sandbox leases, so on 2026-09-18 it was replaced by a **self-describing**
> `size + mtime` criterion carried by the two copies themselves (07 §3.11.3).

Sixth conclusion (final shape: write-through + preconditions, see [07 §3.3](./07-formal-rootcause-and-fix.md) / §3.11.3):

> The incident's third necessary condition ("the host wrote and the two copies differ") can never
> hold on docker, because the bind mount makes `X ≡ S`. **Write-through moves that property to
> remote sandboxes**: when a host tool writes a file it writes into the sandbox too, and stamps the
> sandbox file's mtime to the store object's. "Are the two copies the same version?" is then
> decided by **each copy's own `size + mtime`** (bytes are compared only when metadata disagrees),
> with **nothing remembered anywhere**, so the verdict is identical across replicas and restarts
> (07 §3.11.3). Write-through proceeds per path, and failures are recorded per path.

Seventh conclusion (divergence is allowed, but must be identifiable and recoverable, see [07 §3.11](./07-formal-rootcause-and-fix.md)):

> **No CAS, no fallback copies**: before overwriting, *observe once*, put "replaced a different
> version (N bytes)" into the tool result, and write through as asked. The two earlier versions
> (refuse on mismatch. Preserve a `.sandbox-version` copy) were both rejected — the first
> deadlocks, the second duplicates the perception channel. The mechanism therefore converged to
> four: write-through / memory-free version decision / refuse + signal / exec change signal
> (07 §3.11.1), with no deadlock.

Eighth conclusion (the asymmetry of perception, see [07 §3.11.1](./07-formal-rootcause-and-fix.md)):

> **The agent perceives store changes (it made them) and does not perceive sandbox changes (a
> script made them).** That missing channel is why the incident was invisible. Sync now **emits
> signals**: the `exec` result carries "the sandbox changed these paths — synced" or "these paths
> were not synced because both sides moved". That channel turns "are the two copies consistent?"
> into an observable state — preservation and arbitration tools are the fallback, not the only
> means.

Ninth conclusion (**the state observability principle**, see [07 §3.12](./07-formal-rootcause-and-fix.md)):

> **A state change inside the harness must be perceivable by the agent**. Otherwise the agent
> reasons about a world that no longer exists. The key distinction is "caused by the agent"
> (the tool result is the receipt) versus "happened in its world" (invisible by default) — the
> latter must be signalled explicitly, in a channel the agent actually reads (the tool result),
> not slog. Auditing this directory's mechanisms against it: the filesystem-sync family is
> complete (including changes that happen during idle eviction), and three more "changed but
> unannounced" cases were found — context compaction, background memory updates, skill-list
> refresh — recorded in §3.12.

**The principle now stands alone as a document**: [08-state-observability-principle.md](./08-state-observability-principle.md).
It is not a corollary of filesystem sync but a requirement on the whole harness — the formal
statement is in its §2 (the agent's `Belief` versus the world `World`), the field audit and the
**review checklist for new mechanisms** are §5/§6. §5 has fully converged: the filesystem-sync
family (including eviction-time changes) and the "invisible when absent" family (compaction /
memory / skills / tool set) now all have signals. The latter are carried by **one unified
per-turn environment-change signal** rather than each subsystem inventing its own line.

## Verification strategy: which claims need a real E2B run

Not every assertion deserves a cloud sandbox — doubles are fast and deterministic. The criterion
is a single one:

> **Any assertion containing a "backend physical fact" must be verified on a real backend**. Pure
> logic (orchestration, criteria, convergence) uses doubles.

| Assertion type | Example | How it is verified |
|---------------|---------|--------------------|
| Backend physical fact | the sandbox and the store are **two** copies. Hydrate preserves the store's write time. The in-sandbox path layout | **real E2B** (`TestE2BLive*`) |
| Orchestration and criteria | the precondition table, zero migration, convergence, locality, store-only keys | doubles (`lifecycle_sync_contract_test.go`) |
| Historical fact | the defect was introduced in `950070b`. Leases amplified it | `git log` / `git show` (no run needed) |
| Production fact | the incident session's byte counts, logs, object timestamps | frozen into the evidence tables in §1.2 / §2.5 (one-off verification) |

For this directory specifically, the three live-E2B acceptance tests are in
[07 §3.9](./07-formal-rootcause-and-fix.md). They cover 01 §2/§3.5's comparison table, 07 §1.1's
metadata observation and §3.3's fix promise — the claims only a real backend can falsify.
Everything else rests on doubles and document cross-checks.

How to run them (not part of regular CI. Explicitly gated):

```bash
FASTAGENT_E2B_LIVE=1 E2B_API_KEY=e2b_... \
  go test ./internal/sandbox/ -run TestE2BLive -v -count=1
```

## Related existing documents (Chinese)

- [../sandbox-pool-leases.md](../sandbox-pool-leases.md) — cross-pod sandbox leases
- [../sandbox-scope-leak.md](../sandbox-scope-leak.md) — scope isolation and hydrate failure
- [../coding-agent-runtime.md](../coding-agent-runtime.md) — coding subdir and dev-server preview
- [../per-chatter-files.md](../per-chatter-files.md) — per-chatter file routing
