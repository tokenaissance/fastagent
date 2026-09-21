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
>
> **Correction (measured 2026-09-19)**: T1–T6 **are committed** (branch `fastagent`, HEAD `8984c99`;
> `a0080a5` is an ancestor of HEAD; the **private repository `tokenaissance/fastagent` already carries that
> commit**); **dev runs** `8984c99`, and **production is 34 commits behind**
> (`a24c0a8`, image `…:20260917035926-fastagent-a24c0a8`). This repo has two remotes: `fastagent`
> (private, the current one) and `origin` = `tokenaissance/fastclaw` (the **public mirror, 34 commits
> behind, deliberately not pushed for now**). So "uncommitted" below is stale;
> "**not in production**" still holds. Where the tables below say `HEAD`, read it as `16a7532` of
> 2026-09-17 — this line supersedes it.
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
| **HTTP** | **no standing wire** (a *reply* channel since 2026-09-22) | `HTTPClient.sendRequest` is one POST per request. It used to parse the body as a single JSON-RPC object and nothing else; since 2026-09-22 it advertises both content types and also reads a reply that arrives as an SSE stream (the transport requires the client to support both shapes). That is a reply channel only: a server can put a message there while answering *our* request, and nowhere else. Nothing opens the spec’s GET stream, so an unprompted change still has nowhere to arrive — it is not "we drop it", it is "it cannot arrive" |

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
1. **the HTTP half is not fixed**: what is missing is a *standing* channel. Two MUSTs of the transport
   landed 2026-09-22 (row 43) — `Accept: application/json, text/event-stream`, and reading a reply that
   arrives as an SSE stream — but both live inside one request’s lifetime. An unprompted change still
   needs the spec’s SSE stream (a real feature) or a periodic re-list (one network round trip per turn),
   so for a server that announces a change by itself, HTTP behaves exactly as it did before the research:
   the change waits for the next reload;
2. **neither transport sends `notifications/initialized`** (searching the repo for `initialized` finds
   nothing): the MCP spec requires the client to send it after initialize. Today's servers
   (QC / Quandora) do not require it, so it is not fatal — but it may be the precondition for some
   servers to start pushing notifications at all. Recorded as **G15 (open)**: not changed here, because
   it alters the handshake with real servers and needs a live check first.

**Noticed while landing row 43 — recorded as facts, not decided** (no new gap numbers):

1. **the negotiated protocol version is never read.** `Connect` asks for `2024-11-05` and the client
   ignores the version the server answers with, so it cannot tell which revision it is really speaking
   to. The MUSTs this pass implemented come from the Streamable-HTTP revision (`Accept` on every request,
   both reply shapes, `MCP-Protocol-Version` on later requests, `notifications/initialized`). Two of
   those are still unmet — the version header, and G15 — and the version header follows from reading
   the handshake, so **the declaration cannot be corrected before G15 lands**: that is an order, not a
   preference.
2. **nothing owns a long-lived resource** — *fixed 2026-09-22, row 44*. It used to be: `mcp.Manager.Close`
   closed every connected client, but no production caller reached it, while the paths that drop an agent
   — `userSpaceRegistry.invalidate`, `evictIdle`, `setSystemSandboxPool` — deleted the map entry only.
   So a drop released nothing: a stdio child was left to the `os.File` finaliser on the stdin pipe
   (`os/file_unix.go` sets it) and exited whenever the GC got to it, and a standing HTTP stream would have
   been *permanently* unreleasable, because the goroutine reading it holds the body. Now a drop **retires**
   the space and a sweep takes its clients back once it has been retired for `releaseGrace` (5m) *and* no
   turn of it is running or waiting — a turn is the only thing that ever calls an MCP tool, so that is the
   tightest cheap proof that nothing is using the clients, and it is what keeps a long turn from being cut
   off mid-call. Two limits, recorded rather than hidden: the release runs on the evictor ticker, so it can
   be up to a tick late; and a request that obtained the space *before* the drop can still start a turn
   between the sweep’s check and its own `AcquireTurn`. Either remedy for boundary 1 now lands in a hole
   that has an owner.
