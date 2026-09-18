# 02 · Decomposing the system's semantics with Clean Architecture

> Status: design analysis · last verified: 2026-09-17
> Purpose: to answer "which layer is the current implementation putting responsibility in the wrong place?"
> Read [01-current-implementation.md](./01-current-implementation.md) first.
> Method: Robert C. Martin's four concentric layers + SOLID + component coupling; every proposal passes
> through the five-step Musk gate (see §7).

## 1. Mapping onto the four layers

| Layer | What it corresponds to here | What it should own | What it actually is |
|-------|-----------------------------|--------------------|---------------------|
| **Entities** | the invariant "the workspace is one durable state" | declare **who owns the version**: no successful write may be silently overwritten by an unrelated background action | the invariant does not exist. The same store is given three different identities in three places (§1.1) |
| **Use Cases** | `LifecyclePool.syncSnapshot`, `mirrorSandboxWrite`, `flushIfSupported` ([lifecycle.go](../../internal/sandbox/lifecycle.go)) | orchestrate the two writers so a conflict is decidable | degraded into a "copy function" whose criterion is a byte count — a criterion that only holds with a single writer (§1.2) |
| **Interface Adapters** | `RemoteWorkspace`, `WorkspaceSnapshotter`, `PortExposer` ([executor.go](../../internal/sandbox/executor.go)) | **translate** backend physical facts into words the policy can use | they report capability bits, not semantics. `RemoteWorkspace` carries exactly the fact arbitration needs, yet is only used for "should we sync at all" (§1.3) |
| **Frameworks & Drivers** | the docker bind mount, the e2b/boxlite sandbox fs, S3/local FS | provide facts only | the facts are indeed only provided, but they are **not declared**, so the layers above infer them from side effects (§1.4) |

### 1.1 Entities: there is no ownership rule

The only real invariant is one sentence:

> No successful write may be silently overwritten by a background action unrelated to the user's request.

That sentence **does not exist** in the code. In its place are three mutually contradictory implicit
assumptions:

| Location | It assumes the store is… |
|----------|--------------------------|
| the package comment in [workspace.go](../../internal/workspace/workspace.go): "durable blob store for agent-generated artifacts" | an **archive** of artefacts, not the primary copy |
| [file.go](../../internal/agent/tools/file.go) line 494: `read_file` reads the store first, falling back to the executor | the **authoritative primary copy** |
| the comment at [lifecycle.go](../../internal/sandbox/lifecycle.go) line 339: "upload anything the sandbox wrote" | the sandbox's **assistant-side record**, with the sandbox as the source |

Three identities, and no rule saying "one path may have two writers". The consequence is that each
locality looks reasonable while nothing anywhere is responsible for "who wins" — which is exactly why the
defect could hide for months.

**What it should look like**: write the ownership down and let types and comments carry it, e.g.
`// workspace.Store is the single authoritative copy of a logical path; the sandbox copy is a
discardable cached view`, or the opposite declaration `// the sandbox is authoritative and the store is
an archive` — either is fine, but one must be chosen and obeyed by every writer.

### 1.2 Use Cases: the criterion is coupled to the backend

`syncSnapshot` is **the only place that sees both writers at once** (host tools writing the store, `exec`
writing the sandbox), so conflict arbitration naturally belongs to it. Its criterion today is:

```go
if info, err := p.workspace.Stat(...); err == nil && info.Size == int64(len(data)) {
    continue
}
```

This "same size ⇒ skip" optimisation presupposes "the store is a mirror of the sandbox" — a
**backend-dependent** premise:

| Does the premise hold? | Consequence |
|------------------------|-------------|
| single writer (docker, physically shared) | equal size ⇔ equal content ⇔ nothing to do. The criterion is correct |
| two writers (e2b / boxlite) | equal size ≠ equal content (**an equal-length edit is missed forever**); different size ≠ a newer snapshot (this incident) |

In other words a **performance decision** (incremental vs full) was written into the criterion, and that
decision depends on the backend's physical shape — a Frameworks & Drivers fact leaking into a Use Case's
source. The dependency direction is inverted.

Another ignored fact: **there are more than two writers**. The full set is
`{host tools→store, mirrorCodingWriteToSandbox→sandbox, exec→sandbox, mirrorSandboxWrite→store, syncSnapshot→store}`.
No "who is newest" heuristic can stand on its own over that set.

### 1.2.1 Addendum: the mirror's misaligned path (strengthening the point above)

Two of those five writers use a path mapping **nothing verifies**:
`mirrorCodingWriteToSandbox` hard-codes the logical path under the `/workspace/` root
([file.go](../../internal/agent/tools/file.go) line 896), while hydrate expands by scope
prefix (the comparison table in [01](./01-current-implementation.md) §3.5). In a loose session the two
point at **different locations**, so:

