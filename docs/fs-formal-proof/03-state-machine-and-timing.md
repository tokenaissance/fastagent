# 03 · State changes and timing

> Status: model + as-built timing cross-check · last verified: 2026-09-17
> Purpose: to lift "how sync works" out of the code details and show, from the angle of state change,
> **why docker is not a problem and e2b / boxlite necessarily is**, and which cell of the timeline the
> incident landed in.

## 1. State definition

Treat each logical path `p`'s state as a pair:

```
State(p) = (S[p], X[p])
  S[p] : the byte content of p in the store (or non-existent)
  X[p] : the byte content of p in the sandbox's /workspace (or non-existent)
  Base  : the version copied from the store when the sandbox was born (the "baseline"); in the
          current implementation it is implicit in X's initial value
```

Three abstract events (I/O details ignored):

| Event | Semantics |
|-------|-----------|
| `HostWrite(p, v)` | a host file tool writes: `S[p] ← v` |
| `SandboxWrite(p, v)` | an `exec` inside the sandbox writes: `X[p] ← v` |
| `Sync()` | the write-back channel (triggered post-exec or on evict): writes into `S` those entries of `X` whose size differs from `S` |

`Hydrate()` is a fourth event (`X ← S` at sandbox creation), but it only happens at a lifecycle boundary.

## 2. docker: a single register

On docker `S` and `X` **are physically the same thing** (the bind mount), so `X[p] ≡ S[p]` holds always.

| Moment | State(p) | Note |
|--------|----------|------|
| `Hydrate` | — | not needed; the bind mount is the sharing |
| `HostWrite(p, v2)` | `S = X = v2` | one write acts on both copies |
| `exec` | unchanged (unless a script edits that file) | `SandboxWrite` and `HostWrite` are the same operation |
| `Sync()` (evict) | unchanged | the snapshot reads back `S` itself: `len(X[p]) == len(S[p])`, so the criterion always says "skip" |

**The key point**: on docker `Sync()` is not "not writing" — it **really runs every time**: it walks the
host directory, reads every file, compares sizes one by one, and skips them all. So it is neither
necessary nor free (the docker row of [01-current-implementation.md](./01-current-implementation.md) §2).

Conclusion: docker has no conflict **not because the criterion is clever, but because this backend has
no second register.**

## 3. e2b / boxlite: two registers + one-way write-back

```
initially:  S[p] = v1      X[p] = v1        (Hydrate)
① HostWrite(p, v2):        S[p] = v2      X[p] = v1      ← the fork appears, and nobody knows
② exec (not touching p):   S[p] = v2      X[p] = v1
③ Sync():                  take the snapshot → v1; Stat(S[p]).Size = |v2| ≠ |v1| → Put(v1)
                           S[p] = v1      X[p] = v1      ← v2 is silently destroyed
```

Step ③ is the whole problem with this model: `Sync()`'s criterion only looks at "are the sizes equal",
and after a fork it cannot tell two situations apart:

| Actual situation | What the criterion sees | Verdict | Correct? |
|------------------|------------------------|---------|----------|
| the sandbox changed p, the store did not | sizes differ | overwrite with the sandbox | ✅ correct (this is the intent) |
| the store changed p, the sandbox did not | sizes differ | overwrite with the sandbox | ❌ **data loss** (this incident) |
| both sides changed p | sizes differ | overwrite with the sandbox | ⚠️ a coin flip |
| the contents differ but the lengths are equal | sizes equal | skip | ❌ **silently misses the write** (not yet observed, but mechanically true) |

A correct decision needs to know whether `X[p]` moved relative to `Base` and whether `S[p]` did —
that is, it needs **a baseline**. The current implementation records no baseline anywhere: once
`hydrate` finishes, the system has no memory of "which store version this sandbox content corresponds
to".

## 4. The full lifecycle timeline (e2b)

```
the scope is used for the first time
  └─ Hydrate()             X ← S (pour the then-current store into the sandbox)   ← Base is fixed here, then forgotten
turn N
  ├─ HostWrite(...)        S ← the new version, X untouched                       ← the fork window opens
  ├─ exec(...)             X ← sandbox artefacts
  │    └─ Sync(post-exec)  entries of X whose size differs from S are pushed back  ← may overwrite the HostWrite
  └─ exec(...) triggers it again                                                    ← another overwrite window
idle
  └─ Sync(evict)           as above, pushing all of X once more                     ← the incident usually ends here
     └─ Sleep() or Release()
        ├─ Sleep: keeps X and the hydrated flag → the same X keeps being written back after waking (the window never closes)
        └─ Release: destroys X → re-created by the next Hydrate (the window closes for now)
```

Note two amplifiers:

