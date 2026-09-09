# Cross-pod E2B sandbox lease registry

> **Status**: implemented, **unreleased** (feature branch
> `feat/e2b-sandbox-leases`)
> **Storage**: Postgres (`sandbox_leases`) in production; sqlite in tests
> **Last updated**: 2026-09-09
> **Decision owner**: mengmengmengqiang@gmail.com
> **Reviewed by**: mengmengmengqiang@gmail.com (2026-09-09)
> **Commits**: see `feat/e2b-sandbox-leases` git log; latest doc revision
> `85b17fa`
> **Open follow-ups**: none — rotation runbook:
> [sandbox-secret-rotation.md](./sandbox-secret-rotation.md).

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

## Alternatives considered

- **No sharing (status quo)** — each pod creates its own E2B instance per
  scope. Rejected: this is the problem (N pods → N instances, observed with
  10 replicas), and retries/crash recovery only make it worse.
- **Sticky/affinity routing** — pin a session to one pod so only that pod
  creates its sandbox. Rejected: load imbalance, pod loss still strands or
  duplicates instances, and it requires router changes that do not fix the
  underlying correctness question (who may destroy an instance).
- **Redis lease/lock** — rejected: Redis is an optional dependency in this
  codebase (refresh locks, pub/sub), so making leases require it would break
  single-node/sqlite deployments; and a Redis lock would still need
  hand-rolled fencing (a versioned token) to match the CAS guarantees the
  relational table provides. The lease row also belongs next to the rest of
  the per-agent state in the same store.
- **Reuse the workspace blob store** — rejected: it is an artifact store
  without transactional compare-and-set; lease ownership needs CAS semantics,
  which the relational table provides.

## Semantics (v1, deliberately small)

- A pod handling a scope first looks up a valid lease → **adopts** the
  existing sandbox (no new e2b create), renews ownership to itself.
- No valid lease → create + hydrate locally, then `AcquireSandboxLease`.
  A lost race (another replica just won) closes the local copy and adopts the
  winner's sandbox.
- Before every use of a cached executor the pool reconciles against the
  shared lease and renews under this pod's ownership (TTL default 15 min)
  when the sandbox is unchanged.
- Release/eviction deletes the lease only when `owner = this pod` **and** the
  `epoch` this pod last received still matches (fenced delete). If another
  pod adopted in between, the evicting pod drops its local reference
  **without** destroying the shared sandbox. Registry errors fail open: the
  sandbox is left alive.
- Adoption does not replay hydration (creator hydrated the same scope);
  skill/workspace changes apply on next recreate, same as single-pod behavior.

### Failure semantics (registry errors fail open)

Fail-open means "the sandbox is left alive", but each operation degrades
differently. This table is the contract; each row maps to its test.

Guiding principle: **availability first, destruction right strictly gated**
— without proof of ownership (a matching epoch) a pod must never close a
sandbox, even if that means leaking one until TTL/expiry.

