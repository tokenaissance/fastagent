# 06 · Re-reviewing this design against Cordis's formal principles

> Status: review record (conclusion: **the original plan is non-compliant under Cordis and has been
> corrected**) · last verified: 2026-09-17
> Basis: the practical summary of _A Programming Paradigm for Spatiotemporal Composability_
> (arXiv:2608.25512, Peking University × DeepSeek-AI) in
> [mcp-oauth-design.md §13](../mcp-oauth-design.md) — an existing compliant example
> in this repo; this document applies its criteria directly to filesystem sync.
> Objects of the review: [02](./02-semantics-and-architecture.md) ·
> [03](./03-state-machine-and-timing.md) · [05](./05-remediation-plan.md) (especially 02 §5's ownership
> declaration, 03 §8's points of attack, and 05's P0/P2)
> Position: this document is where **F1 (preconditions / zero migration)** in the [index](./00-formal-systems.md)
> gets its criteria; its authoritative definition is [07 part 2](./07-formal-rootcause-and-fix.md), and F2
> (observability) plus F3 (delivery) live in [08](./08-state-observability-principle.md).
> Follow-up: [07-formal-rootcause-and-fix.md](./07-formal-rootcause-and-fix.md) states the root cause and
> the fix rules in the same language, and corrects §4.4 here (the `shadow` file idea is retired in favour
> of a `⟨CONFLICT⟩` state inside the baseline).

## 1. The paper's criteria (the seven relevant here)

| # | Principle | Precise meaning |
|---|-----------|-----------------|
| C1 | **revertible effect** | effect = Γ → Γ×(Γ→Γ): an action returns (new state, **explicit inverse**), with the inverse produced **at the application site** and held by the runtime |
| C2 | **left inverse; only `g∘f` is promised** | witness `g(δ)=γ`; `f∘g` is never required. The inverse is the component author's obligation; the runtime only holds the replay |
| C3 | **undo is one-shot** | arm/dispose may fire once; repeat firing comes with no guarantee → preconditions must be explicit |
| C4 | **restoration = observational equivalence ≃** | not physical restoration, only indistinguishability for observers |
| C5 | **a false precondition = error + zero migration** | `set(k,v)` requires `k∉dom`; violating it must error and **must change no state at all** |
| C6 | **system boundary** | inside, one may "modify exclusively + restore"; crossing the boundary is an emission — irreversible, answerable only with withholding or compensation (also composed LIFO) |
| C7 | **declarative loader = entry list + keyed diff** | one entry per component; reconcile converges to "the state the final configuration dictates"; **rebuilding a single entry does not affect its neighbours** |

## 2. Mapping: this system's Γ, effects, keys and boundary

| Paper concept | What it is in filesystem sync |
|---------------|-------------------------------|
| context Γ | one scope's workspace state: `(the set of store keys, the sandbox /workspace, and the last observed equivalence)` |
| key space | **each path** (not each file); `byo-account-design.html` and `todo.md` are two keys |
| entry | one path's `{digest}`; the declarative document = an index from path → digest (called **B**, the baseline) |
| action (management plane) | `write_file` / `edit_file` / `apply_patch` / an exec write / `sync` |
| inside | store writes, sandbox writes, hydrate, reconcile (the system can put the state back) |
| outside (emission) | a fact the user has **already read** in the file panel or a download; actions the agent already took based on the old content; the other pod's actions during a cross-pod adoption |

That last row is this system's peculiarity and the essence of the incident:
**what is truly irreversible there is not the file being overwritten but "the user already read it and the
agent already acted on it".**

## 3. Criterion-by-criterion audit (of the original plan)

| Principle | What the original design did | Verdict |
|-----------|------------------------------|---------|
| C1 | `sync` has no inverse and returns none | ❌ **inapplicable/undeclared**: I treated `sync` as an ordinary function rather than an effect with an inverse |
| C2 | no witness (no test proving "after a revert it can be restored to the application site") | ❌ T1–T6 only assert final byte counts, which is not a witness |
| C3 | the `shadow` path is created anew on every conflict, so repeated runs differ | ❌ violates "one-shot" and cannot be replayed |
| C4 | `shadow` changes the workspace's **entry set** | ❌ observers (the file panel, `list_dir`, `read_file`) can see the extra object → observationally inequivalent |
| C5 | `sync` has no preconditions; everything is inferred from "the current state" | ❌ **the core violation**: it migrates even on a conflict (writing a shadow), and the paper's "zero migration" is not implemented |
| C6 | no distinction between "recoverable inside" and "compensatable only across the boundary" | ❌ undeclared (which also explains "fixing the store doesn't stick": fixing the store is inside, while the old copy still sitting in the sandbox is a different chain) |
| C7 | `shadow` makes one reconcile **change the entry set**, and each run differs | ❌ violates both halves of keyed diff: convergence and "one entry does not affect its neighbours" |

