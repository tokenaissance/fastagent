# 05 · A systematic remediation plan

> Status: awaiting review / scheduling · last verified: 2026-09-17
> ⚠️ **This document's P0 (writing a shadow file) and P2 (baseline arbitration) were corrected by
> [06-cordis-review.md](./06-cordis-review.md)**: the formal re-review ruled that `shadow` violates keyed
> diff's convergence (it changes the entry set), and that the correct action is "a failed precondition →
> error + zero migration". Implement from 06's P0′–P3′; this file is kept as a record of the design's
> evolution (its incident analysis, test matrix T1–T7 and boundary statement remain valid).
> Prerequisites: [01 the as-built record](./01-current-implementation.md) ·
> [02 semantic analysis](./02-semantics-and-architecture.md) ·
> [03 states and timing](./03-state-machine-and-timing.md) ·
> [04 the incident record](./04-incident-workspace-2026-09-17.md)

## 0. The target invariant

The whole change serves one rule (the ownership declaration of [02](./02-semantics-and-architecture.md) §5):

> **The store is the single authoritative copy of a logical path; the sandbox's `/workspace` is a cached
> view of it.**
> **A path inside the sandbox that the store does not yet have is a sandbox artefact and may be written
> back;**
> **content at a path the store also has may be written back only when it can be shown to have been
> produced by this sandbox's current run.**

Two corollaries come with it:

- **destroy nothing**: no arbitration action may silently delete content either side successfully wrote;
  a conflict must first land somewhere observable and recoverable.
- **declare facts**: the backend's shape is declared by the driver as a fact the policy can read; the
  layers above must not infer it from side effects (byte counts).

## 1. Staging

