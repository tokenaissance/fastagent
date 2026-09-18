# 10 · Whole-harness state-change audit: triggered by the agent & triggering the agent

> Status: audit (2026-09-18) · Method: the formal statement in [08](./08-state-observability-principle.md) §2
> (δ = a fact that the world changed, σ = the agent-readable signal) plus its §6 checklist
> Scope: **every** component in `fastagent` that changes the agent's world, or that pulls the agent
> into running a turn
> Relation to 09: 09 audits only the sandbox lifecycle (G1–G4); this document is its complement and
> overview, and adds G5–G13
> Evidence levels: **【code】** the code path was read (`file:line`) · **【test】** pinned by a test ·
> **【probe】** reproduced by a local probe (§7 has a re-runnable one) · **【prod】** production data

> ## ⚠️ Deployment status (recorded 2026-09-18; read this before the rest)
>
> **Every "fixed (2026-09-18)" below means fixed in the WORKING TREE: uncommitted and undeployed.**
> `HEAD` is `16a7532` (2026-09-17); the working tree is +2163/−359 across 76 files. The difference is not
> a detail — it is **whole mechanisms that do not exist on the deployed side**:
>
> | Mechanism | HEAD (i.e. production, if nothing else was deployed) | Working tree |
> |---|---|---|
> | write-through (`WriteThrough` / `writeThroughSignal`) | **absent** (only the old coding-only `mirrorCodingWriteToSandbox`) | present |
> | the sync's `BLOCKED` / `CompareResult` / `StoreOnlyLine` (the G4–G7 signals) | **absent** | present |
> | the sync's criterion | **skip when `Stat().Size == len(data)`, otherwise `Put` the sandbox's copy over the store** — the incident's original mechanism | `size+mtime` → bytes → refuse and report on divergence |
> | persisting the unhydrated fact (G19) | absent (`unhydrated` is in-process only) | present (`sandbox_leases.unhydrated`) |
> | identity/config/cron sampling in the environment signal (G8/G9/G10), the snapshot on the receipt (G20) | absent | present |
> | the panel delete's path convention (G21), the sync's scope collapse (A), the preview container's addressing (G) | **all still in their buggy shape** | fixed |
>
> **So "what production does today" cannot be inferred from the "fixed" markers below — read HEAD instead:**
>
> * **The incident's root cause is still live**: a host write whose mirror did not land (loose sessions
>   never mirrored) is overwritten by the sandbox's older copy on the next sync — **the 2026-09-17
>   incident can still reproduce** (HEAD's criterion skips on equal size, so it is intermittent and
>   size-dependent).
> * **The panel delete is still a silent no-op** (G21 is present in HEAD as-is).
> * **Project sessions are still producing `<pid>/<chat>/…` duplicates** (A), and still growing them —
>   the cleanup script should run *after* A ships.
> * G22 (the stamp missing) is **not** a production issue: HEAD has no write-through at all; it was a
>   defect in undeployed code and is fixed here.
>
> If production runs something other than HEAD (another branch, an older image), re-derive this table from
> the deployed code. The quickest discriminator is two production probes: ① delete a file in the panel and
> see whether the row comes back on refresh; ② upload a file and see whether `exec ls` finds it in the same
> session. Each answer picks a column of the table above.

## 0. The classification criterion

The principle asks one thing: **when this change happened, could the agent's `Belief` drift away from
reality without it being able to notice?**

Split by "who triggered it": the two classes have different criteria.

| Class | Trigger of δ | Criterion | Satisfied for free? |
|-------|--------------|-----------|---------------------|
| **A: triggered by the agent** | the agent's own tool call | the tool result must be a **complete receipt**: not "the call succeeded" but "here is what the world became" | 08 §2 says this holds by construction — **but only under that condition**. Without the second half, class A violates the principle just as well |
| **B: triggering the agent** | the harness (cron / heartbeat / goal / subagent), the user (web / IM / API / webhook), external systems (MCP servers, providers, other writers of the store) | a σ must exist **and** be delivered in a channel that turn actually reads; slog does not count | ❌ not satisfied by default |

