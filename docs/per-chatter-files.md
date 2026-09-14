# Per-chatter files: whose MEMORY.md is that?

> **Status**: fixed. Two halves: the missing-row read path in
> `internal/agent/tools` (a miss is an empty file) and the automatic-turn
> identity in `internal/agent/loop.go` (the turn writes the memory of the
> account it acts for).
> **Trigger**: operator report — `edit_file('MEMORY.md')` answered
> `system file get: store: not found` while the file clearly had content.

## The rule

`USER.md` and `MEMORY.md` are **per-chatter**: every read/write is a strict
lookup of `agent_files(agent_id, user_id, filename)` —
`GetAgentFileExact`, no owner overlay, because a public-link visitor must never
inherit the owner's accumulated memory. Everything else in the identity set
(`SOUL.md`, `IDENTITY.md`, `AGENTS.md`, `BOOTSTRAP.md`, `TOOLS.md`,
`HEARTBEAT.md`) is the agent's shared template and *does* fall back to the
owner's row.

So "which file is this?" is answered by *who the turn is*.

## Failure 1 — a missing row was a hard error

`read_file` always resolved DB → agent home on disk → empty. `edit_file`
resolved DB only and turned `store.ErrNotFound` into
`fmt.Errorf("system file get: %w", err)`, handing the model a storage-internal
string. A chatter who had never saved MEMORY.md — i.e. everyone, on their first
write — could not edit it, while `read_file` on the same path answered happily.

Both paths (host and sandbox, `edit_file` / `read_file` / `apply_patch`) now go
through `Registry.readSystemFileWithFallback`: **a missing row is an empty
base**, not an error. The edit then either applies or fails with an actionable
`old_string` miss that names the file.

## Failure 2 — automatic turns were not the owner

Cron/heartbeat/goal/webhook turns stamp a sentinel `UserID` (`"cron"`,
`"system"`, `"goal"`, `"webhook"`) instead of an account. The gateway mints an
app_user for any non-`u_` id (`web:cron` → `u_cd824…`), so per-chatter state
keyed on it lands in a row no conversation ever reads — and, before the fix
above, made every `edit_file('MEMORY.md')` fail.

Production repro (2026-09-14, `agt_cda27bb…`, session *Kronos Kline Forecast
Model*): the agent's own `*/5` cron re-entered its session as `UserID="cron"`.
In that one session the operator's turn returned
`Edited MEMORY.md (1 replacement(s))`; the cron turn returned
`system file get: store: not found` four times, and `read_file('MEMORY.md')`
answered with an empty string. The row itself existed all along — under the
operator's `u_396a1f88…`, 18 kB, containing the very `old_string` the model was
patching.

`Agent.chatterUserID` now routes those turns to the account they act for:

| Source | Identity used for per-chatter state |
|---|---|
| real user (web / IM chatter) | the chatter's `u_xxx`, unchanged |
| cron | the job's `owner_user_id`, else the agent owner |
| goal continuation | the goal's `owner_user_id`, else the agent owner |
| heartbeat | the agent owner (the tick carries no owner field) |
| webhook | the token/body `userId`, else the agent owner |
| sub-agent | **not** listed — it inherits the parent turn's chatter |

Without this half the write "succeeds" into the synthetic cron user's private
row, which is why both halves ship together.

## Who may read the agent home on disk

`systemRoot/MEMORY.md` is **one un-scoped mirror per agent** — whoever wrote
last owns its bytes, and in practice that is the owner's private memory. The
disk fallback is therefore restricted the same way `ContextBuilder.loadFileForUser`
and `memory_store_adapter.GetMemory` already restrict it:

* shared identity files — readable by any caller allowed to touch them (the
  overlay hands chatters the owner's copy anyway);
* per-chatter files — only the account that owns the agent home. A visitor
  whose row does not exist yet sees an empty profile/memory, never the owner's.

## Tests

Unit (port boundary, fake `SystemFileStore`):

| Test | Locks |
|---|---|
| `TestEditFileMissingMemoryRowDoesNotLeakStoreError` | no raw `store: not found`; the error names the file |
| `TestEditFileOwnerUsesDiskBaseWhenRowIsMissing` | disk-only MEMORY.md edits from the disk base and saves to the row |
| `TestPerChatterFileNeverInheritsTheOwnersDiskCopy` | visitor read/edit cannot see or write the owner's mirror |
| `TestOwnerReadsDiskCopyWhenRowIsMissing` | the owner still reads the legacy/mirror disk copy |
| `TestSharedIdentityFileFallsBackToDiskForNonOwnerCaller` | SOUL.md etc. keep the inherited template path |
| `TestChatterUserID_AutonomousTurnsActAsOwner` | the source→identity table above, including sub-agent pass-through |

Cloud-path e2e (real `DBStore` + `MemoryStoreAdapter`, real file tools):

| Test | Locks |
|---|---|
| `TestAutonomousTurnMemoryRouting_CloudPathE2E` | cron (gateway-minted `u_cd824…` **and** raw sentinel) and heartbeat turns edit the operator's row; no stranded synthetic row |
| `TestVisitorTurnMemoryStaysPrivate_CloudPathE2E` | an IM visitor reads empty, keeps their own row, and the owner's row/mirror stay untouched |