| Stage | Goal | Risk | Depends on |
|-------|------|------|-----------|
| **P0** | stop the bleeding: no more silent overwrites, and conflicts are visible | low (only the write-back path's behaviour and logging) | nothing |
| **P1** | observe: quantify how often conflicts happen and what they look like | none (logs/metrics) | P0 |
| **P2** | treat the cause: introduce a baseline so "a sandbox edit" and "a host edit" can be told apart | medium (new scope-level state) | P1's data |
| **P3** | harden: remove the sources of forks (mirror host writes into the sandbox, unify path resolution) | medium | P2 |

The order is deliberate: **without P1's data, P2's design is guesswork**; and P3's mirror fails often in
real environments, so it can only reduce probability — it cannot replace arbitration.

## 2. P0 · Stop the bleeding

### 2.1 Change one: only separated backends write back (also deleting docker's pointless full read)

At the entry of `syncSnapshot` ([internal/sandbox/lifecycle.go](../../internal/sandbox/lifecycle.go)):

```go
// Only a backend whose /workspace is not shared with the host needs a write-back.
// docker's /workspace is a bind mount of the host directory — the snapshot reads back the store
// itself, every file compares equal and is skipped: it is a full read of the workspace for nothing.
if _, remote := ex.(RemoteWorkspace); !remote {
    return
}
```

The gain (a deletion from [02](./02-semantics-and-architecture.md) §7 Step 2): docker no longer walks the
host directory and reads every file on each eviction (still potentially thousands of files after skipping
`node_modules`).

### 2.2 Change two: make the criterion "directional + destructive-free"

Expand the current single `continue` into three explicit branches (keeping "new paths are persisted"
intact):

```go
for path, data := range files {
    info, statErr := p.workspace.Stat(ctx, sc.agentID, sc.projectID, sc.sessionID, path)
    switch {
    case errors.Is(statErr, workspace.ErrNotFound):
        // a sandbox artefact absent from the store — persisted as before
        // (images/reports produced by exec depend on this path)
    case statErr != nil:
        // do not gamble with data when the store is unreadable
        continue
    case info.Size == int64(len(data)):
        continue
    default:
        // same path, different size: the sandbox side cannot be shown to be newer (host tools
        // write this key too). Neither overwrite the store nor discard the sandbox content:
        // save it aside + warn.
        shadow := path + ".sandbox-shadow"
        _ = p.workspace.Put(ctx, sc.agentID, sc.projectID, sc.sessionID, shadow,
            bytesReader(data), int64(len(data)), "")
        slog.Warn("sandbox sync: store wins, sandbox copy preserved as shadow",
            "path", path, "storeBytes", info.Size, "snapshotBytes", len(data), "cause", cause)
        continue
    }
    // …the original Put…
}
```

Why "save aside" rather than "just skip": skipping outright would leave **a sandbox edit forever unable to
reach the store** (the same incident's mirror-failure mode; see the correction record in
[02](./02-semantics-and-architecture.md) §2 about the first plan). Saving it aside keeps both sides' data,
at the price of a temporary filename that needs a cleanup policy (§6).

### 2.3 P0 acceptance

- docker produces no `syncSnapshot` read behaviour at all (verifiable by logs or a benchmark);
- on e2b / boxlite, when the same path has different sizes the **store content is unchanged** and a warn
  appears carrying the path and both byte counts;
- the new `sandbox-shadow` object is observable via `List` and affects no read path (the UI does not show
  it, `read_file` does not read it);
- the three regression tests below (§5) pass.

## 3. P1 · Observation

P0's warn already answers "how many conflicts happened". Adding two more kinds of information makes it
usable for deciding P2:

| Metric | Collection | Use |
|--------|-----------|-----|
| conflicts per day per backend | count P0's warns (grouped by `cause`, `backend`) | judge P2's urgency |
| the distribution of paths involved | as above, aggregated by path | see whether conflicts cluster on a few files (`todo.md`-like high-churn files) |
| sandbox lifetime vs conflict rate | join with `sandbox_leases`'s `state`/`updated_at` | test [04](./04-incident-workspace-2026-09-17.md) §6.2's hypothesis that long-lived instances amplify the problem |

**Criterion**: if conflicts recur on high-churn files, P2 must be done; if the conflict count approaches
zero within a month (say because P3's mirror already covers the main paths), P2 can wait.

## 4. P2 · Baseline arbitration (treating the cause)

### 4.1 Model

```
ScopeBase : the fingerprint set of this scope in the store when the sandbox was born (Hydrate)
            {path → (size, hash?)}
X         : the sandbox's current content
S         : the store's current content
```

Per path `p` at sync time:

| Sandbox | store vs Base | sandbox vs Base | Verdict | Action |
|---------|---------------|-----------------|---------|--------|
| absent | — | — | — | none |
| present | unchanged | changed | a sandbox edit | **write back** (the design intent, now with grounds) |
| present | changed | unchanged | a host edit | **skip** (this incident's root cause) |
| present | changed | changed | a real conflict | keep the store + save the sandbox version aside + warn (P0's shadow semantics) |
| present | not in Base | — | a sandbox artefact | write back |

### 4.2 Implementation constraints

- **fingerprint granularity**: start with `(size, mtime)` or `size + a content hash`. Note
  [01](./01-current-implementation.md) §7: the existing fixtures' `ObjectInfo.ModTime` is always the zero
  value, and `SnapshotWorkspace` returns only a `map[string][]byte` (no time). If a hash is chosen, the
  baseline is computed host-side right after `Hydrate`, **so the snapshot interface needs no time field** —
  which also keeps P2 independent of backend capability differences.
- **where the state lives**: one row per scope suffices. Either the `sandbox_leases` row (a new column) or
  scope metadata storage; both have existing migration paths. A separate table is not recommended — this
  is a property of a scope, not an independent entity.
- **invalidation**: when the sandbox is `Release`d (destroyed) the baseline goes with it; `Sleep` keeps it
  (consistent with the `hydrated` flag).
- **multi-pod / cross-pod adoption**: the baseline is scope-level persistent state, so an adopter reads
  the same baseline — this is exactly P2's advantage over "in-process temporary state".

### 4.3 P2 acceptance

- all four cases of [03](./03-state-machine-and-timing.md) §3 have unit tests, and cases 2 and 4 no longer
  destroy data;
- the cross-pod adoption scenario (one pod writes the store, another syncs) has an integration test;
- a missing baseline (a legacy scope, a migration leftover) degrades to P0's behaviour (no overwrite +
  shadow), never to today's behaviour.

## 5. Test matrix

To be added in [internal/sandbox/lifecycle_test.go](../../internal/sandbox/lifecycle_test.go),
reusing the existing fixtures (`fakeWorkspace` / `snapshottingExecutor` / `snappingPool`):

| # | Scenario | Backend | Expectation |
|---|----------|---------|-------------|
| T1 | the store has the new version, the sandbox the old one, evict fires | e2b shape | the store is **unchanged**; a shadow appears; one warn |
| T2 | as T1 but triggered post-exec | e2b shape | as T1 |
| T3 | the sandbox has a new file the store does not | e2b shape | written back normally (`TestLifecycle_FlushOnEvict` stays green) |
| T4 | a shared backend (no `RemoteWorkspace`) | docker shape | `syncSnapshot` makes no `Stat`/`Put` call at all (assert with counting doubles) |
| T5 | a sandbox edit + an unchanged store (P2) | e2b shape | written back (protecting the legitimate "an exec edited it" chain) |
| T6 | both sides changed (P2) | e2b shape | the store is kept + a shadow + an alert; neither counted as success nor lost |
| T7 | **mirror path alignment**: a host tool writes a loose session's path | e2b shape | the copy hydrate placed inside the sandbox **is** the one updated; the store gains no second key with the same content (see [01](./01-current-implementation.md) §3.5) |

**The fixtures must be extended as well**: `fakeWorkspace` needs to record and return `ModTime` per path
(if T5/T6 decide by time) or to record baseline fingerprints (if by hash); `snapshottingExecutor` needs to
express "the sandbox content ≠ the store content". The current fixtures' shape (`Stat` returns only `Size`)
makes these scenarios **impossible to express at all**, which is one of the direct reasons this defect
survived for months.

T7 and T5 are a pair: T5 protects "a sandbox-side edit can be written back", T7 protects "a host-side edit
really reaches the sandbox". Only when both are green are the four conditions of
[03](./03-state-machine-and-timing.md) §7 simultaneously closed.

## 6. Operations and cleanup

| Item | Policy |
|------|--------|
| ~~the lifetime of `*.sandbox-shadow`~~ (**the design was retired**: 06 ruled that `shadow` violates keyed diff's convergence and replaced it with "error + zero migration"; this row was that design's ops counterpart and does not apply to the current implementation) | — |
| alerting | a warn-level log; escalate above a frequency threshold (P1 decides the threshold) |
| user visibility | do not show shadows by default (to avoid polluting the file panel); use the admin file browser when debugging |
| documentation | this directory plus [01](./01-current-implementation.md)'s contract notes must be updated with the code (the GEB isomorphism requirement: a code change without a doc update counts as unfinished) |
| **one-off cleanup: the chat-subdir duplicates decision A left behind** | `scripts/workspace_project_chat_duplicate_cleanup.py` (`--selftest` runs without any deployment). It **holds no credentials** and reads two listings (the object listing from `aws s3api list-objects-v2 …`, plus the chat list from `select project_id, session_key from sessions`), and it only ever lists a copy for deletion when **the same bytes survive at the project root** (ETag must be a non-multipart md5). A copy with no project-root counterpart, one whose bytes differ, or any run without `--chats` is reported and left alone. Dry-run by default; `--emit-deletes` only prints `aws s3 rm` lines and writes a manifest — executing them is a separate step for the operator. **Ordering**: ship A first (otherwise the deployed sync keeps producing duplicates and the cleanup becomes a treadmill), then run the cleanup |

## 7. P3 · Hardening (removing the sources of forks)

After the addendum, P3's order **must** change: unify the path mapping first, and only then discuss
mirroring. Otherwise writing two places only adds copies
([03](./03-state-machine-and-timing.md) §7.1).

1. **Unify "logical path → physical location" (the first item)**:
   four mappings coexist today — store keys come from `scopeSessionID()` + `wsPath()` or from
   `r.sessionID` + the raw path; hydrate expands the relative paths the store's `List` returns; the
   mirror hard-codes `/workspace/<path>`; and `mirrorSandboxWrite` infers the store key back from the
   `/workspace/` prefix. Extract a single function, e.g.
   `ResolvePath(logical string, scope Scope) (storeKey string, sandboxPath string)`, shared by every
   writer, hydrate and the sync. **This is the precondition for being able to write T7 at all.**
2. **Put `apply_patch` on the same resolution chain as `write_file` / `edit_file`**:
   today `apply_patch` uses `r.sessionID` + the raw path while the other two use `scopeSessionID()` +
   `wsPath()`, resolving to different objects in a project session
   ([01](./01-current-implementation.md) §8). Once item 1 is done this follows automatically.
3. **Reconsider whether the coding mirror still needs to exist**: it serves dev-server hot reload
   (introduced by the preview feature on 2026-06-14), not consistency; after the mapping is unified it
   should degrade to one call site writing into the sandbox through the same mapping, rather than a
   separate set of path rules.
4. **The mirror's failure semantics**: a failed mirror currently only logs
   ([02](./02-semantics-and-architecture.md) §3's use sites). It should be stated that "a failed mirror =
   a fork risk has been created", and counted in P1's metrics rather than silently degraded.

> **Postscript (2026-09-18, second pass)**: item 2 landed, items 3 and 4 narrowed to almost nothing, and
> item 1 has one piece left.
>
> - **Item 2 ✅**: all 6 touchpoints of `apply_patch` now use `scopeSessionID()` + `wsPath()`
>   ([01](./01-current-implementation.md) §8.1): 3 key-level unit tests + 1 "key ↔ sandbox path agree"
>   unit test + 1 live E2E, each falsified (reverting the key to `r.sessionID, path` turns them red).
> - **Item 3 ✅**: the mirror is one call site inside one function, `writeThroughSignal` — its store end
>   and its sandbox end are produced by the **same** `wsPath()`, and `mirrorCodingWriteToSandbox` is gone.
> - **Item 4 ✅**: a failed mirror is no longer silent. `WriteThroughOutcome` splits
>   "replaced a different version / no baseline to compare / no second copy" into four states that reach
>   the tool result, and "could not write into the sandbox" has its own σ ([10](./10-harness-state-audit.md) §2.1).
> - **Item 1 (partial)**: the named function `ResolvePath(logical, scope)` was not extracted — by the
>   "don't abstract a single implementation" test, two consumers (the tools' `wsPath()` and hydrate's
>   scope-relative expansion) each keep one call site, while "both ends must come from one source" is now
>   pinned by tests. **The third mapping disagreement that remains is the scope itself**: hydrate collapses
>   the session in a project session and sync does not — [01](./01-current-implementation.md) §8.2 /
>   [10](./10-harness-state-audit.md) G17, a product decision (does a sandbox-born file land at the project
>   root or in this chat's subdir).

## 8. Decision log (ADR summary)

| Decision | Why |
|----------|-----|
| **no CRDT / event sourcing / per-file locks** | the requirement is "do not silently overwrite", not distributed collaborative editing; the three backends' physical differences are the only variable, and a baseline covers it |
| **no new `MountKind` enum** | `RemoteWorkspace` already expresses the same fact; another name merely turns an implicit contract into a duplicated one. [02](./02-semantics-and-architecture.md) §7 Step 2 |
| **no file mtime as a cross-copy criterion** | docker's mtime is the same file as the host's but means something different; an e2b snapshot's mtime depends on tar behaviour; the test fixtures have no mtime. A baseline fingerprint is more robust and testable |
| **on conflict, keep the store by default rather than the sandbox** | the store is the user-visible copy (file panel, downloads, `read_file`), so keeping it preserves product consistency; the sandbox version is recoverable from the shadow |
| **no durable attribution manifest for G4 (decision, 2026-09-18: option a)** | what is missing here is only the *cause*, while the *actionable consequence* is already in the same sentence (the `storeOnly` line says exec cannot see those paths and how to put them back with `read_file` + `write_file`). A "delivery manifest" would cost **thousands of rows per hydrate** (the incident's shape: 1228 files in one hydrate) and still only produce a report; a "writer manifest" would touch all six write paths plus a new table, with historical rows left unsigned. When a scenario appears where the cause is worth money (settlement/audit), build the writer manifest — it covers the delivery manifest as a side effect |
| **G7b, upload half: no write-through (decision, 2026-09-18: option a)** | the product semantics is **"the panel is the file library"**: an upload adds a copy to the agent's durable library, and "the next `exec` lists it immediately" is **not** a requirement. Why: ① the cost and the workaround are already stated by the existing signal (`StoreOnlyLine` names the paths and gives the `read_file` + `write_file` recipe), so the agent never reasons inside a wrong world; ② write-through would turn "a library operation" into "a sandbox write" and would still need a rule for "what if no sandbox exists" (create one? pay a sandbox for a single upload?); ③ production is e2b + S3, so write-through needs `internal/setup` to reach the sandbox pool (cross-layer wiring) for no more than "one less manual copy". **Caveat**: on LocalFS + docker it is naturally visible immediately (one tree); this decision does not ask anyone to hide it — "not promised, not prevented" |
| **G7b, delete half: write through (decision, 2026-09-18: **d1**)** | "the file library" semantics cannot cover a delete: with the library copy gone and the sandbox copy still there, the next sync reads that copy as sandbox-born and writes it back — **"deleted, then it came back"**. Of the two candidates, d1 (write-through delete) beats d5 (deletion tombstone) because d1 fixes the **source** (the sandbox copy is gone, so the sync has nothing to resurrect) and is therefore **immune to the two-scopes problem** (01 §8.2 / G17); d5 would have to rebuild a scope judgement inside the sync — the very trap this round keeps repairing. The cost is one cross-layer wire (`internal/setup` has no pool → an optional interface on `userResolver` reaching the gateway), paid once for a confirmed bug; and it **never creates an instance** (`LiveExecutorPool` looks for live ones only) — minting a sandbox in order to delete a file inside it would be an absurd price |
| **the delete had to delete first (Fix 0, found and fixed the same day)** | While building d1 it turned out the delete **never worked at all** (the path got the scope applied twice — see the correction in [10 §3.2](./10-harness-state-audit.md) and G21): the list hands out agent-relative, prefixed paths while the handler added the query scope on top, so the key grew a second prefix ⇒ nothing was there ⇒ both backends report success ⇒ the UI believes it deleted and **the row is back on refresh**. Fix 0 = make delete use the download endpoint's convention. **Fix 0 and d1 must land together**: fixing only Fix 0 turns "silently nothing" into "deleted, then resurrected" |
| **G17: the preview container is project-addressed (G) + writes and deletes broadcast to every live container of the project (H) (decision, 2026-09-18)** | A project is "**one file tree, many containers**": containers are per chat (deliberate — concurrent chats must not share shell state) and the preview's dev server runs in only one of them. Docker closes that with a bind mount; a backend without one has to do it explicitly. **G**: with a project, take the container for `session=""` (`previewSandboxSession`) ⇒ one project = one preview, both entry points land on the same container (before this, a console-started preview used a container no agent turn ever used, so writes could never reach it). **H**: writes and deletes broadcast to **every live container of the project** (`LiveProjectExecutors`) — keeping per-chat shells and sharing only file data. **F′ (one container per project) was rejected**: it would share shell/processes/ports too, which is exactly what docker's own comment says to avoid. **Residual A** (a sync write-back still lands in the chat subdir ⇒ duplicate objects in the store) is decided separately |
| **the write-through's stamp (G22): fixed (2026-09-18, same round)** | The stamp read the store with the **sandbox scope**, while a coding-root project session's tools write the project root ⇒ miss ⇒ no stamp, and the reconcile fell back to whole-object reads for those paths. Fix: the writer hands the store scope down (`WriteThroughScope` gains `sandbox.StoreScope`; the single call site is `writeThroughSignal`). Measured on real E2B: **1 → 0** whole-object reads; the unit test pins "the stated scope is used", and the falsification (back to the sandbox scope) goes red at once |
| **G17's residual A: the sync writes back to the project root (decision, 2026-09-18; fixed)** | Before: the sync wrote back under the **container's** scope, so every project file the sandbox held was copied into `<project>/<chat>/…` — duplicates in the file panel and in `list_dir`, and a file created by `exec` was invisible to the tools (it landed in the chat subdir). After (`syncStoreScope`: a project means `session=""`): the write-back uses the **same key** as hydrate and the file tools, so no duplicates are produced and `exec`'s output is immediately readable by `read_file`. **Accepted consequence**: store keys are no longer per-chat isolated inside a project (that is what "one tree" means); **no migration** — copies created before the change stay in the store (no longer refreshed, and nobody cleans them), to be handled as a one-off cleanup if it ever matters |
| **staged rather than all at once** | the arbitration policy's correctness depends on the real frequency and shape of conflicts; before P1 there is no basis to prove P2's assumptions |
| **docker takes part in no write-back** | it is physically one copy, so any sync is self-deception; this also deletes a pure waste |
| **"mirror the host write into the sandbox" is not treated as a correctness mechanism** | the existing mirror's path mapping disagrees with hydrate ([01](./01-current-implementation.md) §3.5) and lands elsewhere in a loose session; until the mapping is fixed it can only reduce probability, never carry correctness. Reuse the same mapping once P3 item 1 is done |

## 9. Open questions (to settle before implementing)

1. Is the baseline fingerprint `(size, mtime)` or a content hash? The former is cheap but can miss cases
   (an equal-length edit within the same second); the latter is exact but needs one extra full read at
   hydrate time.
2. The shadow's retention period and who cleans it up (should it join the existing workspace lifecycle
   task?).
3. Should a "conflict detected" signal be exposed to the agent (so it can replay deliberately), or is a
   background alert enough?
4. Should P3's mirror be enabled only on e2b / boxlite, or also cover boxlite's unimplemented
   `PortExposer` scenario (no port exposure ⇒ no dev server ⇒ the mirror's benefit is smaller)?