| Registry condition | Pool behavior | Test |
|---|---|---|
| No registry failure (baseline) | Acquire/renew/adopt succeed with a fresh epoch; Release with the matching epoch deletes the row and closes the sandbox exactly once | `TestSandboxLeaseAcquireAdoptRenewRelease`, `TestE2BPoolReleaseUsesFencingEpoch` |
| Token decrypt failure on read (rotated / mismatched key) | Read fails closed → pool treats it as a lookup error and creates locally; an unexpired row blocks registration until TTL | `TestEncryptedSandboxLeaseStoreRotation` |
| Token encrypt failure before write | Refuses to persist (never writes plaintext); the error propagates to the pool's acquire-error path, which keeps the local sandbox unregistered | pool acquire-error path: `TestE2BPoolFreshGetLeaseErrorsFailOpen` |
| Lookup error before local create | Still creates + registers locally; an acquire error keeps it unregistered | `TestE2BPoolFreshGetLeaseErrorsFailOpen` |
| Adopt renew error | Uses the adopted executor but records **no epoch**, so release can never destroy it | `TestE2BPoolAdoptRenewErrorKeepsExecutorWithoutEpoch` |
| Reconcile lookup error (cached) | Keeps the cached executor; no renew, no close | `TestE2BPoolReconcileRegistryErrorsKeepLocal` |
| Reconcile reclaim acquire error | Keeps the cached executor, unregistered | `TestE2BPoolReconcileRegistryErrorsKeepLocal` |
| Double race (Acquire lost + adoption CAS miss) | Keeps the local sandbox unregistered until the next reconcile | `TestE2BPoolCreateLostRaceAdoptMissKeepsLocalUnregistered` |
| Release / CloseAll registry error or declined delete | Drops the local reference; the sandbox stays alive | `TestE2BPoolReleaseRegistryErrorLeavesSandboxAlive`, `TestE2BPoolCloseAllHonorsLeaseStore` |

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
- `internal/store/sandbox_leases.go` — Postgres/sqlite adapter (DBStore) +
  `EncryptedSandboxLeaseStore` (at-rest token encryption decorator)
- `internal/cryptoutil/cipher.go` — neutral at-rest credential cipher
  contract shared with MCP OAuth (`port.Cryptor` aliases it)
- `internal/sandbox/e2b_executor.go` — pool adopt/acquire/release integration
- `internal/gateway/userspace.go` — pool wiring: per-pod owner id + lease store
- `docs/sandbox-secret-rotation.md` — key rotation runbook

## Rollout

- **Prerequisite**: `FASTAGENT_OAUTH_SECRET` must be configured. Without it
  the gateway disables shared leases and falls back to per-pod sandboxes —
  plaintext `envd_token` rows are never written.
- No manual migration needed: boot `Migrate()` runs
  `CREATE TABLE IF NOT EXISTS sandbox_leases (...)` (including `epoch`) on
  both dialects. The table ships only with this feature branch — nothing has
  been released, so there is no pre-epoch table to upgrade.
- Verify after rollout: gateway log
  `system sandbox executor pool created backend=e2b ... sharedLeases=true`;
  one session should produce one `e2b sandbox created` even when requests hit
  several pods (later hits log `e2b sandbox adopted from shared lease`). If
  the log shows `sharedLeases=false`, re-check that `FASTAGENT_OAUTH_SECRET`
  is configured before debugging further.
- CI coverage: `.github/workflows/go-test.yml` runs the sandbox/store/gateway
  suites against a Postgres service on every push/PR; the live E2B job runs
  only when `E2B_API_KEY`/`E2B_TEMPLATE` secrets exist.

## Security note (v1)

`envd_token` is encrypted at rest with AES-256-GCM, using the same master
secret as MCP OAuth tokens (`FASTAGENT_OAUTH_SECRET`, derived to a 32-byte
key via SHA-256; see `internal/mcp/oauth/adapter/cryptor.go`). The ciphertext
is **base64-encoded into a universal `TEXT` column** — no `BLOB`/`BYTEA`, no
dialect branching, identical schema on sqlite and Postgres (the same
dialect-neutral approach used elsewhere in this repository). Encryption is
applied by `store.EncryptedSandboxLeaseStore`, assembled at the gateway
composition root; `DBStore` itself stays key-agnostic.

- Shared leases require `FASTAGENT_OAUTH_SECRET` to be set. Without it the
  gateway logs a warning and keeps per-pod sandboxes — plaintext `envd_token`
  rows are never written.
- Rotating the secret makes existing rows undecryptable. Reads fail closed at
  the wrapper, the pool treats it as a registry lookup error and falls back
  to a local create (fail-open), and the stale row is reclaimed on expiry or
  takeover. Rotation is therefore safe but leaves orphaned sandboxes until
  their E2B timeout — follow
  [sandbox-secret-rotation.md](./sandbox-secret-rotation.md).

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