### 3.1 The worst one: `shadow` is a "reconcile with side effects"

C7 requires reconcile to be **idempotent + convergent**: for one declarative document, repeated runs must
reach the same final state (Theorem 80), and rebuilding one entry must not touch its neighbours
(Corollary 69). P0's `shadow`:

```
first sync:  conflict → create path.sandbox-shadow
second sync: the store has been modified → the shadow is updated again (different content)
third sync:  …
```

The entry set changes on every run and the final state depends on "how many times it ran". This repo
already has the correct counterpart: the per-key rows of `configs_kv`
([ports.go](../../internal/store/ports.go) line 130) are exactly "one row per key, converging
by key diff". My `shadow` was its exact opposite.

### 3.2 The second worst: missing preconditions = silent corruption in the paper's sense

C5 defines "executing in a wrong state" as requiring an error and zero migration. The original design had
no preconditions at all — `sync` only looked at "are the byte counts equal", which is equivalent to
"infer the intent from the state". This incident is the direct consequence of violating C5: **the state
did not permit this migration (the store was updated) and the system did it anyway.**

## 4. The corrected design

### 4.1 First, one characterisation: `sync` is a reconciler, not an effect

The paper's effect/inverse model applies to **actions initiated by a user or the agent**. `sync`
(triggered post-exec or on evict) is not an action but a **declarative convergence**: adjust the sandbox
to agree with the authoritative document. Asking for an "inverse" of a reconciler is the wrong question;
the right requirements are C5 + C7:

> **one key-level operation per path, with an explicit precondition; when it fails, error and migrate
> nothing.**

This also explains why "find an inverse for sync" goes nowhere: the inverse belongs to the **per-path
migration**, not to sync as a whole.

### 4.2 Syntax: the declarative document (baseline index B)

```
B (scope-level, durable) : { path → digest }        digest = (size, content hash)

the reading (the single authority):
   hydrate completes → B := the store's index at that time
   reconcile         → judge path by path against the table below; only paths that really migrated update B
```

Storage: **reuse `configs_kv`** — `kind = "ws_baseline"`, `scope`/`scopeID` for agent/project/session, one
row per path (or one JSON per scope). The reasoning matches §13.2: only per-key rows allow a key-level
diff, and no new table is introduced ([ports.go](../../internal/store/ports.go); the
`mcp_undo` in [mcp_undo.go](../../internal/agent/mcp_undo.go) is a ready example).

### 4.3 The precondition table (making C5 concrete)

Let `Xs` = the sandbox's current digest, `Ss` = the store's current digest, `Bs` = the baseline digest:

| # | sandbox | store | baseline | Verdict | Migration | Precondition satisfied |
|---|---------|-------|----------|---------|-----------|------------------------|
| 1 | absent | absent | — | nothing | none | ✅ (a no-op) |
| 2 | present | absent | absent | a sandbox artefact | push → store, `B := Xs` | ✅ `path∉dom(store)` |
| 3 | = Bs | = Bs | = Bs | neither side moved | none | ✅ |
| 4 | ≠ Bs | = Bs | = Bs | **a sandbox edit** (exec output) | push → store, `B := Xs` | ✅ `Xs≠Bs ∧ Ss=Bs` |
| 5 | = Bs | ≠ Bs | = Bs | **a host edit** | pull → sandbox, `B := Ss` | ✅ `Ss≠Bs ∧ Xs=Bs` |
| 6 | ≠ Bs | ≠ Bs | = Bs | **a real conflict** (both moved) | **zero migration + error** (see §4.4) | ❌ explicit error |
| 7 | absent | ≠ Bs | = Bs | a sandbox-side deletion | **zero migration + error** (the inverse needs a snapshot, see §5) | ❌ explicit error |
| 8 | — | — | no baseline (legacy scope) | unobserved state | degrade to "push only paths absent from the store" | ✅ conservative |

