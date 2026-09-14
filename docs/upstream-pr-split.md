# Split the lease implementation into upstream PRs

> **Status**: analysis done, nothing ported yet — this is the plan the next
> session executes.
> **Why**: PR [#124](https://github.com/fastclaw-ai/fastclaw/pull/124) carries
> the *design* (`docs/sandbox-pool-leases.md`) from this fork to
> `fastclaw-ai/fastclaw`. The implementation lives on this line and has to be
> split into reviewable slices before it is offered upstream.
> **Measured**: 2026-09-14, against `origin/dev` = `ad270ee` (upstream snapshot
> + the design doc), i.e. the PR's own base.

## What the measurement showed

The implementation cannot be lifted onto the PR base as-is. Three concrete
blockers, each verifiable:

1. **Module drift.** The feature needs `golang.org/x/crypto v0.46.0` for the
   AES-GCM secret decorator; the PR base's `go.mod` does not carry it, and
   `GOPROXY=direct` from this network cannot resolve `golang.org/x/*` at all
   (it timed out during the experiment; a proxy fixed it). Any slice that
   touches the store therefore also carries a `go.mod`/`go.sum` bump, which
   reviewers should see called out.
2. **Missing support package.** `internal/store/sandbox_leases.go` imports
   `internal/cryptoutil`, which does not exist on the PR base (it is 16 lines
   and depends only on the standard library, so it travels with slice 1).
3. **The table does not exist there.** The PR base's `database.go` has zero
   occurrences of `sandbox_leases`: the `CREATE TABLE`, the `state`/`paused_at`
   retrofit (`migrateSandboxLeasesAddState`) and the migration call are all
   part of this line's work, and `database.go` itself has diverged heavily, so
   those hunks must be re-applied by hand rather than copied wholesale.

The pool and gateway slices sit on top of the newer sandbox lifecycle
(`internal/sandbox/{lease,pool,lifecycle}.go`, ~12k lines in the package today),
which is a wider surface than the store slice and depends on the same package
layout upstream does not have yet.

## Slice plan

Each slice must build and pass its own tests on its own base — a slice that
needs the next one is not a slice.

| # | Slice | Files | Verification on the ported base |
|---|---|---|---|
| 1 | **store/adapter** — the lease table and the encrypted store | `internal/cryptoutil/cipher.go` (new, 16 lines), `internal/store/sandbox_leases*.go` (+ its tests), `CREATE TABLE sandbox_leases` + `migrateSandboxLeasesAddState` + the migrate() call, `go.mod`/`go.sum` (`x/crypto`) | `go test ./internal/store/ -run 'SandboxLease\|Encrypted'` |
| 2 | **pool policy** — one lease per scope, CAS adoption, pause/resume, rebuild publish | `internal/sandbox/lease.go`, `pool.go`, `lifecycle.go` + their tests | `go test ./internal/sandbox/ -run 'Lease\|Pool\|Lifecycle'` |
| 3 | **gateway/config wiring** — who gets a pooled executor and how it is configured | `internal/gateway/sandbox_pool*.go`, user-space builder, `internal/config` sandbox-pool fields, `cmd/fastclaw/cmd_sandbox.go`, helm values | `go test ./internal/gateway/ -run SandboxPool` |

Slices are stacked: 2's base is 1's branch, 3's base is 2's. Upstream sees three
PRs whose diffs are each about one concern, not one 12k-line PR.

## Sequencing options

1. **Port now** (slice 1 first). ~one focused session for slice 1 including the
   DDL transplant; slices 2–3 are larger because they carry the lifecycle
   surface.
2. **Advance the base first** — get upstream's `dev` and this fork's `dev` onto
   a common, newer point (merge upstream into the fork's line, or land a
   mechanical sync commit), then the same slices shrink from "port" to
   "rebase". This is the cheaper route *if* upstream has been moving.
3. **One feature PR from the fork's dev** — rejects the split's purpose
   (reviewability) but is the only way to ship without porting. Listed for
   completeness, not recommended.

Recommendation: try 2 first (cheap to check: how far apart are the two `dev`s
now?), fall back to 1.

## Rules for whoever executes this

* Never open a PR from a tree that does not build. The PR base must compile and
  its targeted tests must pass, in a worktree, before the branch is pushed.
* Keep the design doc (`docs/sandbox-pool-leases.md`) as the reference the PRs
  link to; it is already upstream via #124, so the slices do not need to
  re-explain the design, only the code.
* Freeze a snapshot of the sandbox files when the port starts: the feature line
  is still moving (uncommitted work in `internal/setup`, `internal/store`),
  and upstreaming a moving target produces unreviewable diffs.