- the "mirror" neither guarantees that the sandbox's hydrated copy is updated nor that no duplicate
  object appears;
- `mirrorSandboxWrite` then writes the mirror's product back to the store under a root key, so the store
  ends up with several keys for the same content.

This addendum moves the problem from "missing arbitration" to "missing a verified path mapping":
**the conversion from "logical path" to "store key / sandbox path" is implemented separately in five
writers**, with no single source. That is why [05](./05-remediation-plan.md) §7 promotes "unify path
resolution" to the first action of P3.

### 1.3 Interface Adapters: capability bits instead of semantics

A port should translate physical facts into the policy's vocabulary. The two existing markers report:

```go
type WorkspaceSnapshotter interface { SnapshotWorkspace(ctx) (map[string][]byte, error) } // "I can give you bytes"
type RemoteWorkspace interface { IsRemoteWorkspace() }                                    // "my /workspace is not shared with the host"
```

What they do not answer: **is this snapshot the authoritative copy? can it be stale? when the host has
written the same path, should I yield?** So the policy layer can only infer the backend type from
comparing byte counts.

A more precise criticism (to avoid overstating it): `RemoteWorkspace` **does carry** the shared/separated
fact that arbitration needs, and it is already used in three decisions:

| Use site | Purpose |
|----------|---------|
| [lifecycle.go](../../internal/sandbox/lifecycle.go) line 735 | whether to snapshot and write back **after an exec** |
| [lifecycle.go](../../internal/sandbox/lifecycle.go) line 772 | whether to mirror back to the store **after `WriteFile`** |
| [file.go](../../internal/agent/tools/file.go) line 893 | whether to mirror the host write **into the sandbox** |

The missing fourth site is **arbitrating "who wins"**. So the accurate statement is: **the fact is
already reported, but it has been downgraded into a switch for "should I make the call" rather than the
basis for "who has authority on the call".**

The fix therefore does not necessarily need a new interface: the cheaper route is to make that existing
fact actually used at the arbitration point, and to write its semantics into the comment.

### 1.4 Frameworks & Drivers: facts are provided but not declared

It is right that the driver layer "only describes itself". The problem is that the facts it describes are
never translated into a form the layers above can use, so those layers infer them from side effects
("are the two byte counts equal? then the backend is probably shared").

The inference happens to work on docker and fails on e2b / boxlite — **not because the adapters are
wrong, but because the port declares no semantics**:

| Fact | Provided by | Expressed? |
|------|-------------|-----------|
| is `/workspace` one copy or two | docker declares no marker; e2b/boxlite declare `RemoteWorkspace` | expressed, but its semantics are unused at the arbitration point (§1.3) |
| the snapshot may be stale / may come from a remote machine | nobody | **not expressed** |
| whether a copy is "the previous version" | nobody (`ObjectInfo` has no version, no author) | **not expressed** |

## 2. The dependency rule

The ideal direction:

```
Frameworks & Drivers (docker bind mount / e2b fs / S3)
        ↑ implement abstractions only, exposing no details upward
Interface Adapters (Executor markers / WorkspaceStore implementations)
        ↑ translate physical facts into the policy's vocabulary
Use Cases (syncSnapshot / hydrate / mirror)
        ↑ depend on abstractions only; decisions do not change when the backend does
Entities (the ownership invariant)
```

The actual direction:

```
Use Cases  ──infer from byte counts──▶  the physical shape in Frameworks & Drivers
```

This is a textbook **dependency-inversion gap**: the policy layer depends on details it should not know,
knows that it does not know them, and therefore guesses with heuristics. `syncSnapshot` contains no
`Backend()` call, yet its correctness depends on which backend it is.

## 3. Through SOLID

| Principle | Where it is violated | Note |
|-----------|---------------------|------|
| **SRP** | `syncSnapshot` does three things at once: fetch the snapshot, decide the delta, write the store | three responsibilities with three reasons to change: backend shape, arbitration policy, storage API |
| **OCP** | adding a backend (boxlite) requires re-evaluating whether the existing criterion still holds | extending requires modifying the *correctness premise* of existing code rather than adding an adapter |
| **LSP** | `WorkspaceSnapshotter` returns "the host's copy" on docker and "the remote copy" on e2b | two implementations of one interface with different semantics; callers cannot rely on one assumption |
| **ISP** | one boolean, `RemoteWorkspace`, serves three purposes (sync switch, mirror switch, implicit conflict semantics) | the interface is too narrow to carry the semantics; callers must supply the rest themselves |
| **DIP** | the policy layer infers backend physical facts from byte counts | source dependencies point at details |

**LSP is the core characterisation**: the name `WorkspaceSnapshotter` suggests "get the workspace's
bytes", while its actual meaning swings with the backend between "read the host itself" and "download the
remote copy". Every assumption a caller makes about this interface holds on only one backend.