## Test topology

**Last reviewed**: 2026-09-09

Test layers mirror the production dependency direction (policy → port →
adapter → composition root), so each seam is exercised without pulling
outer layers inward:

Why this shape: policy tests must never need a database or network, adapter
tests must exercise real SQL (not mocks), composition-root tests only assert
wiring decisions, and the live e2e is the single place that pays for real
external dependencies.

- **Policy (sandbox package)** — `lease_pool_test.go` drives the pool with a
  `fakeLeaseStore` that implements `SandboxLeaseStore` and can inject errors
  per operation. Covers every fail-open path: lookup/acquire/adopt-renew/
  release/CloseAll failures keep sandboxes alive; reconcile registry errors
  keep the local executor; the double race (Acquire loss + CAS adoption miss)
  keeps the local sandbox unregistered. No database involved.
- **Adapter (store package)** — `sandbox_leases_test.go` runs `DBStore`
  through `sandbox.SandboxLeaseStore` against real sqlite: renew CAS miss,
  stale/missing release no-ops, monotonic epoch. `sandbox_leases_postgres_test.go`
  covers the production dialect (concurrent acquire single winner, stale
  release fencing, idempotent `Migrate`) and is gated by
  `FASTAGENT_TEST_PG_DSN` — sqlite serializes writes, so cross-connection
  semantics are only proven on Postgres. `sandbox_leases_crypto_test.go`
  covers the encryption decorator: round-trip with no plaintext in the raw
  row, wrong-key fail-closed, and rotation (old-key rows unreadable → new
  key reclaims after TTL) on sqlite, with a Postgres variant gated by
  `FASTAGENT_TEST_PG_DSN`.
- **Composition root (gateway package)** — `sandbox_pool_lease_test.go` tests
  the pure `sandboxLeaseOpts` decision (nil store / missing owner ⇒ no shared
  lease) and `buildSystemSandboxPool` wiring without network access.
- **Live e2e (`TestE2BPoolCrossPodAdoption`)** — requires `E2B_API_KEY` and
  `E2B_TEMPLATE`; in CI a missing credential set fails the test instead of
  silently skipping. Each "pod" opens its own `DBStore` handle over the same
  underlying database (Postgres when `FASTAGENT_TEST_PG_DSN` is set,
  otherwise two sqlite connections to one `?cache=shared` file). Stages:
  1. Pod A creates a sandbox, registers the lease, and writes a marker into
     `/workspace`.
  2. Pod B adopts the same `sandbox_id` (epoch bumped) and must read the
     marker back — proving it runs on the same E2B instance, not a second
     sandbox with a copied lease row.
  3. Pod A releases with its stale epoch: the lease must survive and Pod B
     must still execute and still read the marker.
- **CI** — `.github/workflows/go-test.yml` runs the sandbox/store/gateway
  suites against a Postgres service; a second job runs the live E2B e2e only
  when `E2B_API_KEY`/`E2B_TEMPLATE` secrets exist.

### How to run

```bash
# Unit + adapter + gateway (no external deps)
go test ./internal/sandbox/ ./internal/store/ ./internal/gateway/ -count=1

# Postgres semantics (start any local PG first)
FASTAGENT_TEST_PG_DSN='postgres://postgres@localhost:5432/postgres?sslmode=disable' \
  go test ./internal/store/ -run Postgres -count=1 -v

# Encryption decorator + rotation on Postgres (same env gate)
FASTAGENT_TEST_PG_DSN='postgres://postgres@localhost:5432/postgres?sslmode=disable' \
  go test ./internal/store/ -run '^TestEncryptedSandboxLeaseStorePostgres$' -count=1 -v

# Live E2B cross-pod adoption (requires credentials)
E2B_API_KEY='...' E2B_TEMPLATE='...' \
  go test ./internal/sandbox/ -run '^TestE2BPoolCrossPodAdoption$' -count=1 -v
```
