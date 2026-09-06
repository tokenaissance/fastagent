# Cross-pod E2B sandbox lease registry

Status: implemented (v1) · Storage: Postgres (`sandbox_leases`), sqlite in tests

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
- Release/eviction deletes the lease only `WHERE owner = this pod`; if
  another pod adopted in between, the evicting pod drops its local reference
  **without** destroying the shared sandbox. Registry errors fail open: the
  sandbox is left alive.
- Adoption does not replay hydration (creator hydrated the same scope);
  skill/workspace changes apply on next recreate, same as single-pod behavior.

## Files

- `internal/sandbox/lease.go` — port + lease record + default TTL
- `internal/store/sandbox_leases.go` — Postgres/sqlite adapter (DBStore)
- `internal/sandbox/e2b_executor.go` — pool adopt/acquire/release integration
- `internal/gateway/userspace.go` — pool wiring: per-pod owner id + lease store

## Rollout

- No manual migration needed: boot `Migrate()` runs
  `CREATE TABLE IF NOT EXISTS sandbox_leases (...)` on both dialects.
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
