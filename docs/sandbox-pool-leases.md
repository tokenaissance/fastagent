# Cross-pod E2B sandbox lease registry

Status: implemented (v1 base + cache-reconcile fix + CAS/epoch) · Storage:
Postgres (`sandbox_leases`), sqlite in tests

## Problem

The sandbox pool is per gateway process. With N gateway replicas behind a
round-robin service, one (agent, project, session) can be served by every pod
in turn, and each pod lazily creates its **own** e2b instance for the same
scope → up to N sandboxes per session (observed: 10 pods → 10 e2b instances).

## Decision

Share the scope→sandbox mapping in Postgres. Each row is a lease:

```text
scope_key      PRIMARY KEY — "agent[:p:proj][:s:sess]" (poolKey)
owner          pod identity ("hostname:pid")
sandbox_id     e2b instance id
envd_token     short-lived e2b access token (shared so other pods can adopt)
template       e2b template used at creation
expires_at     unix seconds; expired ⇒ dead, next acquirer may replace
epoch          monotonic fencing version; bumped on every renew/adopt
updated_at     unix seconds
```

## Semantics (v1, deliberately small)

- A pod handling a scope first looks up a valid lease → **adopts** the
  existing sandbox (no new e2b create), renews ownership to itself.
- No valid lease → create + hydrate locally, then `AcquireSandboxLease`.
  A lost race (another replica just won) closes the local copy and adopts the
  winner's sandbox.
- Every tool use on a locally cached executor renews the lease (owner = this
  pod, TTL default 15 min).
- Release/eviction deletes the lease only when `owner = this pod` **and** the
  `epoch` this pod last received still matches (fenced delete). If another
  pod adopted in between, the evicting pod drops its local reference
  **without** destroying the shared sandbox. Registry errors fail open: the
  sandbox is left alive.
- Adoption does not replay hydration (creator hydrated the same scope);
  skill/workspace changes apply on next recreate, same as single-pod behavior.

## Hardening: CAS + epoch (stage 2)

Cross-pod correctness needs the ownership transfer itself to be a
compare-and-set, not a read-then-write:

- Every successful renew/adopt increments `epoch` and only matches
  `scope_key + sandbox_id + expires_at > now`. A stale renew (row deleted or
  pointing at another sandbox) returns no row and must not be treated as
  ownership.
- Destroy is only allowed after
  `DELETE … WHERE scope_key=? AND owner=? AND epoch=?` returns one row. The
  evicting pod carries the epoch it last received, so a delayed/duplicated
  eviction from an older snapshot can never delete a lease that was renewed
  or adopted in the meantime.
- `Acquire` claims a fresh/expired row and stamps `epoch = 1`.

Why epoch on top of owner: owner changes cover most takeovers, but not
same-owner request reordering (an old release racing a newer renew from the
same pod) or a delete formulated before another pod's adoption completed.
The version column makes any stale destroy request fail closed.

### Staged hardening plan

| Stage | Scope | Status |
|---|---|---|
| 1 | lease table + adopt/acquire/release + gateway wiring | done (`e359bf0`) |
| 1b | per-use reconcile: cached executor vs lease sandbox_id | done (`091c579`) |
| 2 | CAS + epoch on renew/adopt/release + race unit tests | done (`86fcac1`) |
| 3–6 | heartbeat/reconciliation loop, degraded state machine, metrics, liveness GC | **not in scope** — decision: CAS + epoch is sufficient for the current release; revisit only if prod observations justify them |

## Files

- `internal/sandbox/lease.go` — port + lease record + default TTL
- `internal/store/sandbox_leases.go` — Postgres/sqlite adapter (DBStore)
- `internal/sandbox/e2b_executor.go` — pool adopt/acquire/release integration
- `internal/gateway/userspace.go` — pool wiring: per-pod owner id + lease store

## Rollout

- No manual migration needed: boot `Migrate()` runs
  `CREATE TABLE IF NOT EXISTS sandbox_leases (...)` (including `epoch`) on
  both dialects. The table ships only with this feature branch — nothing has
  been released, so there is no pre-epoch table to upgrade.
- Verify after rollout: gateway log
  `system sandbox executor pool created backend=e2b ... sharedLeases=true`;
  one session should produce one `e2b sandbox created` even when requests hit
  several pods (later hits log `e2b sandbox adopted from shared lease`).

## Security note (v1)

`envd_token` is stored in plaintext in `sandbox_leases`. It is short-lived
(bounded by the e2b sandbox lifetime) and rows expire/are deleted on release.
Encrypting the token at rest is a possible follow-up if this table is deemed
sensitive.

## Known tradeoffs (accepted)

- After a pod crash, its lease expires within TTL (default 15 min); another
  pod may create a fresh sandbox for that scope while the orphan still lives
  until e2b's own 30-min timeout.
- Adoption races are benign for correctness of destruction (owner check), but
  two pods briefly sharing one sandbox is expected during takeover windows.
- In the rare double-race where a creator loses `Acquire` and the subsequent
  CAS adoption also misses, the pool keeps its own unregistered sandbox until
  the next reconcile; because no epoch was recorded, its later release will
  not destroy it and it lives until the e2b timeout. Logged as a warning;
  accepted for v1.