## 4. Through component coupling

- **CCP (common closure)**: the implementations related to `/workspace` semantics are spread across four
  places — `internal/workspace` (storage), `internal/sandbox` (executors and sync),
  `internal/agent/tools` (file tools), `internal/runtime` (the coding runtime). All four share one reason
  to change ("the workspace semantics changed") but do not live in one component, so a semantic change
  needs four edits and is easy to miss (the "three identities" of §1 are the result of missing one).
- **SDP (stable dependencies)**: `internal/agent/tools` is a volatile layer (the tool set keeps growing)
  yet depends directly on the concrete behaviour of `internal/workspace` and `internal/sandbox` (for
  instance `apply_patch` assembling store arguments by hand, bypassing `scopeSessionID()`). The stable
  layer (the invariant) does not exist at all.
- **SAP (stable abstractions)**: the most stable thing should be the abstraction "the ownership rule";
  today it is neither stable nor abstract — because it does not exist.

## 5. The target semantics, in one sentence

I recommend adopting the following explicitly (either polarity works, but it must be unique and obeyed by
every writer):

> **The store is the single authoritative copy; the sandbox's `/workspace` is a cached view of it.
> A path inside the sandbox that the store does not yet have is a sandbox artefact and may be written
> back; content at a path the store also has may be written back only when it can be shown to have been
> produced by this sandbox's current run.**

The design consequences of that semantics: docker satisfies it by construction (the cached view *is* the
thing), while e2b / boxlite must explicitly distinguish "a new artefact" from "a change to a known path",
and need **a baseline** (which version the sandbox was hydrated from) to prove the latter.

## 6. Four resulting responsibility corrections (directions, not implementations)

| Layer | Direction of the correction |
|-------|-----------------------------|
| Entities | write the ownership rule into `internal/workspace`'s package comment and type docs, and use it as a review criterion |
| Use Cases | split `syncSnapshot` into "take the snapshot / decide / persist"; the decision depends only on abstract facts, never on byte counts inferred from the backend |
| Interface Adapters | make the existing fact used at the arbitration point (`RemoteWorkspace` decides "may an existing path be written back"); write "a snapshot may be stale" into the contract |
| Frameworks & Drivers | drivers report facts only; docker needs to participate in no write-back path at all (see the deletions in §7) |

## 7. The anti-over-design gate (Musk's five steps)

**Step 1 · Question (make the requirement less silly)**

- "Do we need version history / content hashes / three-way merges?" — **No.** The real requirements are
  "a successful write must not be silently overwritten" and "new files produced by exec must be visible".
  A version genealogy is an invented requirement.
- "Do we need to give docker a sync protocol too?" — **No.** On docker the two copies physically are one,
  so any "sync" is self-deception.

**Step 2 · Delete**

- **delete all of docker's write-back work**: `SnapshotWorkspace` walks the host directory on every
  eviction, reads every file, and compares sizes only to skip them all — pure waste. Short-circuit
  backends with `RemoteWorkspace == false`.
- **delete the implicit criterion "infer the backend from byte counts"**: not optimise it, but stop
  needing it.
- **do not add a parallel enum such as `MountKind`**: `RemoteWorkspace` already expresses the same fact;
  inventing a second name replaces an implicit contract with "two names for one thing", raising review
  cost with zero semantic gain.

**Step 3 · Simplify**

- reduce the criterion to one explicit rule ("does the path already exist in the store?") plus one
  explicit baseline check;
- drive the three-backend, three-scenario verification matrix from one table instead of writing special
  branches for e2b;
- for conflicts, first pick the simplest action that **destroys no data** (keep the original object, save
  the conflicting version aside and warn), rather than reaching for a merge.

**Step 4 · Accelerate**

Only after Steps 1–3 are done: make "write sandbox artefacts back" faster (e.g. tar only the paths that
actually changed) and refine hydrate's size limits and error taxonomy. Performance is not the current
problem.

**Step 5 · Automate**

After a week of manual, stable operation, consider automating "detect a conflict → tell the agent to
replay", or maintaining the baseline fingerprint as part of the sandbox lease row. For now, logs and
one-off scripts.

## 8. Conclusion

The current system "looks correct" on docker by the coincidence of physical sharing, and fails on
e2b / boxlite as a **necessary consequence of semantic absence at the port**. The minimal correct
direction is not to add a sync protocol but to:

1. write the ownership rule down (Entities);
2. let arbitration depend only on explicit facts, no longer comparing byte counts (Use Cases);
3. make the existing fact used at the arbitration point and write its semantics into the contract
   (Interface Adapters);
4. delete docker's pointless write-back work and every "guess the backend" branch
   (Frameworks & Drivers).

Concrete steps, the test matrix and the decision log are in [05-remediation-plan.md](./05-remediation-plan.md).