Three substantive differences from the original plan:

1. **row 5 (a host edit) now correctly means "pull", not "skip"**. The original only achieved "do not
   overwrite the new version with the old copy", leaving the sandbox and store inconsistent for a long
   time (also the reason `exec` sees old content in [01](./01-current-implementation.md) §3.5). With a
   baseline the pull is safe, and that is what makes "the two copies converge".
2. **row 6 becomes error + zero migration, instead of writing a shadow.** The conflict's visibility comes
   from the error, not from an extra file (C4/C7).
3. **row 7 brings deletions into the decision** (the original ignored sandbox-side deletions entirely).

### 4.4 Handling the conflict (row 6) correctly

The paper requires: a violated precondition → error + zero migration. But **zero migration makes the same
conflict fire again on the next sync**, so the system needs a convergent exit. By C7's convergence
requirement that exit can only be "change B", the declarative fact:

```
① error (fail loud, into tool_result / session trace; matching the mcp fail-loud convention)
② B[path] := ⟨CONFLICT, Xs, Ss⟩     ← the conflict itself is a state in the document, not a new file
③ the next sync: reads CONFLICT → converges by the declared policy (default: the store is authoritative
   → pull → Xs := Ss, B := Ss)
```

Why not a `shadow` file: C4's observational equivalence plus C7's "one entry does not affect its
neighbours". A `shadow` introduces a new entry, while `⟨CONFLICT⟩` is just a state value of the same entry.

**Compensation must also be declared (C6)**: if the product wants "the sandbox's version is not lost",
that is not something reconcile can carry but compensation — e.g. keep it in the session trace
(`session_events` already holds tool results) or offer a one-off export. The difference between the two is
exactly the paper's inside vs outside: **do not disguise compensation as reversibility.**

### 4.5 At the file-action level (C1/C2/C3): entirely missing today

The design above solves "sync" but not "**is the file write itself reversible?**". By C1,
`write_file` / `edit_file` / `apply_patch` should return (new state, inverse), with the inverse produced
**at the application site**. This repo already has the pattern:

```
the <mcp-undo> marker: `remove` returns the complete deleted entry with its tool_result at the
application site, the runtime persists it into session_events, and undo replays it automatically (LIFO).
```

Translated to the filesystem, the minimal viable shape is:

| action | precondition | inverse (produced on the spot) | witness |
|--------|--------------|-------------------------------|---------|
| `write_file`/`apply_patch` onto an existing path | (optional, see below) | **the old content in full** (or `sha256` + a store version reference) returned with the tool_result | after writing the old content back, `digest == the old digest` |
| `write_file` to a new path | `path∉dom(store)` | delete that path | after deletion the path does not exist |
| `edit_file` | `old_string` exists uniquely (**already**) | the reverse replacement | after the replacement the digest returns to its previous value |