3. **a server message that rides along on a request’s stream is traced, not acted on**
   (`parseResponseBody` logs it at debug and skips it). That is deliberate — acting on one is a standing
   channel’s job — but it means a server that uses the request stream as its only channel reaches nobody,
   and only the debug level says so.

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
>
> **The "shape of the control action" column (added 2026-09-20; values are STPA's four guide words)**: it records
> **the shape in which the control action went wrong on this row** — **not provided**, **provided** (it was
> said, but saying it is itself what causes the harm: a falsehood, an empty action, an overwrite),
> **wrong timing / order**, or **duration** (kept running after it should have stopped).
> A **"—" means the row is not a shape problem at all** — it belongs to another family (single source,
> scope invariant, resource bound, dead code, protocol compliance).
> **Why it was added**: the table used to have only the "Duty" column (O1–O5), which can judge a **violation**
> but not an **omission** — "under this condition we provide nothing" was invisible. With this column,
> **the omission itself becomes a visible row.**
> **Next step (not landed yet, a candidate)**: give every class of δ an **allowed set of control-action
> shapes** (provide / not provide / order / duration) and require a **named refusal** (which class of δ,
> on which criterion) — that is what turns this "ledger with a duty register" into a "ledger with a
> control-action list". Today we have **visibility** only, not **control**; and this layer does not reach
> "the agent received it and still acts wrongly" (that half is row ③ of 19.5.1 in *The Mathematical
> Principles of Cognitive Philosophy*). The full statement is in 19.4.6.2, "From ledger to control".
> **A checkable reading**: 23 rows = not provided **13** · provided **2** · wrong timing/order **1** · duration **0** · non-shape **7**.
> An empty "duration" is not an omission but a **reading that can be refuted** — find one "kept running too long" row.
> **Half of it has already been found**: one entry outside the table is a "duration" (**G27**, retired resources retained forever).
> 0 rows in the table, 1 in the register — both numbers have to be written down before it counts as a reading.
>
> **Why this table can decide an "omission"** (added 2026-09-20): because the unit of analysis in this
> system is **stipulated**, not observed — three roles (I1), two delivery points (`D₁` / `D₂`) and one
> duty of the channel (∀δ ⇒ ∃σ) are written down and can simply be read out. So "the thing that should
> have happened did not" can be judged **against the specification**, without waiting for an incident:
> **G3 and G14 are the kind of row you can read out of the code** (a carrier living in process memory;
> two sources for one fact) — they are **structural properties, not incident properties**.
> **The price**: the verdict holds only inside
> **this system's own** scope — where the specification is silent, this method is blind as well.
> The full form is in 07 §3.11.1's methodological note and in *The Mathematical Principles of
> Cognitive Philosophy* 19.4.9.

| # | Duty (08 §2.2) | Gap | Trigger | Cost | Direction of the fix | **Shape of the control action** |
|---|------|------|--------|------|---------|------|
| ~~G5~~ | O1 | ~~the σ from `write_file` / `apply_patch` is always false (over 2 MiB), and docker emits it too~~ | the agent itself | **P0 fixed (2026-09-18)**: four `CompareResult` states; docker is silent; re-checked with the same probe (§2.1) | — | provided |
| ~~G6~~ | O1 | ~~`apply_patch` / `write_file` can never report "replaced a different version"~~ | the agent itself | **P1 fixed (2026-09-18)**: `previousStoreVersion` + `plannedWrite.previous`; `write_file` reports it now | — | not provided |
| ~~**G7a**~~ | O1 | ~~the divergence is invisible~~ | the user | **P1 fixed (2026-09-18)**: `list_dir` now names the "store has it, the sandbox does not" paths (with the reason and a way out); docker is not asked, and an unanswerable probe stays silent | — | not provided |
| ~~**G7b**~~ | — (write-path symmetry: a Cordis precondition, not a delivery duty) | uploads/deletes do not reach the live sandbox | the user | **upload half decided (2026-09-18): no write-through = option a.** The product semantics is **"the panel is the file library"**; "the next `exec` immediately lists it" is **not** a requirement, and both the cost and the workaround are already stated by the existing signal (`list_dir` and the post-exec sync name the paths the store has and the sandbox does not, and give the `read_file` + `write_file` recipe). Clarification: on LocalFS + docker it is *naturally* visible immediately (one tree); we will **not** change the architecture to hide it — the accurate statement is "immediate visibility is not promised and not prevented; the backend decides" | **delete half: fixed twice.** The **panel** on 2026-09-18 (G21's d1) and the **tool path** on 2026-09-20: `apply_patch`'s Delete stopped at the store, so the sandbox copy survived and the next sync pushed the **old version** back — silently, with the caller told it worked. It now calls the same `sandbox.LiveWorkspaceFileRemover` (live instances only, never creating one); a failed mirror attaches a signal instead of claiming a clean delete, the same posture as the panel's `sandboxRemoved:false`. Chose **d1 over d5** for the reason in [05 §8](./05-remediation-plan.md): fix at the source, so no sync-side scope inference is involved. UT: `TestApplyPatchDeleteReachesTheLiveSandboxCopy`, `TestApplyPatchDeleteSurvivesAFailedMirrorWithoutLying`, `TestApplyPatchDeleteIsANoOpWhenTheBackendHasNoSecondCopy`; falsification run for real (disable the mirror call ⇒ the first two go red). **Live proof: done (2026-09-20, real E2B).** `TestE2BLiveApplyPatchDeleteSticks` runs the whole chain on a real sandbox — hydrate → tool delete → the sandbox copy is gone → **one exec later the sync runs and the store still has no key** (no resurrection). **Falsification run on the real machine too**: disable the mirror call and it fails at *"the sandbox copy survived the tool delete: yes"* in 29 s. Command: `FASTAGENT_E2B_LIVE=1 E2B_API_KEY=… go test ./internal/agent/tools/ -run TestE2BLiveApplyPatchDeleteSticks -v`. [This row used to say "the delete half is still unfixed", contradicting G21 above and §6 below: it was the *tool* half that was open, not the panel's.] | not provided (decided) |
| ~~**G21**~~ (found 2026-09-18; same family as G7b) | — (the path/scope conventions disagree; F1's "one path, one key") | ~~the panel delete was a silent no-op: the list returns agent-relative paths that already carry the scope prefix, the panel DELETEs that path as-is with `?sessionId=`, and the handler applied the scope **again** ⇒ the key grows a second prefix ⇒ nothing is there; both backends report success for a missing target ⇒ API 200, the UI believes it deleted, and the row reappears on refresh~~ | the user | **fixed (2026-09-18)**: ① **Fix 0** — delete now uses the same convention as download (a path that carries its prefix is read as agent-relative, `Delete(agent,"","",path)`; the older bare-path + query shape keeps working, with `sandbox.StorePathScope` as the single decision point); ② **d1** — the same delete also drops the **live sandbox's** copy (`Gateway.RemoveWorkspaceFile` → `sandbox.LiveWorkspaceFileRemover`, reaching the instance through `LiveExecutorPool` — **live instances only, never creating one**), because otherwise the next sync writes it straight back (both halves pinned on real E2B). A failed sandbox removal answers `sandboxRemoved:false` + a warning instead of pretending the delete was clean | — | provided |
| ~~G8~~ | O1 | ~~identity/system files edited from outside, unsignalled~~ | user / other session / other pod | **P1 fixed (2026-09-18)**: the turn-level sample now carries fingerprints of the seven identity files; the file is named, never quoted | see the landing note in §3.3 (`identitySampleFiles`) | not provided |
| ~~**G9**~~ | O4 | ~~agent config edited from outside, unsignalled; the baseline died with the instance, and a config change takes effect precisely by replacing that instance~~ | the user | **fixed (2026-09-18)**: `model` + `prompt_mode` are sampled, and the "before" is no longer invented — it is **read from the conversation's own turn receipt**. Zero new storage, zero new write path, and inherently safe across instances and replicas. A rebuilt agent's **first turn** can now state `my configuration changed: <old> → <new>`; no readable receipt means silence. The receipt later grew into the whole snapshot (see G20) | — (G20 generalised the same mechanism to the other five families) | not provided |
| ~~**G20**~~ | O4 (the generalisation of G9) | ~~the five families (skills / tool names / memory / identity files / cron) still kept their baselines in the in-process `envTracker.last`: their first turn after a pod restart or hot reload was silent; and `handlePlanMode` plus the API's `HandleMessageStream` neither sampled nor stamped~~ | the user / harness | **fixed (2026-09-18)**: the receipt now carries the **whole `envSnapshot`** (metadata `run_receipt`); the `envTracker` type, its `last` map, its mutex and the config-only `configWas` branch are deleted, and the judgement is the pure `renderEnvDelta(prev, seen, cur)`. All three turn entry points sample and stamp | see §3.3: this is the first complete landing of the "reuse an existing durable record" place; the only boundary left is "no receipt ⇒ silence" (first turn / after compaction) | not provided |
| ~~**G19**~~ (found 2026-09-18) | O1 | the Policy C unhydrated judgement (`workspaceUnhydrated`) lived only in the **creating process**: `adoptFromLease` deliberately does not replay hydration ("the creating pod hydrated the same scope"), so an adopting replica's flag is always false ⇒ **the declaration disappears**, and the agent reads the empty `/workspace` as "my files are gone" | the sandbox-lifecycle family | P1 **fixed (2026-09-18)**: the bit rides the **instance** in `sandbox_leases.unhydrated` (`SetSandboxLeaseUnhydrated`, CAS on owner + sandbox id; cleared by Acquire/Replace so it can never be pinned onto a successor); adoption reads it back with `ex.setWorkspaceUnhydrated(rec.Unhydrated)`, and the create/replace publish points write it via `publishUnhydrated` | live E2E: pod A creates with a broken store (listing fails) → pod B adopts the same instance → still reports unhydrated; falsification (removing the adoption read) turns that E2E red | not provided |
| ~~**G10**~~ | O1 | ~~cron jobs / `HEARTBEAT.md` edited or deleted from outside, unsignalled~~ | the user | **P2 fixed (2026-09-18)**: the scheduled-job list joined the turn-level sample (`scheduled jobs added / changed / no longer exist: <name>`); a change to `HEARTBEAT.md`'s content was already covered by G8's identity fingerprints; an unreadable list is stated as unreadable and **never reported as a deletion** | — | not provided |
| ~~G14~~ | — (two sources for one thing, not a delivery duty) | ~~`HEARTBEAT.md` has **two sources**: the prompt reads the store, the trigger reads only `<home>/HEARTBEAT.md`~~ (`heartbeat.go`) | the user / an operator | **P2 fixed (2026-09-18)**: `loadHeartbeatTasks` now goes through the **same resolver the prompt uses** (`ctxBuilder.loadFileForUser("HEARTBEAT.md", ownerUserID)` — store row first, disk fallback). The owner is exactly what `chatterUserID` resolves a `SourceHeartbeat` turn to, so "what the agent sees" and "what fires" are the same bytes by construction. Shapes with no context builder (embedded/CLI) keep reading the disk copy | — | — |
| ~~**G11**~~ | O1 | ~~MCP server-side notifications are dropped~~ | an external server | **the stdio half was fixed (2026-09-18)**: capture → gate (30s per server) → reuse `mcpConfigNotify` to rebuild → the per-turn tool-set signal reports it; **HTTP still has no notification channel** (see §3.4's two boundaries — closing it needs SSE or a periodic re-list) | — | not provided |
| **G15** | — (protocol compliance, not a delivery duty) | `notifications/initialized` is never sent (neither transport) | — | P3: the spec requires it after initialize; today's servers do not require it, but it may be the precondition for a server to start pushing notifications at all | send it, but only after verifying the handshake against real servers (QC / Quandora) | not provided |
| ~~G16~~ | — (a masked secret written back through the setup API, not a delivery duty) | ~~a skill secret is overwritten by its own mask~~ (found by the 2026-09-18 dead-code scan) | the admin dashboard | **P1 fixed (2026-09-18)**: the rule now has **one home** — `mergeSkillEntry` (per entry) + `mergeSkillEntries` (per patch) — shared by **both** write paths: the global `skills.entries` sweep (merged against the stored value before it is written) and the per-agent override row (loaded with `scope.SettingInto`, then merged). The existing inline guards for providers/channels are **left alone** (different request shapes; revisit once a third variant proves the seam). Wiring it exposed a real trap: Go's JSON decoder **reuses** maps rather than replacing them, so the pre-overlay snapshot must be a **deep copy** (`cloneSkillEntries`) — otherwise the comparison runs against the very map that was mutated in place, which is how the first attempt silently kept the mask | — | — |
| ~~**G12**~~ | O2 | ~~a deferred/dropped automatic turn leaves only a slog line~~ | the harness | **P2 fixed (2026-09-18)**: each drop carries the specifics (it used to be just `count=N`); a cron drop — the user's own task — also sends a note into that chat (bounded send, no turn started); the harness's own sources are logged without bothering the user | — | wrong timing / order |
| ~~**G13**~~ | O3 (not O2) | ~~a background shell / sandbox job finishing is not pushed~~ | the agent itself | **reclassified: not a defect (formal re-review, 2026-09-18)**. δ = the process exits; the σ **exists and is recomputed from the world on every read**: `bash_output` answers `[status] exited (code=N)` (`killed` and `lost — the sandbox was replaced` are derived the same way, [sandbox_background.go](../../internal/agent/tools/sandbox_background.go) lines 350–375). By 08 §2.2.2 **D₁ exists only while a call is in flight**, so "nothing is pushed while idle" is not "there is no delivery point" — the delivery point is that read itself (O3). A recomputable criterion is **place 1** of the three placements: no carrier at all | ⛔ **Do NOT build "attach 'background job X exited' to the next tool result"**: it would introduce an in-process "exited but not yet reported" set — exactly the O4 shape (lost when the instance changes) — for a fact that can be recomputed. Trading new in-process state for an already-reachable σ is a net loss | — (reclassified) |
| ~~**G18** (= 01 §8)~~ | — (entity invariant: one path, one key — not a delivery obligation) | ~~`apply_patch` wrote the store with `r.sessionID` + the raw path while the mirror and the other two tools used `scopeSessionID()` + `wsPath()` ⇒ a single write contradicted itself: the store landed on key A, the mirror on path B, one file with two keys~~ | the agent itself | **P1 fixed (2026-09-18)**: all 6 touchpoints (3 host + 3 sandbox) unified on one resolution, and the sandboxed `apply_patch` gained the per-file write-through it never had. 3 + 1 unit tests and 1 live E2E, each falsified (reverting the fix turns them red) — 01 §8.1 | — | — |
| ~~**G17**~~ | — (scope invariant: same family as G18, one layer down) | visibility in a project's "one file tree, many containers": a project session's containers are **per chat** (deliberate — concurrent chats must not share shell state) and the preview's dev server runs in only one of them ⇒ ① a console-started preview uses the container `agent:p:<pid>`, which **no agent turn ever uses**, so agent writes can never reach it; ② a sibling chat's edit never reaches the dev server's container (docker gets this for free from the bind mount) | the agent / the user | **decided + fixed (2026-09-18, options G+H)**: **G** = the preview container is addressed by PROJECT (`previewSandboxSession`: any entry point in a project uses `session=""`, so one project = one preview container); **H** = writes and deletes are **broadcast to every live container of the project** (`LiveProjectExecutors` + `mirrorToProjectPeers` and the delete fan-out) — docker's mount semantics made explicit on a backend without one, while **keeping per-chat shells**. A partial mirror gets its own σ (§2.1). Live: two containers in one project — A writes, B reads it; A deletes, B loses it and B's sync does not resurrect it | **A was fixed too (decided the same day)**: `syncStoreScope` collapses the write-back to the project root in a project session (the same key hydrate and the file tools use) ⇒ no more `<project>/<chat>/…` duplicates, and a file created by `exec` is immediately visible to `read_file` / `list_dir`. **No migration**: copies produced before the change stay in the store (no longer refreshed, and nobody cleans them) — for the one-off cleanup see `scripts/workspace_project_chat_duplicate_cleanup.py` in [05 §6](./05-remediation-plan.md) (it only lists a copy for deletion when the same bytes survive at the project root). — no duplicates, the exec artefact lands at the project root and is visible to the tools, and a sandbox edit of an existing path is still refused (against the key the tools read) | — |
| ~~**G22**~~ (found while implementing, 2026-09-18) | — (scope invariant, the **third** instance of the same family) | the write-through's **mtime stamp** reads the store with the **sandbox scope** (`Stat(sc.agentID, sc.projectID, sc.sessionID, storeKey)`), while in a coding-root project session the tools write the **project root** (`session=""`) ⇒ that Stat always misses in project sessions ⇒ **no stamp lands**.  | the reconcile loses the cheap "same size + same mtime" test for those paths and falls back to a byte comparison (`equalToStore`) — **no functional loss** | the agent / sandbox | **fixed (2026-09-18, same round)**: the writer hands the store scope down — `WriteThroughScope(storeScope, storeKey, sandboxPath, content, previous)` (`sandbox.StoreScope`), and both the stamp and H's broadcast copies use **the scope the caller stated** instead of inferring it from the container. **Measured** (real E2B, `TestE2BLiveSyncReadsNoBodiesForStampablePaths`): whole-object reads for that path in one sync went **1 → 0** (stats still 2). `TestWriteThroughStampsWithTheStoreScopeItWasGiven` pins that the stamp uses the stated scope; **falsification**: point the pool back at the sandbox scope and it goes red at once. **Why do it when the impact is only performance**: this seam (the store scope crossing into the sandbox layer) produced three defects in one round (G21 delete, G17/A sync, this one), which meets the "invest in a boundary after 3+ changes at the same seam" test — and the fix replaces guessing with passing a fact (a port correction), adding no machinery | — |
| ~~**G23**~~ (found in review 2026-09-18; **fixed the same day**) | — (scope invariant, the **fourth/fifth** instance of the same family: one rule written as several expressions) | ~~"a project session ⇒ keys land at the project root" is written in **three places with two different predicates**: the agent side's `Registry.scopeSessionID()` uses `codingRootScope` (= `a.projectRuntime != nil && projectID != ""`), the sandbox side's `syncStoreScope()` uses `projectID != ""`, and the panel side's `StorePathScope()` uses "does the path carry a scope prefix"~~ following that thread turned up a **fifth**: the **layout table** (`pid/sid` → directory) was written out twice, in `LocalFS.scopeDir` and again in `S3.key` / `S3.scopePrefix` | agent / panel / sandbox / both store backends | ~~unreachable today, so not a defect; but the equality holds by wiring accident~~ **P3 fixed (2026-09-18)**: both rules moved into [`internal/workspace/scope.go`](../../internal/workspace/scope.go) as two pure functions — `ScopeSegments` (the layout table, shared by LocalFS and S3) and `WriteScope` (the writers' collapse, shared by the file tools and the sandbox's write-back); `Registry.codingRootScope` / `SetCodingRootScope` are deleted and `sandbox.StoreScope` is now a **type alias** of `workspace.Scope` (the port no longer carries a second copy of the fact). **One behavioural delta**: the collapse is keyed on "is there a project" instead of "is a runtime wired and are we in a project"; the two differ only in a deployment that has projects but no runtime manager — and `cmd/fastclaw/main.go` builds and wires the runtime manager unconditionally, so in such a deployment projects cannot be created either | Unit tests: `go test ./internal/workspace/ -run 'TestScopeSegments\|TestWriteScope\|TestAWriterScope'` (the layout table, the writers' rule, and that the two describe one filesystem), `go test ./internal/sandbox/ -run 'TestLayoutWriteScopeAndParserAgree\|TestProjectWritersAndTheSyncShareOneScope\|TestAProjectChatSubdirKeyIsItsOwnPath'` (layout / writers / panel parser agree; and "a project-chat-subdir key is its own object" is pinned), `go test ./internal/agent/tools/ -run TestScopeSessionIDCollapsesInsideAProject`. **Falsifications**: revert `syncStoreScope` to "no collapse" ⇒ `TestProjectWritersAndTheSyncShareOneScope`, `TestSyncWritesBackToTheProjectRootNotTheChatSubdir` and `TestSyncScopeEqualsHydrateScopeForProjects` go red; revert `scopeSessionID()` to `r.sessionID` ⇒ `TestScopeSessionIDCollapsesInsideAProject` and `TestApplyPatchUsesTheSameStoreKeyAsWriteFile` go red (both run for real) | — |
| G1–G4 | G1/G2 = **O2** (no delivery point; fixed); G3 = **O4** (in-process queue; fixed); **G4 = O1, fixed on 2026-09-18 as far as "the fact is stated"**: one store `List` per sync reports the paths the store has and the sandbox does not (the same sentence as G7a, `sandbox.StoreOnlyLine`); **attribution ("the sandbox deleted it" vs "it was uploaded later") = decided against** (2026-09-18, option a): the consequence is already delivered, a manifest would only buy the cause, and a "delivery manifest" would write thousands of rows per hydrate while a "writer manifest" would touch all six write paths — see the decision log in [05 §8](./05-remediation-plan.md) | the sandbox-lifecycle family (rebuild, deletion) | provider / harness | see 09 | see 09 §6 | not provided |
| **G31** (added 2026-09-21; the verdict on gap-register #2) | O1 (should have been produced, was not) | **when the lease store is unavailable, admission exclusion is silently let through**: as soon as `GetSandboxLease` / `AcquireSandboxLease` errors, the code keeps the local sandbox or local executor running (`e2b_executor.go:2182` / `:2434-2435` / `:2445` / `:2489`), leaving only a `slog.Warn` line — **invisible to the agent**; and this is a contract pinned by a test (`lease_pool_test.go:415` `TestE2BPoolFreshGetLeaseErrorsFailOpen`, comment verbatim "Registry errors fail open"). Inside a DB-jitter window two pods can each run the same scope — exactly the shape of the 09-18 cross-replica double-turn incident | harness | **not fixed**: the witness is not evaluable ⇒ neither seam-removal nor relocation is available (the DB is this system’s only chokepoint) ⇒ per 12 §3.2 the answer is **hold back + declare**, or keep letting it through but **must produce a σ** ("this turn did not obtain cross-replica admission") at `D₁`/`D₂`; the status quo is the ✗ "silently degrading" entry in 19.4.6.1’s negative list | see [12 §3.2](./12-lease-formal-design.md) (pick one of the two paths; either way it must be named) | **not provided** |
| **G32** (added 2026-09-21; the P-WAD row #1 of gap-register #5, **measured**) | O1 (should have been produced, was not) | **at release time the carrier holds no epoch ⇒ the release is silently dropped**: `p.leaseEpochs` (`e2b_executor.go:1868`) is the **in-process mirror** of the fencing epoch (the epoch itself is issued by the shared `sandbox_leases` row). When the registry errors at the moment of issuance, the executor is registered while the epoch **is not** (`:2215-2216`, comment verbatim `fail-open; release will not destroy the sandbox`) ⇒ from then on `Release` hands **epoch=0** to `DELETE ... AND epoch = ?` (`internal/store/sandbox_leases.go:237`) ⇒ no row matches ⇒ `deleted=false` ⇒ `:2615` returns `nil` straight away. **Controlled experiment (`internal/sandbox/lease_epoch_gap_test.go`, 2026-09-21)**: `Release` returns `nil`, destroys **0** sandboxes, logs **0** lines — and **the row still names the releasing pod itself** (owner=pod-a). It is **item-by-item identical** to a release that a sibling replica correctly fenced out: two different worlds, one observable | harness (sandbox-pool release path) | **not fixed**: the status quo is the ✗ "silently degrading" entry in 19.4.6.1’s negative list — i.e. the "let it through" half of what 12 §3.2 ruled on, **minus the declaration**. Consequence: an eviction / a panel delete **silently releases nothing** (instance and row both survive, and the next caller **adopts** the instance instead of rebuilding). **Boundary**: this is not the "witness not evaluable" case §3.2 judged (where `:2609` was recorded as "the opposite direction, and correct") — here the witness **is** evaluable and the key in the caller’s hand is empty | ① **declare** (smallest change): produce a σ when `epoch == 0` ("this release acted on no row"); ② **change the predicate**: with no epoch, CAS-delete on (owner, sandbox_id) — a fact the caller genuinely holds — but that has to be read against L4(c) in [12 §5](./12-lease-formal-design.md) (the G25 stale-epoch measurement). Either way it must be named | **not provided** |

> **G32's three ways out, and what each costs (added 2026-09-21)**:
> ① **declare**: `epoch == 0` ⇒ produce a σ ("this release acted on no row") — the acceptance test is **a number**, not a log line;
> ② **change the predicate**: with no epoch, CAS-delete on `(owner, sandbox_id)` — **this path does not survive G25** (the stale-token measurement in 12 §5),
> unless the signature is made unique per acquisition first (`owner = <pod>/<uuid>`, 12 §6), and that is itself another in-process carrier;
> ③ **remove the state**: when issuance fails, do not register (destroy it and fail `Get`).
> The cost table for all three (which stage it treats / what it buys / cost / risk / precondition) is in *认知哲学的数学原理* 19-5 §19.5.8.9 **§九**.
> **Which one to pick is undecided** — this table registers, it does not rule.

> **G22's boundary (measured 2026-09-18)**: with the write-through stamp **removed entirely**,
> `TestE2BLiveSyncReadsNoBodiesForStampablePaths` still read **0** whole objects — the criterion's ±1s tolerance
> (`sameVersion`) absorbs "the store write and the mirror write landed in the same second", so that count is a
> **measurement**, not a falsification. What does pin the stamp: the unit test
> `TestWriteThroughStampsTheSandboxCopyWithTheStoreTime` (it pins the instant in the command) and the live
> `TestE2BLiveHydrateKeepsStoreStamp` ([11 §10](./11-change-register.md), rows 10-4 / 10-5 / 10-8).

> **G24 (found 2026-09-19, in the cross-replica turn incident)** — duty `—` (**F1 preconditions**: the
> tool-writes-the-store direction): a tool's `workspace.Store.Put` has **no precondition at all**
> (last-writer-wins). T1 turned the `sandbox→store` write-back into a reconciler with preconditions
> (`BLOCKED`: refuse and report on differing bytes), but the **`tool→store` direction was never
> checked**. When a session has two writers (concurrent turns on two replicas) the same path is simply
> overwritten: on 2026-09-18 the same deliverable was written twice (pod B 16:44:07 **15 348 bytes** →
> pod A 16:50:16 **11 492 bytes**) and **the first version was lost** — silently; nobody was told.
>
> **Fix**: the primary fix is **not in this layer** — the cross-replica turn lease removes the second
> writer altogether ([docs/session-turn-integrity.md](../session-turn-integrity.md) A1).
> This layer gets two belts: ① **detect and report** (a `Stat` before and after the write comparing
> `size+mtime`; no interface change — turns a silent loss into a σ); ② **conditional write**
> (`ObjectInfo` gains `Version`; `PutIfVersion` + `ErrVersionConflict`; 3 implementations + **11**
> `.Put(` call sites) where a conflict is refused and reported — the **same policy as T1's `BLOCKED`**. **Decided 2026-09-19: take ② (family B); change list B1–B11**
> (`Move` already has this posture: it refuses to overwrite a non-empty destination,
> `ErrMoveDestinationExists`).
>
> **Note (upstream)**: this gap's upstream is that "**what one write means under concurrency**" was never
> stated (on the same port `LocalFS` does an in-place `O_TRUNC` write while `S3.Move` calls itself "Not
> atomic"). It is the formal fourth-system candidate; the analysis and the reopening conditions are in
> [00 §7](./00-formal-systems.md)'s note and §7.1 "The original design". **Current decision: not
> adopted** — A1 (removing the second writer) plus A3 (detect / conditional write) is enough.
>
> **Shape of the control action: provided** (the write was provided, but as an overwrite — the earlier version is lost, silently)

> **G25 (found 2026-09-19, while checking every existing lease against the cross-replica turn
> lease's requirements)** — duty `—` (**F1's mechanism layer**: the lease itself as the witness of a
> precondition): the sandbox lease's fencing token **resets to 1 every generation** — both of
> `AcquireSandboxLease`'s statements hard-code `epoch = 1`
> (`internal/store/sandbox_leases.go:72` / `:81`); only renew/replace increment (`:118-124` /
> `:151-160`). So `epoch` says "how many renewals this possession received", not "which generation of
> this row this is".
>
> **Measured counterexample** (a probe, deleted after the run): the same `owner` (`host:pid`, which
> repeats) re-acquires the same scope after the previous generation expired, and generation 2's `epoch`
> is back to `1`; a delayed release carrying generation 1's token —
> `ReleaseSandboxLease(scope,"pod-a",1)` — returns **`released=true`** and **the new generation's live
> row is deleted** (`GetSandboxLease` then returns `nil`). It also explains why one document contradicts
> itself: `docs/sandbox-pool-leases.md:87-89` admits "monotonic within a lease cycle only", while
> `:237-240` claims "any stale destroy request fails closed" — the latter is false.
>
> **Family and fix**: not an F2/F3 gap but **L4(c)** of [12 §3](./12-lease-formal-design.md) (a fencing
> token must be **unique per acquisition**: either the holder identity embeds a one-shot nonce, or the
> token is strictly monotonic over the row's whole life). The fix is one clause: the claim branch becomes
> `epoch = epoch + 1` (the insert branch keeps `1` — a first row has no predecessor); the falsification
> test is "the token strictly increases across two takeovers", which goes red the moment that clause is
> reverted. `session_turns` does not inherit the weakness: its holder is `<pod>/<uuid>` (unique per
> acquisition) and its token never resets — see [12 §6](./12-lease-formal-design.md).
>
> **Fixed (2026-09-19, working tree)**: the claim branch is `epoch = epoch + 1`
> (`internal/store/sandbox_leases.go:69-80`) and the insert branch keeps `1`;
> `TestSandboxLeaseEpochNeverResetsAcrossTakeover` pins "strictly increasing across two takeovers +
> a stale release refused + the live row still there", with the falsification run for real. The prose
> followed: [../sandbox-pool-leases.md](../sandbox-pool-leases.md)'s U clause and "Hardening" section
> now say fixed rather than uncovered.
>
> **Shape of the control action: provided** (the fence token is provided, but it does not fence)

> **G26 (found 2026-09-19, answering "we are supposed to be a serverless fastagent — does any design
> violate that?")** — duty `—` (**E bucket: retention**; the harness's own residency, not a σ): in the
> build **production is actually running** (`a24c0a8`, image tag
> `20260917035926-fastagent-a24c0a8`, pods started 2026-09-17T04:02Z) `session.Manager.sessions` is an
> **unbounded** in-process map — `m.sessions[key] = s` at `internal/session/manager.go:389` and `:446`
> with **no eviction anywhere** (`git show a24c0a8:internal/session/manager.go | grep -n
> 'sessionCacheMaxSize\|evictIdleLocked'` prints nothing). Every (agent, session) pair the pod serves
> stays resident for the life of the process, each carrying that session's whole LLM-facing working set
> (`Session.Messages []provider.Message`). Its size is therefore bounded by **how much history this pod
> has served**, which is precisely what a serverless process must not do — and the two production
> replicas, started in the same minute with **0 restarts**, do not have the same footprint:
> `kubectl -n production top pod` on 2026-09-19 read **96Mi** (62mx9) and **88Mi** (vxrcx) after 2d9h.
>
> **Fix (landed in the working tree, not yet deployed)**: an LRU bound — `sessionCacheMaxSize = 128`,
> never dropping the caller's own session or any session with work in flight — plus one footprint line
> per 100 cache-touching `Get`s. `TestSessionCacheEvictsIdleEntriesAndRebuildsThem` pins that a dropped
> entry is rebuilt from the authoritative store (so eviction is unobservable) and
> `TestSessionCacheKeepsSessionsWithWorkInFlight` pins that in-flight state is never dropped.
> **The first implementation was a pure idle TTL and the test caught it**: when every entry was touched
> recently nothing is "idle" and the map still grew to 136 > 128. An idle TTL is a *preference*; only
> eviction reaches the *bound*.
>
> **The budget's unit is sessions — decided 2026-09-19, after measuring both alternatives.** Byte and
> line budgets were implemented and then dropped: a byte budget needs an estimate maintained on every
> mutation path (and drifts the moment one forgets), and lines are not a fixed size either. The session
> count is the unit the cache can enforce exactly and cheaply, and the thing it must bound — how many
> entries the pod accumulates — is genuinely a count. `TestSessionCacheBudgetIsCountedInSessionsNotSize`
> pins the unit: the cache saturates at exactly the budget even when every session is 64 KiB heavy
> (**falsification run for real**: gating the sweep on line count instead saturates at 51, and the test
> goes red).
>
> **The unit and the scope, decided 2026-09-19** — the budget is **10 sessions per agent**
> (`agentSessionCacheMaxSessions`; a Manager is built per agent, so the scope is a real one and the
> constant's name now says it — §10.7 keeps the pod-wide alternative and what it would cost). Ten is
> small on purpose: what the cache saves is allocation work, because `Get` re-reads the working set
> from the store on **every** call, so keeping ten conversations warm buys the same thing as keeping a
> hundred. The residual is measured, not hidden — a session is not a fixed size (the same ten can cost
> 2.3 MiB or 77.1 MiB depending on shape, §10.3) — and eviction is now the normal path rather than the
> exception, which is exactly why §10.6's field-by-field proof exists: dropping an entry is
> unobservable except for one field, `snapshot` (§10.5), which is the accepted cost.
>
> **Shape of the control action: —** (a resource-bound problem, not a shape problem)

> **G27 (found in the same audit; fixed the same day — see the addendum below)** — duty `—`
> (**E bucket: retired resources**): two per-agent tables
> keep entries for work that is **already over**, and neither has a single `delete`:
> `tools.shellManager.shells` (`internal/agent/tools/bash_session.go:157`) and `tools.sandboxJobs.live`
> (`internal/agent/tools/sandbox_background.go:104`). Both only ever **add** (`Start` / `start`), and
> `rg 'delete\(m\.shells|delete\(s\.live'` finds nothing repo-wide. The code says so out loud:
> *"we deliberately do NOT remove the session from the map here. bash_output remains useful after exit …
> Registry.Close handles cleanup, or a future TTL eviction can be layered on top."* But
> `Registry.Close()` has **zero production callers** — the only two call sites in the repo are tests
> (`internal/agent/workspace_signal_e2e_test.go:91,124`). The Registry is constructed once per agent in
> `newAgentWithActor` (`internal/agent/loop.go:338`), so in production the table's lifetime is the
> agent's, and the agent's is the `UserSpace`'s — 30-minute idle TTL, refreshed by every use.
>
> **Cost**: a host background shell holds an `outputBuffer` capped at `bufferCap = 4 MiB`
> (`bash_session.go:25`), so the worst case is **4 MiB × every host background job this agent has ever
> started**. The sandbox table's entries are small (a path, a read cursor, a runner) — the same shape at
> lower weight. Reaching the host path additionally requires `run_in_background` **and**
> `useSandbox == false` (`internal/agent/tools/exec.go:292` refuses background work on the sandbox path
> when no executor is bound), so how *large* this gets depends on the deployment; that it is
> **unbounded** does not.
>
> **Fixed (2026-09-19, working tree)** — retirement moved onto the transition that actually happens,
> because the one that didn't (`Registry.Close`) is unreachable in production:
>
> | Table | Retired when | What is kept | Bound |
> |---|---|---|---|
> | `shellManager.shells` | the reaper, immediately after `cmd.Wait` returns | the tail that explains the exit | `shellExitedTailBytes` = 64 KiB per shell · `shellRetainedExited` = 32 exited shells per agent |
> | `sandboxJobs.live` | the first poll that observes `exited` / `missing` (there is no process handle to wait on, so the poll **is** the observation) | the entry itself (a path + a read cursor) | `sandboxRetainedFinished` = 64 finished jobs per agent |
>
> Running work is never forgotten in either table. The trim lands **before** `done` is published, so a
> reader that sees "exited" cannot be handed a tail without also being told it is a tail — and the
> sentence names the real reason (*"is no longer buffered (the shell exited; only its last 64 KiB is
> kept)"*) instead of reusing the 4 MiB running-cap wording, which would have been a false explanation
> of a real loss (08 §2.2, O1).
>
> **Cost, stated**: `bash_output` on a forgotten job answers exactly like an id that never existed —
> the message already says ids are valid only within the same agent process, and the honest contract is
> "the most recent N finished jobs stay addressable". The caps (32 shells / 64 sandbox jobs) are high
> enough that a working session never notices; they exist so the table stops being sized by history.
>
> Tests: `TestRetiredShellKeepsOnlyItsTail` (200 KB of output → ≤64 KiB kept, the end survives, the
> reader is told the exit reason), `TestRetiredShellsAreCappedAndRunningOnesSurvive`,
> `TestSandboxJobsForgetFinishedJobsBeyondTheRetention`, `TestSandboxJobsNeverForgetARunningJob`.
> **Falsifications run for real**: disable the trim ⇒ the first fails at *"retired shell holds 200022
> bytes"*; disable the shell cap ⇒ the second times out; disable the job-table cap ⇒ the third fails at
> *"72 finished entries"*.
>
> **Shape of the control action: duration** (kept long past its end: the table retains finished records forever)

> **G28 (found in the same audit, minor)** — `session.StoreAdapter.ownerCache`
> (`internal/session/store_adapter.go:55`) is a map with no bound and no eviction: one entry per
> `session_key` that adapter has ever resolved. Entries are tiny (a key → a user id) and the adapter dies
> with its `UserSpace`, so this is a note rather than a defect — recorded because the audit's test has to
> be applied uniformly, not only where a problem is expected.
>
> **Shape of the control action: —** (a resource-bound problem, not a shape problem)

> **G30 (found 2026-09-19, while checking what the budget's *scope* actually covers; resolved the same
> day)** — duty `—` (**S1's scope**): `sessionCacheMaxSessions` read like a pod-wide quota and was in
> fact **per agent** — `internal/gateway/userspace.go:1115` builds one `agent.Manager` per user space,
> and `internal/agent/manager.go:246` builds one `session.Manager` **per agent**, so a pod with K loaded
> agents could hold K × the budget. **Per agent is the intended scope** (each agent's own conversations
> are the ones worth keeping warm); what was wrong was only that the name and the comment invited the
> pod-wide reading. The field is now `agentSessionCacheMaxSessions` and §10.7 records what the pod-wide
> alternative would cost, so the choice is documented rather than implied.
>
> **Shape of the control action: —** (a naming/scope problem, not a shape problem)

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

> **How the E2B key reaches a run (recorded 2026-09-20, because "it is not in my shell" and "there is no key" are different facts).**
> Two sources, in this order: ① `E2B_API_KEY` from the environment — in the cluster it is injected from the Secret
> `fastagent-secrets` key `E2B_API_KEY` (never a ConfigMap), read once at startup into `config.Sandbox.E2BKey`
> (`internal/config/env.go:132`) and then **unset from the environment** (it is in the read-then-`Unsetenv` list at
> `env.go:236`, the same family of guard that keeps daemon secrets out of the agent's `exec`); ② an admin-saved
> `SandboxE2BKey` (`internal/setup/handlers_admin.go:352`), which **overrides** the environment one
> (`env.go:271-272`). Live tests do not read config at all — they read `FASTAGENT_E2B_LIVE=1` + `E2B_API_KEY` directly
> (`apply_patch_live_scope_e2e_test.go:50`), and the template comes from `FASTAGENT_E2B_TEMPLATE` (prod: `fastclaw-sandbox`).
> A shell where `E2B_API_KEY` is unset does **not** mean the key is unavailable — it can be taken from the cluster secret for a
> local run, which is how the proof above was produced.
>
> **The way it is actually done, and the one used from 2026-09-21 on (recorded because "there is no key on this
> machine" was read as "the live proof cannot be run"):** the key is never stored — not in a shell profile, not in
> `.env*`, not in `~/.fastagent`. The run is prefixed with a one-shot read of the cluster Secret, and the value lives
> only in that command's environment:
> `K=$(kubectl -n production get secret fastagent-secrets -o jsonpath='{.data.E2B_API_KEY}' | base64 -d) && FASTAGENT_E2B_LIVE=1 E2B_API_KEY="$K" FASTAGENT_E2B_TEMPLATE=fastclaw-sandbox go test ./internal/agent/tools/ -run TestE2BLiveApplyPatchDeleteSticks -v`.
> Reproduced that way on 2026-09-21: **pass in 23 s** (sandbox `itzcnc5sxkry7s6276x16`); with the mirror call removed,
> **red in 31 s** at *"the sandbox copy survived the tool delete: yes"*; the panel control `TestE2BLivePanelDeleteSticks`
> passed in the same session (65 s, both subtests). The consequence for this document: "the environment I can see has
> no key" is not evidence that a live proof is out of reach — **it is one `kubectl` read away**, and that is the
> command §4 G7b's live proof (and its 2026-09-21 reproduction) was produced with.

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

## 10. The harness's own residency: the test that answers "does any design violate serverless?" (2026-09-19)

"We are supposed to be a serverless fastagent, so why does pod memory keep growing with turn count?"
is not a σ question (F2/F3) and not a precondition question (F1). It is the **fourth kind of question**
this document set keeps meeting: *what may a process keep in memory, and for how long?* It sits in
[00 §7](./00-formal-systems.md)'s E bucket, under "retention / GC policy".

### 10.1 The test

Applied to every process-resident container in the gateway:

| # | Test | A container fails when |
|---|------|------------------------|
| **S1** | Its size is bounded by **work in flight**, not by how much history this process has served | it gains one entry per session / turn / job ever seen |
| **S2** | Anything **rebuildable from a durable store** must not be the only copy in memory. Judged **per field, not per struct** (see §10.5 — this is the clause the audit added) | dropping an entry at any moment would change something observable |
| **S3** | A **retired** resource (a finished job, a dead session) must be droppable without losing a fact someone still needs | "keep it forever in case the agent asks again" is the design |

One corollary is used below, and is the whole reason §10.3 exists: **a count bound is only a proxy for a
byte bound, and the proxy is as good as the largest element.** "At most 128 sessions" bounds memory
only if a session is itself bounded.

### 10.2 The audit (every container, verdict, evidence)

| Container | Bounded by | Verdict |
|-----------|-----------|---------|
| `session.Manager.sessions` + each `Session.Messages` | LRU 128 in the working tree; **nothing at all in the build production runs** | **fails S1 in `a24c0a8`** (G26; fixed in the working tree). Its byte bound is still open — §10.3 |
| `tools.shellManager.shells` | nothing in the running build; **now**: retired at exit (tail-trimmed) and capped at 32 | **failed S3** (G27), **fixed 2026-09-19** |
| `tools.sandboxJobs.live` | nothing in the running build; **now**: retired on the poll that observes the end, capped at 64 | **failed S3** (G27), **fixed 2026-09-19** |
| `session.StoreAdapter.ownerCache` | nothing; dies with its `UserSpace` (30-minute idle TTL) | minor (G28) |
| `gateway.userSpaceRegistry.spaces` | 30-minute idle TTL, refreshed on use (`idleTTL`, `startEvictor`) | **passes S1** — bounded by *concurrent users*, and holding a space is what "this user is working" means |
| `gateway.dedup` (`sync.Map`) | TTL 60 s + a sweep every 30 s (`cleanupDedup`, started at `gateway.go:853`) | **passes** |
| `gateway.deferredTurns.items` | drained every 1 s (`run`), `maxWait` 5 min, expiry speaks a σ | **passes** |
| `agent.EventHub.subs` | deleted on unsubscribe, and all three production subscribe sites `defer unsubscribe()` (`internal/setup/handlers.go:1284`, `:1566`, `handlers_team_chat.go:187`) | **passes** |
| `gateway.modelCostCache.cache` | keyed by configured (provider, model) pairs | **passes** |
| `sandbox.E2BExecutorPool.executors` / `leaseEpochs` | one entry per scope; removed by `takeExecutor` on release / sleep | **passes S1** — bounded by scopes holding a live lease |

The three failures share one shape: **a table keyed by history with no retirement path.**

### 10.3 What could be measured, and what could not

"Read the online footprint" was the wrong plan, and the user was right to reject it. The in-process
footprint line is **not in the deployed build** (`a24c0a8` predates it), and neither `a24c0a8` nor the
working tree registers `net/http/pprof` (`rg 'pprof|expvar|/metrics' cmd internal` → nothing), so a heap
profile cannot be pulled out of a live pod without shipping an endpoint. What **is** available without
deploying anything:

| Without deploying | What it gives |
|---|---|
| `git show <prod-tag>:<file>` plus `rg` for `delete` / eviction | the **categorical** answer: which containers grow with history. This is a property of the source, not of the running pod |
| `kubectl -n production top pod`, `.status.startTime`, restart count, image tag | the symptom and its shape: same start minute, **different** footprints |
| A local probe driving the real `Manager` and reading `runtime.ReadMemStats` deltas | **bytes per session** — the coefficient that turns "N sessions" into "N bytes" |
| A read-only query against the production store (`sessions` count, message bytes) | how much history the pod was actually asked to hold |
| `kubectl exec … kill -QUIT 1` → `kubectl logs` | a goroutine dump. Useful for leaked goroutines; useless for the heap |

**Measured** (local probe, real `session.Manager`, `runtime.GC()` before and after; the probe was deleted
after the run). The cache saturates at its bound, so every row is "128 sessions resident":

| Message shape per session | Resident heap | Per session |
|---|---|---|
| 20 messages × 512 B | **2.3 MiB** | 18.5 KiB |
| 40 messages × 2 KiB | **12.1 MiB** | 96.8 KiB |
| 30 messages × 10 KiB (≈300 KiB of text — roughly what compaction leaves behind) | **38.5 MiB** | 308.4 KiB |
| 60 messages × 8 KiB (tool-output-heavy) | **62.1 MiB** | 496.7 KiB |
| 60 messages × 10 KiB | **77.1 MiB** | 616.8 KiB |

**Measured against the production store** (read-only; via a temporary `pgprobe` pod in the `production`
namespace, which deleted itself after the query — the database is VPC-private, so `psql` from outside
cannot reach it):

| Production `sessions` | value |
|---|---|
| rows | **108** |
| rows updated in the last 2 days | **5** |
| `messages` as stored (`pg_column_size`, i.e. TOAST-compressed) | **9.1 MiB** total · 449 KiB max · 87 KiB avg |
| `messages` as text (`octet_length(messages::text)`, i.e. what the process holds) | **31 MiB** total · **1.85 MiB max** · 297 KiB avg |

**The second row is the one that matters, and taking the first would have been a mistake**: JSONB is
TOAST-compressed on disk, so the stored size understates the resident size by 3.4×. What the process
holds is the *text*: **31 MiB** for the whole dataset. Add Go struct overhead per message, and at
`GOGC=100` (no `GOMEMLIMIT` in the deployment env) the runtime's target is roughly **2× live heap** —
which lands in the **80–100 MiB** band. That is exactly where all four running processes sit, and the
narrow band *across two builds and two very different uptimes* (prod `a24c0a8` at 2d9h: 96Mi / 88Mi;
dev `8984c99` at 21h: 87Mi / 82Mi) is explained by the same thing: a **finite** dataset (~31 MiB) that
every replica caches in full. A container growing ∝ served history would not plateau; the fact that
the ceiling here *is* the dataset is what makes the unbounded cache the dominant term rather than a
slow leak.

> The two environments do **not** share a database (their `STORAGE_DSN` secrets hash differently), so
> the 31 MiB figure is production's; the band is the *shape* both environments show, and dev's dataset
> is smaller. The band's meaning is the same in both: a ceiling set by data, not by uptime.

> **The "expected ~15 MiB" baseline was never reachable for this binary.** The floor is not "an empty
> process" — it is what one or more `UserSpace`s hold resident (agents, each with loaded skills, prompt
> modules, memory and the full tool catalog) plus the Go runtime and this dataset. The useful target is
> not a number picked from intuition but a **budget with a mechanism** (see 2 below and G27).

Two conclusions, plus the decision the second one forced:

1. **The unbounded cache explains the symptom, quantitatively.** The build production runs caches every
   session it has loaded, forever; the dataset it can hold is ~31 MiB of text, and the resulting live
   heap lands at roughly 2× that in RSS.
   One source fact explains why a *session* can be that heavy: the largest single session is **1.85 MiB
   of text = ~460K tokens by `EstimateTokens`** (`len(Content)/4`, `internal/agent/compaction.go:31`),
   i.e. **well past the 80K-token compaction trigger** — because the trigger measures only `Content` and
   tool-call arguments, while what is stored (and therefore resident) also carries `Metadata`,
   `Thinking` and `RawAssistant`. So "compaction ran" does not imply "this session is small".
2. **A count bound is not a byte bound — and the budget is still counted in sessions (decided
   2026-09-19).** Byte and line budgets were both implemented (`sessionCacheMaxBytes` /
   `sessionCacheMaxLines`) and both dropped: a byte budget needs an estimate maintained on every path
   that mutates history, and lines are no more fixed-size than sessions. What the decision buys is a
   bound the cache can enforce exactly, cheaply and predictably — and what it costs is stated explicitly
   rather than implied: **it caps how many sessions are remembered, not how much each one weighs**, and
   with 108 sessions in production today the 128-session budget does not bind there yet. Choosing a
   budget small enough to shrink today's resident set is a separate product decision; the number that
   decision needs is right here, and the cost of each eviction is a rebuild that `Get` already performs
   on every call (it re-reads from the store unconditionally).

### 10.4 Why this is not a fourth formal system

F2 says "a change to the world must be observable by the agent". S1/S3 are its dual applied to the
harness's own memory: **state that is not a fact about the world must not be retained, and state that is
derivable must not be the only copy.** The skeleton is the one F2 uses everywhere else — *recompute from
the authoritative source; do not remember* — so this is a new instance of an existing shape (a C in
[00 §7](./00-formal-systems.md)'s vocabulary), not a new question with a new judgement form. What it
does add to the checklist is one question that must be asked of every new table: **"what removes an
entry, and is that path reachable in production?"** G27 is what happens when the answer is a `Close()`
nobody calls.

### 10.5 What this audit put back into the method: "rebuildable" is a per-field property

The eviction rule rests on S2: *anything rebuildable from a durable store must not be the only copy in
memory.* Applied to `Session` it looked trivially true — the working set is re-read from the store on
every `Get`, so an entry can be dropped at any moment. Walking the struct field by field, instead of
trusting that summary, found one field where it is false:

| `Session` field | Rebuildable from the store? |
|---|---|
| `Messages` | **yes** — `getByKey` re-reads it on every call, so a dropped entry is unobservable |
| `channel` / `accountID` / `chatID` / `projectID` / `runReceipt` | yes — columns on the session row |
| `snapshot` (the `/retry` restore point) | **no** — `Undo()` restores from process memory only; there is no snapshot column, and `HasSnapshot` simply reports `false` after a drop |
| `turnActive` / `turnWaiters` / `steerBuf` / `turnFence` / `fenceLost` | no — but they exist only while work is in flight, and the sweep is forbidden to drop a busy session |
| `lastTouched` | no, and it does not matter: it is the cache's own bookkeeping |

So the correct statement of S2 is **per field, not per struct**: *for every field, either it is
rebuildable, or it is confined to entries the sweep may not drop, or dropping it is an accepted cost
that is written down.* `snapshot` is the third case, and writing it down is the whole point:

- evicting a session discards its undo point, so `/undo` after a `/retry` can answer "nothing to undo"
  — which is the same answer it already gives whenever the next turn is served by another replica,
  because the snapshot was always pod-local. The bound adds one more way to lose it; the loss was
  already in the contract. What the method requires is that this be **stated**, not discovered.

This is the one place the audit pushed back on the method. It is not enough to change a principle, but
it sharpens the checklist: the field-by-field question — *which parts of this struct are **not**
rebuildable, and who pays for them?* — is now part of S2 in §10.1. It is also what would have caught a
whole-struct eviction that silently dropped undo state if `Snapshot()` were called on every turn
instead of only by `/retry`.

### 10.6 When the budget actually binds: the eviction-safety audit

The budget is **10 sessions per agent**, and a production agent holds more than ten sessions, so
eviction is the **normal** path rather than an exception — which is why "the store rebuilds it" is not
a sufficient argument and had to become a check. "Dropping an entry is unobservable" was verified field
by field, against the code that reads each field:

| `Session` state | Lost when the entry is dropped | Why that is unobservable |
|---|---|---|
| `Messages` | yes | `getByKey` re-reads the working set from the store on **every** call, hit or miss (`internal/session/manager.go:625`) — so a rebuilt entry is identical to a warm one, and eviction costs **zero extra store traffic** |
| `channel` / `accountID` / `chatID` | yes | routing resolves through `resolveOrMintKey`, which asks the **store** (`ResolveActiveSessionKey`) and never the map — so eviction cannot mint a second key for one conversation |
| `projectID` | yes | `SaveSession`'s `ON CONFLICT … DO UPDATE` deliberately does **not** list `project_id` (`internal/store/database.go:2985`), so a rebuilt entry carrying an empty hint can neither blank a row's project nor resurrect an old one; the panel and the turn entry points both resolve the project through `LookupSessionProject`, i.e. through the store |
| `runReceipt` | yes | the environment baseline is read from the **stored message metadata** (`session.RunReceiptOf` → `envBaselineFromReceipt`, `internal/agent/env_changes.go:352`) — which is precisely why G9/G20 moved it there |
| `LastConsolidated` | yes | nothing reads it: `UnconsolidatedCount` and `MarkConsolidated` have **zero references repo-wide** (G29) |
| `snapshot` (the `/retry` restore point) | yes | **this one *is* observable** — §10.5. It is the accepted cost of the bound |
| `turnActive` / `turnDepth` / `turnWaiters` / `steerBuf` / `turnFence` / `fenceLost` | no — the sweep skips busy sessions | and a leftover steer cannot outlive a turn: `EndTurn` hands it back and `flushLeftoverSteer` appends it to history (`internal/agent/loop.go:2150`, `:2236`) |
| `lastTouched` | yes | cache bookkeeping — the entry it graded no longer exists |

Tests that pin this at the operating budget: `TestEvictionIsUnobservableAtTheOperatingBudget` (40
sessions → the budget holds and every one still reads back exactly what it said; **falsification run for
real**: disable the eviction and it fails at *"cache holds 40 sessions, want <= the budget 10"*),
`TestSessionCacheEvictsIdleEntriesAndRebuildsThem`, `TestSessionCacheBudgetIsCountedInSessionsNotSize`.

Two things this audit produced that outlive it:

- **a recorded position with its reasons** — 10 per agent, because the cache's only product is saved
  allocation (the store is read on every `Get` regardless), so a small warm set buys what a large one
  buys; and because a binding budget is one whose behaviour is exercised rather than assumed;
- **G29**: `LastConsolidated` and its two accessors (`UnconsolidatedCount`, `MarkConsolidated`) are dead
  state on the hottest struct in the package — nothing reads them, nothing persists them, and the four
  places that zero the field do so for a reader that does not exist. Recorded rather than deleted here,
  because "zero references" has twice been a lead rather than a verdict (10 §9).

### 10.7 Scope: per agent, and what a pod-wide budget would cost

Decided 2026-09-19: the budget is **per agent** (`agentSessionCacheMaxSessions = 10`). The alternative
was considered and is recorded here because the constant used to read as if it were already pod-wide:

| Shape | What it bounds | What it costs |
|---|---|---|
| **per agent (chosen)** | one agent's warm conversations; a pod with K loaded agents may hold K × 10 entries | nothing extra: the Manager already exists per agent, and eviction is local |
| pod-wide | the whole process, one shared budget of N entries | a shared budget object the composition root creates and every Manager registers with, plus a global sweep that gathers candidates from all Managers and drops the globally oldest |

Two hazards make the pod-wide shape a genuine change rather than a constant, and they are the reason it
is recorded instead of quietly implemented:

1. **Lock order.** `getByKey` inserts and sweeps while holding `m.mu`; a global sweep would hold the
   cache's lock and then each `m.mu`. The per-Manager sweep would have to stop running under `m.mu`
   first, or the two orders deadlock under load.
2. **Lifecycle.** A registry of Managers is itself an unbounded structure unless something unregisters
   them — and nothing tears a `session.Manager` down today (the same missing-teardown shape as
   `Registry.Close` in G27). Adding a registry to fix an unbounded cache, without a teardown to remove
   entries from it, would trade one unbounded structure for another.

A per-Manager *share* of the budget (budget ÷ live Managers) avoids both hazards but makes the bound
drift with load, which is worse than a stated per-agent bound: an operator can reason about "10 per
agent"; nobody can reason about "N ÷ however many agents are loaded right now".

### 10.8 The other missing teardown, decided rather than assumed

G27 named a second missing-teardown fact and left it as a question: `Registry.Close()` still has no
production caller, so when a `UserSpace` is evicted for idleness the agent's host background shells are
**not** killed — they keep running (now holding at most 64 KiB each, G27).

**Decision (2026-09-19): leave it unwired.** Killing a user's background process because they went quiet
for thirty minutes changes a product promise, and the only version of it that does not create a silent
destruction is the one that also *says so* (a σ: "your background tasks were reclaimed") — which needs a
stated idle parameter and a delivery point. Until that exists, "not wired" is the honest option: the
resource cost is bounded and visible, whereas "wired" would make a user's dev server disappear with
nobody explaining it. Reopen when the idle-reclaim promise is defined; the parameter should be the
existing 30-minute idle TTL rather than a new knob.