**A third class** is the easiest to miss: changes that are neither triggered by the agent nor
trigger the agent (the user edits config in a panel; another session or another pod edits the same
agent's files). For the principle they are **exactly like class B** — any change to the agent's world
must be perceivable, and who triggered it does not change the criterion.

## 1. The whole picture

The σ column holds the sentence the agent actually reads; the exit column says which delivery point
it left through (the three exits of 08 §9.1).

### Class A: triggered by the agent

| Component | δ (world change) | σ (what the agent sees) | Exit | Verdict |
|-----------|------------------|-------------------------|------|---------|
| read-only tools (`read_file` / `list_dir` / `web_search` / `web_fetch` / `knowledge_search` / `memory_search` / `get_billing_usage` / `load_skill`) | none | — | — | ✅ no signal needed |
| `write_file` (store route) | store write + sandbox write-through (on docker there is only one copy) | an **always-false** `[workspace] … is too large to compare … (over 2 MiB)` | tool result | ❌ **G5 (P0, every backend)** |
| `edit_file` (store route) | same | on a hit, `[workspace] your write … replaced a different version (N bytes)`; silent otherwise | tool result | ✅ correct (the only call site that passes the pre-image down) |
| `apply_patch` (store route) | same, per file | the same false σ as G5; **never** says "replaced a different version" | tool result | ❌ **G5 + G6** |
| `exec` (sandbox) | the sandbox's `/workspace` rewritten by a command | post-exec sync's `[workspace] the sandbox changed … / NOT synced …` | tool result | ✅ settled by 09/07 |
| `exec` (host `host_exec`) | arbitrary paths on the host filesystem | the command's stdout only | tool result | ✅ (no copy to diverge: the host FS is the single copy, and reading back confirms it) |
| `spawn_subagent` / `delegate_task` | a child agent keeps changing files/memory in the same scope | the parent turn gets the child's text; file changes arrive via the next exec sync signal | tool result (synchronous wait) | ✅ 08 §5.1 |
| `message` | one message sent into a channel | an "sent" receipt | tool result | ✅ |
| `image_gen` / `tts` | a file is produced and attached to the reply | the receipt carries the path | tool result | ✅ |
| `create_cron_job` / `list_cron_jobs` / `delete_cron_job` | a future **automatic turn** is registered/withdrawn | the receipt (job id, schedule) | tool result | ✅ receipt; but see **G10** (when something else deletes it) |
| `update_goal` | goal state / budget | the receipt; a separate `BudgetLimitPrompt` when the budget runs out | tool result + turn prompt | ✅ |
| `set_preference` / `set_timezone` | scope preferences / `USER.md` (a system file) | the receipt | tool result | ✅ (it wrote them itself, C1) |
| MCP config tools (`mcp add/remove/…`) | the tool set changes | the receipt + the next turn's `[Environment changes …] tools now / no longer available` | tool result + turn prompt | ✅ 2026-09-18 |
| skill install/generation (`skill install`, the SkillsLearner background extraction) | the skill set changes | the next turn's `[Environment changes …] skills added / removed / changed` | turn prompt | ✅ 2026-09-18 |
| memory writes (the memory tool / the heartbeat review) | `MEMORY.md` content changes | the next turn's `[Environment changes …] long-term memory was rewritten/created/CLEARED` | turn prompt | ✅ 2026-09-18 |
| identity-file writes (`write_file('SOUL.md', …)` etc.) | part of the system prompt is rewritten | the receipt (it wrote them itself) | tool result | ✅ when self-written; **when written from the outside, see G8** |
| `exec run_in_background` / sandbox background jobs | the process keeps writing files after the tool result returned; its exit is a δ of its own | file changes are reported by the next sync; the **exit is not pushed**, but every `bash_output` call **recomputes from the world** and answers `[status] exited (code=N)` / `killed` / `lost — the sandbox was replaced` | tool result (the consumer's next read) | ✅ **a pull-shaped σ** (the criterion is recomputable ⇒ place 1, no carrier needed; see §4 G13) |

### Class B: triggering the agent

| Trigger | δ | σ | Exit | Verdict |
|---------|---|----|------|---------|
| user message (web / IM / API / webhook) | a new input appears in the session | the message itself is the context | turn input | ✅ |
| **cron** fires a turn | one automatic turn | `[Cron Job: name]` marks the source; stays in the session history | turn input | ✅ 【code】`internal/cron/scheduler.go:289,463` |
| **heartbeat** fires a turn | same | `[Heartbeat — ts]` plus the body of HEARTBEAT.md | turn input | ✅ 【code】`internal/agent/heartbeat.go:66-90` |
| **goal continuation** | the goal is unfinished, the turn continues automatically | the synthesised goal_context message stays in the history | turn input | ✅ 【code】`internal/agent/goal/continue.go:50-68` |
| **a subtask completes** | the parent turn is waiting | the result *is* the parent turn's tool result | tool result | ✅ 08 §5.1 |
| the sandbox is replaced / times out (provider side) | `/workspace` is rebuilt | `[workspace] the sandbox was REPLACED …` (delivered once) | tool result | ✅ 09 G1/G2 |
| a user uploads a file (`POST /api/n`) | the store gained a file; **the live sandbox did not** | **none** | — | ❌ **G7** |
| a user deletes a file (`DELETE /api/n/{path}`) | the store lost a file; **the live sandbox still has it** | **none** | — | ❌ **G7** |
| an outside writer edits identity/system files (panel, another session, another pod) | the next turn's system prompt differs | **none** (the per-turn sample covers skills/tools/memory only) | — | ❌ **G8** |
| an outside writer edits agent config (model, prompt mode, skill switches…) | the runtime looks different | **none** (unless it happens to change a tool or skill name) | — | ❌ **G9** |
| an outside writer deletes/edits a cron job or `HEARTBEAT.md` | "I will be woken at X" becomes false | **none** | — | ❌ **G10** |
| an MCP server changes its tool list (`notifications/tools/list_changed` etc.) | the server's capabilities changed | **none** (no notification handling at all) | — | ❌ **G11** |
| an automatic turn is deferred because the session is busy / dropped on timeout | a turn that should have run did not (the scheduler does **not** re-deliver: it advances the next run as it fires) | one slog line per drop (agent / chat / source / how long / first line of the text) + **a note in that chat for cron** | the session's channel | ✅ 2026-09-18 (was ❌ G12) |
| context compaction / tool-result clipping / an interrupted turn | history was replaced | the summary and the clip placeholder both state what happened | turn input | ✅ 08 §5 |
| provider-account fallback (`providerForAgent` falls back to the shared provider on three of its four paths, 【code】`provider_fallback_log_test.go:3-10`) | the request goes out on a different upstream account (the model name is unchanged) | a slog warning only | — | ⚠️ an operations-side fact: not part of the agent's world model, but it illustrates the same lesson — "something was swapped quietly and only the log knows" |

## 2. Class A in detail

### 2.1 Workspace writes: three tools, one exit, one false signal (G5, P0; **fixed 2026-09-18**)

**Symptom (【probe】 reproduced locally)**: calling `write_file('notes.md', 'hello')` against a real
`LifecyclePool` (RemoteWorkspace + workspace.Store) yields:

```
Written 5 bytes to notes.md

[workspace] notes.md is too large to compare automatically, so the sandbox copy was
replaced without checking it (over 2 MiB). If a script edits a file this large, verify
the result inside the sandbox with exec before moving on.
```

A **5-byte** file is told it is over 2 MiB. `apply_patch` does the same. `edit_file` behaves (it is
silent).

**And the docker backend does it too** (【probe】 a second probe swaps in an executor that does *not*
implement `sandbox.RemoteWorkspace` — i.e. the docker shape — and the output is byte-for-byte the
same). On docker `/workspace` *is* the host directory and there is **only one copy**; nothing can be
"replaced without checking" — the sentence does not even have a referent there, yet it is attached to
every `write_file`.

**Cause (two behaviours that are each pinned by their own test, composed)**:

1. `sandbox.WriteThrough` only compares when the caller supplies "what the sandbox copy was supposed
   to hold" (`previous`); without it, it returns `Compared=false`
   (【test】 `TestSyncContract_WriteThroughWithoutExpectationClaimsNothing`).
2. The tools layer reads `Compared=false` **uniformly** as "over the fingerprint cap": the
   `case !outcome.Compared:` branch at `file.go:920` emits the "(over 2 MiB)" sentence.
3. And `write_file` (`file.go:1080`) and `apply_patch` (`apply_patch.go:690`) **always pass `""`**;
   only `edit_file` (`file.go:1269`) passes the bytes it read.

So `Compared=false` carries two entirely different meanings ("no expectation was given" / "too large
to read"), and the exit reports one of them as the other.

**Why this is P0 and not a wording nit**: the value of a σ is that it is **true**. A σ that is always
false is worse than silence:

- it teaches the model a false fact ("I did not verify this large file"), and the model's subsequent
  behaviour depends on it (it will go and double-check with `exec`);
- it appears on **every** `write_file` / `apply_patch`, which is precisely the "always-on noise" that
  C3 in 08 §3 exists to prevent — once it is background noise, the real signal (a genuine replacement)
  gets skipped along with it.

**The fix (landed 2026-09-18)**: the bool became a set of **named facts**
(`sandbox.CompareResult`), one truthful sentence each — the exit no longer has to guess why it did not
compare:

| Fact | When it holds | The σ it produces |
|------|---------------|-------------------|
| `CompareNoSecondCopy` | docker: `/workspace` IS the host directory, there is no second copy | **silence** (it used to announce "over 2 MiB") |
| `CompareNothingReplaced` | the sandbox copy is what the caller expected / is already the content being written / could not be read | **silence** |
| `CompareReplacedVerified` | the sandbox copy is neither the caller's expectation nor the new content | "replaced a different version (N bytes)" |
| `CompareReplacedUnchecked` | the sandbox copy differs from the new content and there was no expectation to check it against | "the sandbox held a different version (N bytes); there was no earlier copy to compare it against" — **no more "over 2 MiB" claim** |

G6 alongside it: **the pre-image is actually passed down** — `write_file` reads the store's previous
object before writing (`previousStoreVersion`, capped at 2 MiB, and no expectation at all when the read
fails), and `apply_patch` carries the `old` content `runApplyPatch` had already read through
`plannedWrite`. `write_file` can now say "replaced a different version" for the first time; before
this, only `edit_file` could.

**Re-checked with the same instrument (2026-09-18)**: re-running the probe of §7 against the fixed
code gives

```
remote(e2b) write_file   : "Written 5 bytes to notes.md"      ← the false signal is gone
shared(docker) write_file: "Written 5 bytes to notes.md"      ← docker no longer claims "over 2 MiB"
after a script edited the file inside the sandbox, then write_file:
  "Written 2 bytes to notes.md
   [workspace] your write to notes.md replaced a different version in the sandbox (12 bytes). …"
                                                              ← G6: write_file reports it for the first time
```

The real backend was re-run too: `FASTAGENT_E2B_LIVE=1 go test ./internal/sandbox/ -run TestE2BLive -count=1`
passes 5/5 (including `TestE2BLiveRepro` and `TestE2BLiveWriteThroughReachesSandbox`).

**A fifth state (added 2026-09-18, G17/H)**: a **partial** success has to be stated. Inside a project a
write may reach only "this turn's container" while another container (a sibling chat's, or the
console-started preview's) misses it — the symptom is "I changed it and the preview does not move", and
the agent has no clue from the tool result. When `WriteThroughOutcome.BroadcastFailures` is non-zero the
result now says `[workspace] your write … landed in this sandbox, but N other sandbox(es) of this project
could not be updated — a preview running there may keep showing an older version. Writing the file again
retries them.` (C3: with every broadcast successful it stays silent; 【test】
`TestWriteFileStatesAPartialProjectMirror`).

### 2.2 Writing your own future world

The trait of this family: **the receipt only covers "the call succeeded"; the real consequence shows
up next turn** (a tool was added, a skill disappeared, memory was rewritten, the system prompt now
says something else). They are covered by the **turn-level exit** (the per-turn environment signal),
so the verdict is ✅, but one timing property is worth remembering:

| Change | This turn's receipt | Next turn's visibility |
|--------|--------------------|------------------------|
| MCP tools added/removed | yes | turn signal `tools now / no longer available` |
| skills added/removed/edited | yes | turn signal `skills added / removed / changed` |
| memory rewritten | yes | turn signal `long-term memory was …` |
| identity files (SOUL/IDENTITY/USER/AGENTS) | yes | **no signal at all** — but the agent wrote them, so C1 holds; for outside writes see G8 |
| preferences / timezone | yes | no signal (again self-written; what changes is next turn's date line / preference section) |
| goal | yes | `BudgetLimitPrompt` when the budget runs out |
| cron job | yes | no signal (see G10) |

## 3. Class B in detail

### 3.1 The four automatic turns all carry a source marker

What cron / heartbeat / goal continuation / subagent share is that **they wake the agent at a moment
it did not ask for**. Perceivability rests on two things: ① the turn itself is written into that
session's history (readable the next time history is read); ② the input carries a source marker
(`[Cron Job: …]`, `[Heartbeat — …]`).
【code】 `internal/agent/admission.go:45` lists these four sources, and `bus.SourceGoalContext` at
`loop.go:1292` decides that it is treated as a synthetic audit prompt rather than indexed into FTS.

### 3.2 User/API writes straight to the store: the one write path that does **not** write through (G7; its signal half **landed 2026-09-18**)

The agent's own write paths all write through into the sandbox (§2.1); a user uploading/deleting from
the panel takes a different route:

| Action | Code | Result |
|--------|------|--------|
| `POST /api/n` (upload) | `handleAgentFileUpload`, `internal/setup/handlers_agents.go:1445` (`Put` at :1503) | only `workspaceStore.Put`, **the live sandbox is not touched** |
| `DELETE /api/n/{path}` | `handleAgentFileDelete`, `internal/setup/handlers_agents.go:1512` (`Delete` at :1534) | only `workspaceStore.Delete`, **the sandbox copy stays** — **but see the 2026-09-18 correction below: today it deletes nothing at all** |

That produces a divergence today's sync rules can never repair: the sync's domain is the **sandbox
snapshot** (07 §3.11.3), so a file that exists only in the store is neither pushed back into the
sandbox nor deleted — and the agent gets **zero signals** about any of it. The divergence surfaces as
a contradiction between two read paths:

- **upload**: `read_file` hits the store and succeeds; a script under `exec` cannot see it (it is not
  in the sandbox). One path, two worlds.
- **delete** (**corrected 2026-09-18**): the above used to say "after the store copy is deleted,
  `read_file` falls through to the sandbox" — that is **false in practice**, because it assumed the
  store delete succeeded. **The panel delete is currently a silent no-op**: the file list returns
  **agent-relative paths that already carry the scope prefix** (`sessions/<sid>/f`, `projects/<pid>/x`;
  see the comment on `handleAgentFileList`, which says this is the shape the download endpoint
  expects), the panel DELETEs that path as-is with `?sessionId=<chat>`, and the handler then applies the
  scope arguments **again** (`Delete(id, "", <chat>, "sessions/<sid>/f")`) ⇒ the key grows a second
  prefix (S3: `<agent>/sessions/<chat>/sessions/<chat>/f`) ⇒ nothing is there; and both backends report
  success for "delete a target that does not exist" (S3 is idempotent, LocalFS explicitly swallows
  `os.ErrNotExist`) ⇒ the API answers 200, the UI believes it deleted, and the row reappears on refresh
  (the list reads the store, and that object never moved).
  **Contrast**: the download endpoint passes an empty scope (`Get(agent, "", "", path)`,
  `handlers_agents.go:1413`), which is why downloads have always worked — the two endpoints simply
  disagree about how that path is to be interpreted, and that disagreement is the defect itself.
  So the "falls through to the sandbox" case only arises when the store object **really** was deleted
  (a script/tool removed it, or after Fix 0 lands).

This is the same family as the incident with the direction reversed: the incident was "the sandbox's
old copy overwrote the store's new version"; this is "the store's new file can never reach the
sandbox". It causes no data loss (the store is authoritative), but it leaves the two copies
inconsistent and the agent unable to notice — exactly the state the principle forbids.

> **Scope correction (2026-09-18 re-inventory)**: the two bullet points above apply **only to panel
> upload/delete** (`POST`/`DELETE /api/n`, `handlers_agents.go:1503/1534`). **Attachments are a different
> path**: `WriteSessionAttachments` (`agent/attachments.go:118-130`) writes **all three** — the host
> directory, the workspace store, and the **live sandbox** (`ex.WriteFile("/workspace/"+name)`). So "no
> user file ever reaches the sandbox" is wrong; the accurate statement is **"panel upload/delete is the
> only write path that does not write through"**. Every store writer is listed in 01 §3.1.1.
>
> **Addition (2026-09-18)**: this divergence now has **two** moments of speech, sharing one sentence
> (`sandbox.StoreOnlyLine`): `list_dir` (when the agent looks, G7a) and **the sync after every exec**
> (G4) — the sync side closes the walk's blind spot by listing the store once. Neither claims an origin.

**Landing (2026-09-18; the signal half was chosen)**: G7 had two options — write through on this path
too, or turn the divergence itself into a signal. What is implemented is the latter, because the former
changes the product semantics of "where a user's upload lands" and has to cope with there being no
sandbox at all at upload time; it needs your decision. The signal touches no write path.

Concretely: `list_dir` reads the **store** while `exec` reads the **sandbox**, and where the two
disagree is exactly where the agent gets misled. So the store branch, after producing the listing, asks
the sandbox once (`find /workspace -type f -printf '%P\n'`, only when the executor declares
`RemoteWorkspace`; on docker there is one copy and nothing is asked) and appends the paths the store has
and the sandbox does not:

```
f shared.csv (4 bytes)
f uploaded.csv (4 bytes)

[workspace] 1 path(s) listed above are in the workspace store but NOT in this sandbox, so anything run
with exec will not find them: uploaded.csv. A sandbox that is already running is not updated by uploads —
if a script needs one, read it and write it again (read_file + write_file copies it in).
```

Four properties, one test each: **it names the divergence**, **it does not misreport a shared path**,
**it is silent when the two agree (C3)**, and **it says nothing when the sandbox cannot be asked**
(a σ that cannot be grounded is worse than silence) — `store_only_signal_test.go`. The cost is one
extra exec per `list_dir` (an action the agent takes deliberately, not per exec).
**The other half (making uploads actually reach the sandbox) stays open**, see §4.

### 3.3 Outside writers changing the agent's world (G8 / G9 / G10 fixed)

The turn-level sample (`env_changes.go`'s `envSnapshot`) used to collect **skills, tool names, the
memory hash**. Anything outside those three, caused from the outside, had no σ:

> **Landing (2026-09-18; G8 and G10 fixed, and G9 closed in the next paragraph)**: the sample gained three things — fingerprints
> of the seven **identity files**, the **model + prompt mode**, and the **scheduled-job list**
> (`envSnapshot.cron`: id / name / schedule / type / enabled only — `LastRun` / `NextRun` are the
> scheduler's bookkeeping and change on every tick, so including them would fire the signal every turn,
> the exact C3 violation). G8 and G10 therefore close.
>
> **How G9 was closed (third pass, same day): don't invent a baseline — use the one that
> already exists.**
>
> The duty's shape is what forces something durable here: a config change takes effect **by rebuilding
> the Agent**, so the tracker that should have reported it is destroyed by the very change it should
> report — in the O4 sense of 08 §2.2 the σ is not lost in transit, it **cannot be produced at all**.
> The first fix gave it a dedicated baseline (a `configs_kv` row, kind `cfg_seen`). That works, but it is
> a **second copy of a fact that already has a home**: "what this conversation last ran under" is
> recorded by the turn itself — `session.Session.Append` stamps `provider`/`model` onto every assistant
> message (the existing `session_messages` columns, mapped in both directions).
>
> The second version moved the "before" to a single fingerprint field on that same receipt
> (`run_config`) — and in the same pass it became clear the same slot could carry the **whole world
> snapshot**, while the other five families were stuck on the same in-process baseline (see the G20
> paragraph below). **Final shape (third version, i.e. the code as it stands):**
> `provider.Message.Metadata`'s `run_receipt` (`session.RunReceiptMetadataKey`) carries the whole
> `envSnapshot` as JSON; the agent loop takes the document back from `signalEnvironmentChanges(...)`
> and stamps it with `SetRunReceipt(...)` (riding `Append`, the single entry point), and the next turn
> reads it with `envBaselineFromReceipt(history)` plus the pure `renderEnvDelta(prev, seen, cur)`. So:
>
> | Concern | Result |
> |---|---|
> | new storage | **none** — no new kind, table or column; the receipt was already being written |
> | new write path | none — reuses `Append`, the single entry point for assistant messages |
> | across instances / replicas / restarts | ✅ the receipt lives in `session_messages` / `sessions.messages`, independent of the instance |
> | cleanup | none — delete the conversation and the row is gone; no "`seen:` row left behind" |
> | semantics | more accurate: the "before" is **what this conversation really ran under**, not what some process happened to sample |
> | when it cannot be read | silence: first turn, a history rewritten by compaction, rows written before the stamp — all "not sampled" (no guessing) |
> | coverage | five families plus configuration in one step (G20), instead of a single field moved first |
>
> One real bug fell out of this: `StoreAdapter.GetSession` (the blob path) omitted `Provider`/`Model`
> when mapping, while the archive path (`providerMessageFromStored`) carried them — the same row read two
> ways and disagreeing, which is exactly how a receipt ends up incomplete. Fixed, and pinned by
> `internal/session/run_receipt_test.go` (the stamp must still read back **after a reload**).
>
> **G20 (same pass, generalised): put the whole snapshot on the receipt and delete `envTracker.last`.**
> The configuration case could use "place 2" only because the receipt was already being written; the
> other five (skills / tool names / memory / identity files / cron) kept their baselines in the
> **in-process** `envTracker.last`, so after a **pod restart or hot reload** their first observation was
> still silent — the same O4 shape, triggered by an instance replacement instead of by "the change *is*
> the replacement". With a receipt written every turn there is no reason to move just one field:
>
> | Item | Change |
> |---|---|
> | What travels | `provider.Message.Metadata["run_receipt"]` = the whole `envSnapshot` as of the START of this turn (JSON: skills / tools / memory / identity / cron / config) |
> | Who writes | the agent loop takes the document back from `signalEnvironmentChanges(...)` and stamps it with `SetRunReceipt` onto this turn's assistant message (`Append` is the single entry point) |
> | Who reads | the next turn: `envBaselineFromReceipt(history)` → `renderEnvDelta(prev, seen, cur)`, a **pure function** |
> | Deleted | the `envTracker` type, its `last` map and mutex, the `Agent.envTracker` field, and the config-only `configWas` branch — **one baseline source, five families served** |
> | When it cannot be read | silence throughout (first turn / history rewritten by compaction / pre-stamp rows) — the same rule as "first observation is not a change" |
>
> One **delivery gap** fell out of the same pass: of the three turn entry points only `HandleMessage`
> sampled the environment (`HandleWebChatStream` delegates to it, so web chat was fine), while
> `handlePlanMode` and the API's `HandleMessageStream` **neither sampled nor stamped** — the environment
> signal did not exist on those paths at all, and those turns never advanced the baseline either. All
> three paths now sample and stamp (G20 in §4).

- **identity/system files** (the contents of SOUL / IDENTITY / USER / AGENTS / HEARTBEAT): read fresh
  and assembled into the prompt every turn; if the content changes the agent is not told (G8);
- **agent config**: model, prompt mode, skill switches; `reload_epoch.go` only rebuilds the runtime
  UserSpace (a per-user cross-replica invalidation broadcast) — it does not speak for itself, but **the
  rebuild is exactly the line the durable baseline had to cross**: the baseline no longer belongs to the
  instance, so the rebuild becomes the precondition for that σ rather than its destroyer (G9);
- **a cron job or `HEARTBEAT.md` deleted or edited from outside**: the agent's "I will be woken at X"
  becomes a false fact (G10).

Note the difference between G8/G9 and "the tool set changed": a tool-set change happens to be sampled,
so it is ✅ — not because it was designed better, but because **the sampling surface happened to cover
it**. Every sibling outside that surface is unsignalled. This is the second kind of hole beyond item 4
of the 08 §6 checklist ("when is the signal delivered"): **the sampling surface itself needs a criterion**.

### 3.4 Changes on the MCP server side (G11; the **stdio half was fixed 2026-09-18**)

MCP tools are registered **once, when the agent is constructed** (【code】 `loop.go:472-484`:
`mcp.NewManager` → `ToolDefs()` → register the closures), so a server-side change can only become real
in one of two ways: rebuild the agent, or let the registry add and remove tools at runtime.

**Research first — can a notification even arrive?**

| Transport | Can it arrive? | Evidence |
|-----------|----------------|----------|
| **stdio** | **yes, and it was thrown away** | `stdio.go`'s read loop scans stdout line by line and returns only on `resp.ID == requestID`; everything else is `continue`d. A server's `notifications/tools/list_changed` (a method, no id) **is parsed and then dropped**. Request ids start at 1 (`NewStdioClient` sets `nextID: 1`), so a notification's id=0 can never be mistaken for a request id |
| **HTTP** | **no — there is no wire** | `HTTPClient.sendRequest` is one POST per request and parses the body as a single JSON-RPC response; it neither consumes Streamable HTTP's SSE stream nor sends `Accept: text/event-stream`. A server has nowhere to push on this transport: it is not "we drop it", it is "it cannot arrive" |

**Landing (2026-09-18, the stdio half)**: capture → manager → gate → **the rebuild path `mcp add`
already uses**:

```
the stdio read loop recognises a method message   →  StdioClient.SetNotificationHandler
    →  Manager.wireNotificationSink (HTTP is not wired — no pretending)
        →  notificationGate: at most one handled notification per server per 30s
            →  agent layer: method == notifications/tools/list_changed
                 →  ag.mcpConfigNotify(owner, agent)   ← the same path mcp add/remove uses
                     →  the next turn rebuilds the userspace ⇒ a fresh tools/list ⇒ the tool set changes
                          ⇒ **the per-turn environment signal says `tools now available / no longer available` by itself**
```

Note the last two steps: **the signal needed no new machinery** — 08 §9.1's "one exit per category"
paid off a second time. Tool-set differences were already sampled; what was missing was making the
difference actually happen.

The gate is not optional: behind it sits "rebuild one user's whole userspace". A server that announces
changes in a loop (or has a bug) must not be able to turn that into a loop — throttling logs a warning
rather than staying silent.

**Two boundaries, recorded honestly**:
1. **the HTTP half is not fixed**: receiving there needs an SSE stream (a real feature) or a periodic
   re-list (one network round trip per turn). HTTP behaves exactly as it did before the research —
   changes wait for the next reload;
2. **neither transport sends `notifications/initialized`** (searching the repo for `initialized` finds
   nothing): the MCP spec requires the client to send it after initialize. Today's servers
   (QC / Quandora) do not require it, so it is not fatal — but it may be the precondition for some
   servers to start pushing notifications at all. Recorded as **G15 (open)**: not changed here, because
   it alters the handshake with real servers and needs a live check first.

### 3.5 Automatic turns deferred or dropped (G12; **fixed 2026-09-18**)

When an automatic source (cron / heartbeat / goal / subagent) collides with a running turn it is parked
until the session is idle (【code】 `internal/gateway/deferred_turns.go:14-40`) and **dropped** after
`maxWait`, leaving only a slog line. In the dropped turn the agent never learns "there was a turn I
should have run". Cron and goal have natural compensation (the next cycle comes around); for heartbeat
it is a silent missed beat.

**The gap was more basic than that**: the drop left a single slog line reading `count=2` — **not which
turn, which session, or which source**, so it could not be located afterwards. That is the operational
version of 08 §6's item 4 ("a signal produced with no delivery point").

**Landing (2026-09-18)**: first separate "whose expectation broke" —

| Source | Whose expectation | Disposition |
|--------|-------------------|-------------|
| **cron** | **the user's** ("remind me at 9" was their request) | one slog line per drop carrying the specifics (agent / channel / chat / source / how long / first line of the trigger), **plus a note in that chat**: `[scheduled task did not run] The scheduled task "morning-brief" was due, but this conversation stayed busy for over 5m0s, so its turn was dropped. Set it up again if you still need it.` It goes out through the channel the user is already in (IM stays IM) and **starts no turn** |
| heartbeat / goal continuation | the harness itself (the next tick / the next PostTurn comes around) | slog with specifics only; telling the user would be C3 noise |
| subagent | the parent turn (`spawn_subagent` waits synchronously; dispatch goes through the task queue) | slog with specifics only; the synchronous wait makes "parked until timeout" very rare |

The channel that carries the note is **bounded** (250 ms): when the outbound route is wedged it logs a
warning instead of stalling the drain loop — a remedy for observability must not become the new blocker.

## 4. Gap list (ordered by cost)

> The division of labour among the three formal systems, and the symbol table, are in
> [00-formal-systems.md](./00-formal-systems.md); the "Duty" column maps each gap onto 08 §2.2's O1–O5: **O1** the fact should have been produced and
> was not, or was stated falsely; **O2** it was produced but never placed on D₁ or D₂; **O3** the moment of
> taking is wrong (the consumer is required to subscribe); **O4** it was lost before delivery (in-process
> state); **O5** it speaks without a change, or interrupts reasoning. A "—" means the row is not a delivery
> failure (another family; the reason is noted inline).

| # | Duty (08 §2.2) | Gap | Trigger | Cost | Direction of the fix |
|---|------|------|--------|------|---------|
| ~~G5~~ | O1 | ~~the σ from `write_file` / `apply_patch` is always false (over 2 MiB), and docker emits it too~~ | the agent itself | **P0 fixed (2026-09-18)**: four `CompareResult` states; docker is silent; re-checked with the same probe (§2.1) | — |
| ~~G6~~ | O1 | ~~`apply_patch` / `write_file` can never report "replaced a different version"~~ | the agent itself | **P1 fixed (2026-09-18)**: `previousStoreVersion` + `plannedWrite.previous`; `write_file` reports it now | — |
| ~~**G7a**~~ | O1 | ~~the divergence is invisible~~ | the user | **P1 fixed (2026-09-18)**: `list_dir` now names the "store has it, the sandbox does not" paths (with the reason and a way out); docker is not asked, and an unanswerable probe stays silent | — |
| ~~**G7b**~~ | — (write-path symmetry: a Cordis precondition, not a delivery duty) | uploads/deletes do not reach the live sandbox | the user | **upload half decided (2026-09-18): no write-through = option a.** The product semantics is **"the panel is the file library"**; "the next `exec` immediately lists it" is **not** a requirement, and both the cost and the workaround are already stated by the existing signal (`list_dir` and the post-exec sync name the paths the store has and the sandbox does not, and give the `read_file` + `write_file` recipe). Clarification: on LocalFS + docker it is *naturally* visible immediately (one tree); we will **not** change the architecture to hide it — the accurate statement is "immediate visibility is not promised and not prevented; the backend decides" | the **delete half is still unfixed and must be**: awaiting the choice between d1 (write-through delete) and d5 (deletion tombstone), see [05 §8](./05-remediation-plan.md) |
| ~~**G21**~~ (found 2026-09-18; same family as G7b) | — (the path/scope conventions disagree; F1's "one path, one key") | ~~the panel delete was a silent no-op: the list returns agent-relative paths that already carry the scope prefix, the panel DELETEs that path as-is with `?sessionId=`, and the handler applied the scope **again** ⇒ the key grows a second prefix ⇒ nothing is there; both backends report success for a missing target ⇒ API 200, the UI believes it deleted, and the row reappears on refresh~~ | the user | **fixed (2026-09-18)**: ① **Fix 0** — delete now uses the same convention as download (a path that carries its prefix is read as agent-relative, `Delete(agent,"","",path)`; the older bare-path + query shape keeps working, with `sandbox.StorePathScope` as the single decision point); ② **d1** — the same delete also drops the **live sandbox's** copy (`Gateway.RemoveWorkspaceFile` → `sandbox.LiveWorkspaceFileRemover`, reaching the instance through `LiveExecutorPool` — **live instances only, never creating one**), because otherwise the next sync writes it straight back (both halves pinned on real E2B). A failed sandbox removal answers `sandboxRemoved:false` + a warning instead of pretending the delete was clean | — |
| ~~G8~~ | O1 | ~~identity/system files edited from outside, unsignalled~~ | user / other session / other pod | **P1 fixed (2026-09-18)**: the turn-level sample now carries fingerprints of the seven identity files; the file is named, never quoted | see the landing note in §3.3 (`identitySampleFiles`) |
| ~~**G9**~~ | O4 | ~~agent config edited from outside, unsignalled; the baseline died with the instance, and a config change takes effect precisely by replacing that instance~~ | the user | **fixed (2026-09-18)**: `model` + `prompt_mode` are sampled, and the "before" is no longer invented — it is **read from the conversation's own turn receipt**. Zero new storage, zero new write path, and inherently safe across instances and replicas. A rebuilt agent's **first turn** can now state `my configuration changed: <old> → <new>`; no readable receipt means silence. The receipt later grew into the whole snapshot (see G20) | — (G20 generalised the same mechanism to the other five families) |
| ~~**G20**~~ | O4 (the generalisation of G9) | ~~the five families (skills / tool names / memory / identity files / cron) still kept their baselines in the in-process `envTracker.last`: their first turn after a pod restart or hot reload was silent; and `handlePlanMode` plus the API's `HandleMessageStream` neither sampled nor stamped~~ | the user / harness | **fixed (2026-09-18)**: the receipt now carries the **whole `envSnapshot`** (metadata `run_receipt`); the `envTracker` type, its `last` map, its mutex and the config-only `configWas` branch are deleted, and the judgement is the pure `renderEnvDelta(prev, seen, cur)`. All three turn entry points sample and stamp | see §3.3: this is the first complete landing of the "reuse an existing durable record" place; the only boundary left is "no receipt ⇒ silence" (first turn / after compaction) |
| ~~**G19**~~ (found 2026-09-18) | O1 | the Policy C unhydrated judgement (`workspaceUnhydrated`) lived only in the **creating process**: `adoptFromLease` deliberately does not replay hydration ("the creating pod hydrated the same scope"), so an adopting replica's flag is always false ⇒ **the declaration disappears**, and the agent reads the empty `/workspace` as "my files are gone" | the sandbox-lifecycle family | P1 **fixed (2026-09-18)**: the bit rides the **instance** in `sandbox_leases.unhydrated` (`SetSandboxLeaseUnhydrated`, CAS on owner + sandbox id; cleared by Acquire/Replace so it can never be pinned onto a successor); adoption reads it back with `ex.setWorkspaceUnhydrated(rec.Unhydrated)`, and the create/replace publish points write it via `publishUnhydrated` | live E2E: pod A creates with a broken store (listing fails) → pod B adopts the same instance → still reports unhydrated; falsification (removing the adoption read) turns that E2E red |
| ~~**G10**~~ | O1 | ~~cron jobs / `HEARTBEAT.md` edited or deleted from outside, unsignalled~~ | the user | **P2 fixed (2026-09-18)**: the scheduled-job list joined the turn-level sample (`scheduled jobs added / changed / no longer exist: <name>`); a change to `HEARTBEAT.md`'s content was already covered by G8's identity fingerprints; an unreadable list is stated as unreadable and **never reported as a deletion** | — |
| ~~G14~~ | — (two sources for one thing, not a delivery duty) | ~~`HEARTBEAT.md` has **two sources**: the prompt reads the store, the trigger reads only `<home>/HEARTBEAT.md`~~ (`heartbeat.go`) | the user / an operator | **P2 fixed (2026-09-18)**: `loadHeartbeatTasks` now goes through the **same resolver the prompt uses** (`ctxBuilder.loadFileForUser("HEARTBEAT.md", ownerUserID)` — store row first, disk fallback). The owner is exactly what `chatterUserID` resolves a `SourceHeartbeat` turn to, so "what the agent sees" and "what fires" are the same bytes by construction. Shapes with no context builder (embedded/CLI) keep reading the disk copy | — |
| ~~**G11**~~ | O1 | ~~MCP server-side notifications are dropped~~ | an external server | **the stdio half was fixed (2026-09-18)**: capture → gate (30s per server) → reuse `mcpConfigNotify` to rebuild → the per-turn tool-set signal reports it; **HTTP still has no notification channel** (see §3.4's two boundaries — closing it needs SSE or a periodic re-list) | — |
| **G15** | — (protocol compliance, not a delivery duty) | `notifications/initialized` is never sent (neither transport) | — | P3: the spec requires it after initialize; today's servers do not require it, but it may be the precondition for a server to start pushing notifications at all | send it, but only after verifying the handshake against real servers (QC / Quandora) |
| ~~G16~~ | — (a masked secret written back through the setup API, not a delivery duty) | ~~a skill secret is overwritten by its own mask~~ (found by the 2026-09-18 dead-code scan) | the admin dashboard | **P1 fixed (2026-09-18)**: the rule now has **one home** — `mergeSkillEntry` (per entry) + `mergeSkillEntries` (per patch) — shared by **both** write paths: the global `skills.entries` sweep (merged against the stored value before it is written) and the per-agent override row (loaded with `scope.SettingInto`, then merged). The existing inline guards for providers/channels are **left alone** (different request shapes; revisit once a third variant proves the seam). Wiring it exposed a real trap: Go's JSON decoder **reuses** maps rather than replacing them, so the pre-overlay snapshot must be a **deep copy** (`cloneSkillEntries`) — otherwise the comparison runs against the very map that was mutated in place, which is how the first attempt silently kept the mask | — |
| ~~**G12**~~ | O2 | ~~a deferred/dropped automatic turn leaves only a slog line~~ | the harness | **P2 fixed (2026-09-18)**: each drop carries the specifics (it used to be just `count=N`); a cron drop — the user's own task — also sends a note into that chat (bounded send, no turn started); the harness's own sources are logged without bothering the user | — |
| ~~**G13**~~ | O3 (not O2) | ~~a background shell / sandbox job finishing is not pushed~~ | the agent itself | **reclassified: not a defect (formal re-review, 2026-09-18)**. δ = the process exits; the σ **exists and is recomputed from the world on every read**: `bash_output` answers `[status] exited (code=N)` (`killed` and `lost — the sandbox was replaced` are derived the same way, [sandbox_background.go](../../internal/agent/tools/sandbox_background.go) lines 350–375). By 08 §2.2.2 **D₁ exists only while a call is in flight**, so "nothing is pushed while idle" is not "there is no delivery point" — the delivery point is that read itself (O3). A recomputable criterion is **place 1** of the three placements: no carrier at all | ⛔ **Do NOT build "attach 'background job X exited' to the next tool result"**: it would introduce an in-process "exited but not yet reported" set — exactly the O4 shape (lost when the instance changes) — for a fact that can be recomputed. Trading new in-process state for an already-reachable σ is a net loss |
| ~~**G18** (= 01 §8)~~ | — (entity invariant: one path, one key — not a delivery obligation) | ~~`apply_patch` wrote the store with `r.sessionID` + the raw path while the mirror and the other two tools used `scopeSessionID()` + `wsPath()` ⇒ a single write contradicted itself: the store landed on key A, the mirror on path B, one file with two keys~~ | the agent itself | **P1 fixed (2026-09-18)**: all 6 touchpoints (3 host + 3 sandbox) unified on one resolution, and the sandboxed `apply_patch` gained the per-file write-through it never had. 3 + 1 unit tests and 1 live E2E, each falsified (reverting the fix turns them red) — 01 §8.1 | — |
| ~~**G17**~~ | — (scope invariant: same family as G18, one layer down) | visibility in a project's "one file tree, many containers": a project session's containers are **per chat** (deliberate — concurrent chats must not share shell state) and the preview's dev server runs in only one of them ⇒ ① a console-started preview uses the container `agent:p:<pid>`, which **no agent turn ever uses**, so agent writes can never reach it; ② a sibling chat's edit never reaches the dev server's container (docker gets this for free from the bind mount) | the agent / the user | **decided + fixed (2026-09-18, options G+H)**: **G** = the preview container is addressed by PROJECT (`previewSandboxSession`: any entry point in a project uses `session=""`, so one project = one preview container); **H** = writes and deletes are **broadcast to every live container of the project** (`LiveProjectExecutors` + `mirrorToProjectPeers` and the delete fan-out) — docker's mount semantics made explicit on a backend without one, while **keeping per-chat shells**. A partial mirror gets its own σ (§2.1). Live: two containers in one project — A writes, B reads it; A deletes, B loses it and B's sync does not resurrect it | **A was fixed too (decided the same day)**: `syncStoreScope` collapses the write-back to the project root in a project session (the same key hydrate and the file tools use) ⇒ no more `<project>/<chat>/…` duplicates, and a file created by `exec` is immediately visible to `read_file` / `list_dir`. **No migration**: copies produced before the change stay in the store (no longer refreshed, and nobody cleans them) — for the one-off cleanup see `scripts/workspace_project_chat_duplicate_cleanup.py` in [05 §6](./05-remediation-plan.md) (it only lists a copy for deletion when the same bytes survive at the project root). — no duplicates, the exec artefact lands at the project root and is visible to the tools, and a sandbox edit of an existing path is still refused (against the key the tools read) |
| ~~**G22**~~ (found while implementing, 2026-09-18) | — (scope invariant, the **third** instance of the same family) | the write-through's **mtime stamp** reads the store with the **sandbox scope** (`Stat(sc.agentID, sc.projectID, sc.sessionID, storeKey)`), while in a coding-root project session the tools write the **project root** (`session=""`) ⇒ that Stat always misses in project sessions ⇒ **no stamp lands**. Cost: the reconcile loses the cheap "same size + same mtime" test for those paths and falls back to a byte comparison (`equalToStore`) — **no functional loss** | the agent / sandbox | **fixed (2026-09-18, same round)**: the writer hands the store scope down — `WriteThroughScope(storeScope, storeKey, sandboxPath, content, previous)` (`sandbox.StoreScope`), and both the stamp and H's broadcast copies use **the scope the caller stated** instead of inferring it from the container. **Measured** (real E2B, `TestE2BLiveSyncReadsNoBodiesForStampablePaths`): whole-object reads for that path in one sync went **1 → 0** (stats still 2). `TestWriteThroughStampsWithTheStoreScopeItWasGiven` pins that the stamp uses the stated scope; **falsification**: point the pool back at the sandbox scope and it goes red at once. **Why do it when the impact is only performance**: this seam (the store scope crossing into the sandbox layer) produced three defects in one round (G21 delete, G17/A sync, this one), which meets the "invest in a boundary after 3+ changes at the same seam" test — and the fix replaces guessing with passing a fact (a port correction), adding no machinery |
| ~~**G23**~~ (found in review 2026-09-18; **fixed the same day**) | — (scope invariant, the **fourth/fifth** instance of the same family: one rule written as several expressions) | ~~"a project session ⇒ keys land at the project root" is written in **three places with two different predicates**: the agent side's `Registry.scopeSessionID()` uses `codingRootScope` (= `a.projectRuntime != nil && projectID != ""`), the sandbox side's `syncStoreScope()` uses `projectID != ""`, and the panel side's `StorePathScope()` uses "does the path carry a scope prefix"~~ following that thread turned up a **fifth**: the **layout table** (`pid/sid` → directory) was written out twice, in `LocalFS.scopeDir` and again in `S3.key` / `S3.scopePrefix` | agent / panel / sandbox / both store backends | ~~unreachable today, so not a defect; but the equality holds by wiring accident~~ **P3 fixed (2026-09-18)**: both rules moved into [`internal/workspace/scope.go`](../../internal/workspace/scope.go) as two pure functions — `ScopeSegments` (the layout table, shared by LocalFS and S3) and `WriteScope` (the writers' collapse, shared by the file tools and the sandbox's write-back); `Registry.codingRootScope` / `SetCodingRootScope` are deleted and `sandbox.StoreScope` is now a **type alias** of `workspace.Scope` (the port no longer carries a second copy of the fact). **One behavioural delta**: the collapse is keyed on "is there a project" instead of "is a runtime wired and are we in a project"; the two differ only in a deployment that has projects but no runtime manager — and `cmd/fastclaw/main.go` builds and wires the runtime manager unconditionally, so in such a deployment projects cannot be created either | Unit tests: `go test ./internal/workspace/ -run 'TestScopeSegments\|TestWriteScope\|TestAWriterScope'` (the layout table, the writers' rule, and that the two describe one filesystem), `go test ./internal/sandbox/ -run 'TestLayoutWriteScopeAndParserAgree\|TestProjectWritersAndTheSyncShareOneScope\|TestAProjectChatSubdirKeyIsItsOwnPath'` (layout / writers / panel parser agree; and "a project-chat-subdir key is its own object" is pinned), `go test ./internal/agent/tools/ -run TestScopeSessionIDCollapsesInsideAProject`. **Falsifications**: revert `syncStoreScope` to "no collapse" ⇒ `TestProjectWritersAndTheSyncShareOneScope`, `TestSyncWritesBackToTheProjectRootNotTheChatSubdir` and `TestSyncScopeEqualsHydrateScopeForProjects` go red; revert `scopeSessionID()` to `r.sessionID` ⇒ `TestScopeSessionIDCollapsesInsideAProject` and `TestApplyPatchUsesTheSameStoreKeyAsWriteFile` go red (both run for real) |
| G1–G4 | G1/G2 = **O2** (no delivery point; fixed); G3 = **O4** (in-process queue; fixed); **G4 = O1, fixed on 2026-09-18 as far as "the fact is stated"**: one store `List` per sync reports the paths the store has and the sandbox does not (the same sentence as G7a, `sandbox.StoreOnlyLine`); **attribution ("the sandbox deleted it" vs "it was uploaded later") = decided against** (2026-09-18, option a): the consequence is already delivered, a manifest would only buy the cause, and a "delivery manifest" would write thousands of rows per hydrate while a "writer manifest" would touch all six write paths — see the decision log in [05 §8](./05-remediation-plan.md) | the sandbox-lifecycle family (rebuild, deletion) | provider / harness | see 09 | see 09 §6 |

> **G22's boundary (measured 2026-09-18)**: with the write-through stamp **removed entirely**,
> `TestE2BLiveSyncReadsNoBodiesForStampablePaths` still read **0** whole objects — the criterion's ±1s tolerance
> (`sameVersion`) absorbs "the store write and the mirror write landed in the same second", so that count is a
> **measurement**, not a falsification. What does pin the stamp: the unit test
> `TestWriteThroughStampsTheSandboxCopyWithTheStoreTime` (it pins the instant in the command) and the live
> `TestE2BLiveHydrateKeepsStoreStamp` ([11 §10](./11-change-register.md), rows 10-4 / 10-5 / 10-8).

## 5. Conclusions

1. **Class A's problem is not "a missing mechanism" but "a σ that lies".** G5/G6 both live inside an
   existing exit; no new channel is needed — the exit's two sentences just have to each be true. A
   single `Compared` field carrying two meanings is the most concrete defect this audit found.
2. **Every class-B gap points at the same exit, and widening the sample did suffice — twice.** G8 and G10 are
   now closed exactly that way: identity-file fingerprints joined the turn-level sample, and the number
   of exits is still three (08 §9.1). G9 is only half done, and **not** because two lines were missing:
   a config change rebuilds the Agent, so the in-process tracker dies with the instance, and a
   cross-rebuild change needs a persisted baseline or an explicit statement from the reload path. That
   belongs in the open list, not in the "sampling surface" bucket.
3. **G7 was split in two, and that split is itself the conclusion.** The observability half (G7a, saying
   the divergence out loud) has landed and touched no write path; the remaining G7b (making an upload
   actually reach the live sandbox) is a **write-path symmetry** question that this principle cannot
   decide for the product. Separating "must fix" into "can fix now" and "needs a decision" is more useful
   than leaving both in one row.
4. **The principle reaches far beyond "filesystem sync"**: half of the rows that violate it here have
   nothing to do with files (config, cron, MCP notifications, turn scheduling). That confirms 08's
   opening line: filesystem sync is only the first application of this principle.

## 6. How to re-verify

| Conclusion | How to check it independently |
|------------|------------------------------|
| G5 (fixed) | re-run the §7 probe: neither the e2b nor the docker shape produces a `[workspace]` line any more; `go test ./internal/agent/tools/ -run TestWriteFile` |
| G6 (fixed) | the third part of the same probe: when a script edited the sandbox copy first, the `write_file` result contains "replaced a different version (12 bytes)" |
| G7a (fixed) | the four `go test ./internal/agent/tools/ -run TestListDir` cases; or upload in a real environment and look for the `[workspace]` line after the listing |
| G7b | — (write-path symmetry: a Cordis precondition, not a delivery duty) | upload a file into a live sandbox via `POST /api/n`, then compare `exec ls /workspace` with `read_file` — the divergence is still there (it is now merely stated) |
| G8 (fixed) | `go test ./internal/agent/ -run TestEnvSignalCarriesIdentityFileChanges` (naming, no content leak, silence when unchanged, deletion counts) |
| G9 + G20 (fixed) | write end (the receipt): `go test ./internal/session/ -run TestRunReceipt` — against a **real sqlite**, "instance 1 writes → another manager reloads → it is still readable", and a session with nothing bound stamps nothing. Read end: `go test ./internal/agent/ -run 'TestEnvBaselineComesFromTheTurnReceipt|TestRebuiltAgentStatesTheChangeFromTheReceipt|TestReceiptCarriesEverySampledFamily|TestEnvSignal'` — last receipt wins, a model-only row is not guessed from, a missing or undecodable receipt means silence, and **all five families cross the receipt** (one assertion each for skills/tools/memory/identity/cron/config). **Falsification**: ① comment out the stamp in `Append` → `TestRunReceiptStampSurvivesAReload` goes red; ② make `envBaselineFromReceipt` always report "not sampled" (equivalent to deleting the receipt baseline) → all three read-end tests go red |
| G19 (fixed) | `go test ./internal/store/ -run TestSandboxLeaseUnhydrated` (real sqlite: the flag round-trips, a foreign or wrong-instance write does nothing, Replace clears it, and a late write about the old instance cannot pin the successor) + `go test ./internal/sandbox/ -run 'TestE2BPoolAdoptionCarriesTheUnhydratedFact|TestE2BPoolPublishesTheUnhydratedFactOnCreate'`; live: `FASTAGENT_E2B_LIVE=1 E2B_API_KEY=… go test ./internal/sandbox/ -run TestE2BLiveUnhydratedFactSurvivesPodHandoff -v` (two pools = two replicas over one sqlite lease registry; pod B adopts pod A's instance and still reports unhydrated). **Falsification**: remove the adoption read → the live test goes red |
| G13 (reclassified as "not a defect") | `go test ./internal/agent/tools/ -run 'TestBashOutputTool|TestSandboxJobOutput'` (rendering of exited / killed / lost) plus `TestBashOutputSchemaCarriesTheWholeContract` and `TestExecDescriptionsStayInSync` (the tool contract states "still callable after exit; trust the exited row"); live: `go test ./internal/agent/tools/ -run TestSandboxBackgroundE2BLive -v` (a background job outlives the call that started it and can be read on LATER calls). **The criterion is in the code**: the status line is recomputed from the world on every read, never from a "what I last saw" record |
| G21 + the G7b delete half (fixed) | `go test ./internal/setup/ -run TestHandleAgentFileDelete` (the panel's path shape: `sessions/<sid>/f` + `?sessionId=` really removes the store object; a project-root path takes its chat from the request; the older unprefixed shape still works; a failed sandbox removal reports `sandboxRemoved:false`) + `go test ./internal/sandbox/ -run 'TestSandboxPathForStorePath|TestStorePathScope|TestE2BPoolLiveExecutorDoesNotCreate|TestDockerPoolLiveExecutorDoesNotCreate'` (the mapping, and "live instances only") + `go test ./internal/gateway/ -run TestRemoveWorkspaceFile` (no pool / single-copy backend is a no-op; otherwise the executor is asked). Live: `FASTAGENT_E2B_LIVE=1 E2B_API_KEY=… go test ./internal/sandbox/ -run TestE2BLivePanelDeleteSticks -v` — **both halves**: without d1 the delete is resurrected by the next sync (reproducible), with d1 it sticks. **Falsification**: revert the handler's path convention and `TestHandleAgentFileDelete_UsesThePathThePanelClicked` goes red immediately ("the panel's delete left the file in the store") |
| G10 (fixed) | `go test ./internal/agent/ -run 'TestEnvSignalCarriesScheduledJobChanges|TestCronFingerprintIgnoresRunBookkeeping|TestEnvSignalStatesUnreadableJobList'` (deletion/reschedule/addition are named; bookkeeping does not fire it; an unreadable list says so) |
| G14 (fixed) | `go test ./internal/agent/ -run 'TestHeartbeatReadsWhatThePromptShows|TestHeartbeatFallsBackToTheDiskCopy|TestHeartbeatWithNoFileSendsNothing'` (store and disk deliberately differ → tick and prompt must agree; no store → the disk copy; neither → no turn fired) |
| G11 (stdio fixed) | `go test ./internal/mcp/ -run 'TestStdioClientHandsNotificationsToTheHandler|TestManagerWiresNotificationsThroughTheGate|TestManagerDoesNotWireATransportWithoutNotifications'`; for the HTTP half, read `internal/mcp/http.go` and confirm there is no SSE stream |
| G15 | — (protocol compliance, not a delivery duty) | `rg 'initialized' internal/mcp/` finds nothing (neither transport sends it) |
| G12 (fixed) | `go test ./internal/gateway/ -run 'TestDeferredTurnsAnnouncesADroppedScheduledTask|TestDeferredTurnsDropsMessagesPastBudget|TestDroppedCronNoteWithoutAJobName'` (only cron speaks; the job is named; a trigger without a name never renders a dangling quote) |
| G18 (fixed) | `go test ./internal/agent/tools/ -run 'TestApplyPatchUsesTheSameStoreKeyAsWriteFile|TestApplyPatchDeleteUsesTheSameStoreKey|TestApplyPatchKeyInANonCodingSession|TestWriteThroughMirrorsOneKeyAndOnePath'`; live: `FASTAGENT_E2B_LIVE=1 E2B_API_KEY=… go test ./internal/agent/tools/ -run TestE2BLiveOnePathIsOneKey -v` (command in the file header). **Falsification**: revert the key in `writeForPatch` / `writeForPatchSandbox` to `r.sessionID, path` and the first three go red immediately (`keys = [app/notes.md sessions/<sid>/notes.md]`) |
| ~~G17~~ (decided G+H+A, all fixed) | unit: `go test ./internal/sandbox/ -run 'TestWriteThroughReachesEveryContainer|TestWriteThroughCountsAContainer|TestRemoveLiveWorkspaceFileReaches|TestSyncWritesBackToTheProjectRoot|TestSyncScopeEqualsHydrateScope'` (the broadcast reaches every live container of the project, never another project, and counts failures; the write-back lands at the project root, not in the chat subdir; and the collapse rule itself) + `go test ./internal/runtime/ -run TestPreviewSandboxSession` (the preview container is project-addressed) + `go test ./internal/agent/tools/ -run TestWriteFileStatesAPartialProjectMirror` (a partial mirror has its own σ). Live: `go test ./internal/sandbox/ -run TestE2BLiveProjectWriteReachesSiblingContainer -v` (two containers in one project: A writes, B reads it; A deletes, B loses it and B's sync does not resurrect it) and `go test ./internal/agent/tools/ -run TestE2BLiveProjectSessionKeepsOneTree -v` (no duplicates; the exec artefact lands at the project root and is visible to the tools; a sandbox edit of an existing path is still refused). **Falsification**: drop the broadcast → the first live test goes red at "A writes, B reads it"; change `ws := syncStoreScope(sc)` back to `sc` → `TestSyncWritesBackToTheProjectRoot` goes red (the key becomes `chat-1/artifact.txt`) |
| G22 (fixed) | `go test ./internal/sandbox/ -run TestWriteThroughStampsWithTheStoreScopeItWasGiven` (the stamp uses the **stated** store scope, and an object-less scope stamps nothing). Live: `FASTAGENT_E2B_LIVE=1 E2B_API_KEY=… go test ./internal/sandbox/ -run TestE2BLiveSyncReadsNoBodiesForStampablePaths -v` — asserts **zero whole-object reads** in one sync (measured 1 before the fix). **Falsification**: point the stamp back at `Stat(sc.agentID, sc.projectID, sc.sessionID, …)` → the unit test goes red instantly (`primary container was not stamped`) |

## 7. Appendix: the local probe for G5/G6 (re-runnable)

The point of the probe is to use a **real** `LifecyclePool` rather than a test double: G5 is exactly
the composition of two behaviours that are each pinned by a test, so a double at either end hides it.

```go
// Drop it into internal/agent/tools/ temporarily, then: go test ./internal/agent/tools/ -run TestProbe -v
type probeExec struct{ files map[string]string }
func (p *probeExec) Exec(context.Context, string, time.Duration) (string, error) { return "", nil }
func (p *probeExec) ReadFile(_ context.Context, path string) (string, error)     { return p.files[path], nil }
func (p *probeExec) WriteFile(_ context.Context, path, content string) (string, error) {
	p.files[path] = content
	return "", nil
}
func (p *probeExec) ListDir(context.Context, string) (string, error)           { return "", nil }
func (p *probeExec) Backend() string                                           { return "e2b" }
func (p *probeExec) Close() error                                              { return nil }
func (*probeExec) IsRemoteWorkspace()                                          {}
func (p *probeExec) SnapshotWorkspace(context.Context) (map[string][]byte, error) { return map[string][]byte{}, nil }

// the pool must implement sandbox.ExecutorPool (Get/Release/CloseAll/Backend).
lp := sandbox.NewLifecyclePool(probePool, time.Minute, time.Minute)
lp.SetWorkspace(workspace.NewLocalFS(t.TempDir()))
r  := NewRegistry(t.TempDir(), t.TempDir()); r.SetWorkspaceStore(workspace.NewLocalFS(t.TempDir()), "a")
r.SetSessionID("s1")
ex, _ := lp.Get(ctx, "a", "", ""); r.SetExecutor(ex)
out, _ := r.Execute(ctx, "write_file", `{"path":"notes.md","content":"hello"}`)
// measured 2026-09-18: "Written 5 bytes to notes.md" + "[workspace] notes.md is too large to compare
// automatically … (over 2 MiB)"  ← a 5-byte file claiming to be over 2 MiB
```

A second probe drops `IsRemoteWorkspace()` (the docker shape: a single copy) and the output is
**byte-for-byte identical** — which is precisely G5's verdict: "the exit does not know whether a second
copy exists, only that it did not compare this time".

## 8. Not verified (honestly recorded)

- neither direction of G10 (a cron job deleted from the UI, `HEARTBEAT.md` rewritten) has a test; the
  code path is an inference (the `cron` table → scheduler; `heartbeat.go:94` re-reads the file each
  tick) and no counter-example was found;
- ~~G12 only checked the drop branch in `deferred_turns.go`~~ (**now checked**: the cron scheduler calls `UpdateCronJobRun` in the same block where it fires, so it does **not** re-deliver a missed tick — which makes that note the only trace of the missed run; the harness's own sources are covered by the next tick / the next PostTurn);
- whether provider-account fallback should produce an agent-facing signal depends on whether the
  product counts "which account my requests go out on" as part of the agent's world — recorded here,
  not judged (it is already pinned by a slog line, i.e. operator visibility).

## 9. Dead code and legacy plumbing removed along the way (2026-09-18)

Auditing "what is left after this feature was retired" (five scans: identifiers, struct fields,
package-level constants/vars, config, storage) turned up **zero leftovers** of the baseline family —
no `baselineDigestMax`/`staleWrites`/`decideReconcile` identifiers, no unread fields, no unreferenced
constants, no `ws_baseline` key/column or env var. What was removed:

| Item | Location | Why it had to go | Compatibility |
|------|----------|------------------|---------------|
| `makeExecTool` | `internal/agent/tools/exec.go` | a dead function nothing referenced, and it called `makeExecToolFull(nil, …)` — had anything called it, it would have built an exec tool with no Registry | none (unexported, unreferenced) |
| the whole `BoxliteClientID` chain | the config field / the `FASTAGENT_SANDBOX_BOXLITE_CLIENT_ID` env var / the admin request field / the web TS type / the pool and executor constructor parameters / `defaultBoxliteClientID` | three reasons: ① it was a **knob with no effect** — the config surface told operators "put your OAuth client_id here" while nothing read it and nothing complained (a silent no-op); ② it was passed along the whole chain (config → pool → executor), so `grep` made it look alive and the next reader of the constructor would assume it mattered; ③ the OAuth exchange it belonged to was removed upstream, so keeping it meant maintaining a config field + env var + admin field + frontend type forever. After the removal, "the client_id no longer means anything" is a compile-time fact instead of a comment | config rows or clients that still send `boxliteClientId` are ignored (`json.Decode` does not reject unknown fields by default); the env var is simply no longer read; the admin response no longer carries the key (the web client had no reference to it) |
| ~~`WorkspaceSync`~~ (`internal/sandbox/workspace_sync.go`, 121 lines, **deleted**) | found by the 2026-09-18 re-inventory | **dead code**: `NewWorkspaceSync` / `WorkspaceSync` have zero references repo-wide — it was born in `87c50ee` (the 2026-04-12 multi-user refactor), never modified since, and no commit ever referenced the constructor; its own `WorkspaceStore` interface (userID-only keys) is signature-incompatible with today's `workspace.Store` (agent/project/session scoped), which `LocalFS`/S3 implement instead. The job it implemented — hydrate everything, flush on demand — was taken over by `LifecyclePool` + `hydrateWorkspace` + `syncSnapshot` + `WriteThrough` | none (package-private type, zero references) |
| three zero-reference functions: `generateRandomToken` (`handlers.go`, superseded by `newRandID` in the same file), `filterAccounts` (`handlers_agent_channels.go:169`; every caller of `flattenChannelRows` passes no filter), `defaultIfEmpty` (`handlers_agents.go`) | the 2026-09-18 whole-repo reference count (593 files / 2703 function definitions) | each was reviewed by hand: no interface role, no test reference, never passed as a function value | none |
| `Registry.sandboxSessionID` (the field + its assignment in `SetSessionID` + the comment above it) | `internal/agent/tools/registry.go` | introduced but never wired during this round's G18 fix: zero **reads** repo-wide (only the assignment), and the comment aimed the reader at a symbol that does not exist, `sandboxScopeSession` — worse than no comment, because it asserts a design nobody enforces. **The fact it described is true** (the pool keys instances per chat while the store collapses to the project root), so the fact moved into 01 §8.2 / G17 above and the field went | none (unexported field, zero reads) |

> **The fourth candidate from that scan was NOT deleted at the time**: `mergeSkillEntry` (`handlers.go`) is equally unreferenced, but following it down showed it is an **unwired guard** — deleting it would remove the only implementation of a missing behaviour. It stayed, with a comment explaining why, and the gap was recorded as **G16** above; **it has since been wired up** (both write paths share it, see the G16 row). That is this scan's most valuable output: **"zero references" is a lead, not a verdict** — the same signal can mean dead code or "written but never called", and the two call for opposite actions.
| three comment fossils | `internal/sandbox/lifecycle.go` (the half-sentence "…record it here, once per hydrate. Skipped for a scope the sandbox", and `(over the digest cap)`) plus one unread field in a test | they described mechanisms that no longer exist, so a reader would believe the baseline was still there | comments only, zero behaviour change |

**Deliberately kept**: the "retired" history in 05/06/07 (each spot is explicitly marked — it is the
evolution record), and one **negative assertion** (a test asserting the tool result never contains
`resolve_workspace_conflict`) — that is a regression guard against re-introducing the "let the agent pick
a side" design, not a leftover.