Note that `edit_file`'s `old_string` match is already a precondition in C5's sense (a mismatch errors and
migrates nothing) — the only compliant place in this system; the other tools (`write_file`, and
`apply_patch`'s Add/Delete) have none.

**Should file writes get preconditions?** Read strictly, C5 says a `write_file` overwriting an existing
file should first require "holding the latest version" (optimistic concurrency: read first for a digest,
then pass it on the write). That changes how the agent works and is a product decision, so it must not be
slipped in during this repair. The staged suggestion is in §6.

## 5. Boundary statement (C6)

"What is irreversible and can only be compensated" must be written down explicitly, or someone will again
try to fix it by "keeping a spare copy":

| Position | Class | Reason |
|----------|-------|--------|
| store writes, sandbox writes, hydrate, reconcile | **inside (reversible)** | the system modifies them exclusively and can restore the last observationally-equivalent state |
| the agent has read the old content and acted on it (e.g. wrote a follow-up document based on the 41,262-byte version) | **outside (emission)** | cannot be taken back; only the agent can redo it (compensation) |
| a file version the user has downloaded or shared | **outside** | as above |
| another pod's writes during a cross-pod adoption | **outside** | the adopter cannot roll back the other end's actions; it should re-observe the baseline when adopting |
| a sandbox-side deletion (row 7) | **inside but currently irreversible** | the inverse needs a snapshot of the deleted content; without one, reversibility cannot be promised → it must error (C5) |

The third row is the incident's real damage: the byte-level revert can be fixed (inside), but the agent
had already spent two rounds of reasoning on the reverted version (outside). **So "prevent the revert"
must happen before the write is observed, not be patched afterwards** — which gives P0 its urgency at the
paper's level.

## 6. The corrected implementation order

| Stage | Content | Paper basis |
|-------|---------|-------------|
| **P0′** | ① short-circuit `sync` on shared backends; ② replace the byte-count criterion with the **precondition table**: rows 2/3/4 migrate as before, rows 5/6/7 **zero migration + error** (no shadow, no change to the entry set) | C5, C7 |
| **P1′** | land the baseline document `B` (`configs_kv`, kind=`ws_baseline`), written after hydrate; used at first only for **recording and comparison** (observation mode), with migration still on P0′ | C7's keyed-diff premise |
| **P2′** | switch on row 5's "pull" (a host edit → sync into the sandbox) and row 6's CONFLICT convergence; add witness tests | C2, C4, C7 |
| **P3′** | reversibility of file actions: the `tool_result` carries the old content (`<file-undo>` marker, modelled on `mcp-undo`), `undo` replays from the trace LIFO; deletion actions get a precondition (either a snapshot or an error) | C1, C2, C3 |

P0′ is smaller than the original P0 because **"error + zero migration" is simpler than "save a copy
aside"**, and it introduces no new entry. The original P0's shadow plan is retired.

## 7. Acceptance checklist (in the guardrail format of §13.11)

Check each line when adding or reviewing a change related to workspace sync:

- [ ] Is each key (path) operation's precondition written down explicitly? (`path∉dom(store)` / `Xs=Bs` / `Ss=Bs` …)
- [ ] On a violated precondition, does it **error and migrate nothing**? Is there any "write a copy while we're here"?
- [ ] Is the reconcile idempotent and convergent? Does the same document run twice reach the same final state?
- [ ] Does one reconcile **not change the entry set** (no neighbour key added or removed)?
- [ ] Are the parts crossing the system boundary (the user has read, the agent has acted, another pod wrote)
      explicitly declared as emission/compensation?
- [ ] Is there a witness (unit/e2e) proving "after the migration it can be restored to the application site"
      (`g(δ)=γ`)?
- [ ] Is undo/conflict handling at most once, and does a failure leave no half-commit?
- [ ] Does the file write action need a snapshot of the old value? (needing one and not doing it = a C1 violation)

## 8. Relation to the existing Cordis example

This directory's design should align with (not diverge from)
[mcp-oauth-design.md §13](../mcp-oauth-design.md):

| Paper principle | The MCP management plane's implementation | The counterpart for filesystem sync (as corrected) |
|-----------------|-------------------------------------------|---------------------------------------------------|
| per-key declarative storage | `agent_mcp_servers` / `mcp_oauth_tokens` … split by PK | `configs_kv` rows of `ws_baseline`, split by path |
| keyed-diff convergence | a declarative loader: entry list + reconcile | a path → digest index + the precondition table |
| a precondition *is* the error semantics | `ErrMCPServerExists` / `ErrNotFound`, refused before writing the DB | conflict/deletion states → error, zero migration |
| the inverse is produced at the application site | the `<mcp-undo>` marker carries the complete deleted entry | **to add**: a `<file-undo>` marker carrying the old content |
| boundary + compensation | `remove` does not revoke; `login↔logout` only compensates | user-read / downloaded / cross-pod writes = emission; a sandbox deletion is reversible only with a snapshot |
| witness = unit/e2e | the full store/agent/gateway suite + undo e2e | **to add**: T1–T7 rewritten to prove `g(δ)=γ` |

## 9. Conclusion in one sentence

The original design treated `sync` as "a directed copy", so it could only choose between "overwrite" and
"do not overwrite", and either choice silently loses one side's data. Cordis asks the right question:
**does this migration's precondition hold for this path?** — if it does, migrate (and update the
baseline); if not, error and migrate nothing; whatever crosses the boundary is declared as compensation.
This rewrite also removes `shadow`, a non-convergent action, and it puts "prevent the revert" before the
point where the write is observed.