1. **Sync fires often**: after every `exec` (RemoteWorkspace backends only) plus every idle eviction.
   A session usually has several execs per turn, so "overwrite opportunities" are on the order of the
   exec count.
2. **sleep does not close the window**: a sleepable backend keeps the filesystem and the `hydrated` flag
   ([lifecycle.go](../../internal/sandbox/lifecycle.go) around line 320), so the old copy
   can persist across turns, across minute-scale waits and across pod adoption.

## 5. Alignment with the real session

Using the 2026-09-17 incident session (full evidence in [04](./04-incident-workspace-2026-09-17.md)),
the table above resolves to (UTC):

| Event | Time | State change |
|-------|------|--------------|
| `Hydrate` | 05:25:33 | sandbox `iknoadnp4ho8un3yhmjr2` is created, `X ← S` (including the early versions of the three files) |
| `HostWrite` × N | 08:04–08:19 | `S ← the new versions` (50,282 / 6,893 / 462…), `X` untouched |
| `Sync(evict)` | 08:09:47 | `files=2` pushed back → overwrite |
| `Sync(evict)` | 08:24:18 | `files=3` pushed back → overwrite again |
| `HostWrite` (repair) | 08:15 / 12:07 | `S ← 55,527 / 6,893 / 462` |
| `Sync(post-exec)` | 12:05:26 | `files=1` pushed back |
| `Sync(evict)` | 12:15:17 | `files=3` pushed back → **S ends up at the old versions (41,262 / 5,705 / 580)** |

The 12:15:17 entry deserves the most attention: it happened **after** the agent finished its repair
(12:07), triggered by idle eviction — unrelated to the user's request or any agent action.

## 6. Why "new files" are fine

`Sync()` is trustworthy for paths the store does not have (they are sandbox artefacts). So:

| Situation | Result |
|-----------|--------|
| a file created in this session that never entered the sandbox | ✅ persisted correctly (there is no `X` to confuse things) |
| a file created this round that is not in the sandbox either | ✅ as above |
| a file that existed at `Hydrate` time and was later changed by a host tool | ❌ reverted (all three victims were of this kind) |

This explains what looked contradictory during the post-mortem: "every newly written file is fine and
every edited old file is broken" — it is not a problem with the editing tools, but with **whether the
path has an old copy inside the sandbox**.

## 7. The complete list of trigger conditions

A silent revert needs all of:

1. the backend is separated (`RemoteWorkspace`: e2b / boxlite);
2. the path exists in the sandbox's `X` (from `Hydrate` or a write inside the sandbox);
3. the host wrote that path, and `|S[p]| ≠ |X[p]|`;
4. a `Sync()` happens (post-exec or evict).

Conditions 1–3 decide "is there a mine"; condition 4 only decides "when it goes off". So the typical
observation of this class of defect is **delayed, batched, and unrelated to user actions** — exactly
this incident's shape.

### 7.1 Why "mirror into the sandbox on host write" does not close this window

Intuitively, if a `HostWrite` also wrote the content into `X`, then `S` and `X` would agree and
condition 3 would stop holding. That intuition **does not hold** in the current implementation, and the
reason is not that the mirror didn't write but that it wrote somewhere else:

| | Location |
|---|---|
| the copy hydrate laid down | `/workspace/sessions/<sid>/<path>` (loose session) |
| the copy the mirror writes | `/workspace/<path>` |

So after the mirror, the state "the path hydrate placed inside the sandbox is the old version" still
exists, condition 3 still holds — there is simply one more copy now (see
[01](./01-current-implementation.md) §3.5).

This shows a more general conclusion: **as long as the mapping from "logical path" to "physical
location" has no single source, any scheme that "writes both places to stay consistent" only adds
copies instead of removing the fork.**

## 8. Where a fix can act, in timing terms (input to [05](./05-remediation-plan.md))

| Point of attack | Effect | Cost |
|-----------------|--------|------|
| short-circuit shared backends at the entry of `Sync()` | removes the pointless full read on docker | none (a pure deletion) |
| change "exists and sizes differ" to **do not overwrite** (keep the store, save a conflict copy separately) | stops the bleeding immediately; destroys neither side's data | needs a new path convention and a cleanup policy |
| record **Base** (the fingerprint of the store snapshot at sandbox hydrate) and decide from "which side moved relative to Base" | mechanically separates "a sandbox edit" from "a host edit" | needs a scope-level persistent field (could reuse the `sandbox_leases` row) |
| mirror into the sandbox when the host writes | greatly reduces the probability of a fork | mirror failures are normal (the sandbox may not exist), so it can only be hardening |

The first two can land "without changing the model"; only the last two touch the model — which is exactly
why [05](./05-remediation-plan.md) is staged.
