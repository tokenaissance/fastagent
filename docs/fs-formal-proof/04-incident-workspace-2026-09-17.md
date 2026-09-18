# 04 · Incident record: deliverables silently reverted (2026-09-17)

> Status: located, explained, fix awaiting scheduling · last verified: 2026-09-17
> Relation: this document is the empirical evidence behind [01](./01-current-implementation.md) /
> [02](./02-semantics-and-architecture.md) / [03](./03-state-machine-and-timing.md). Those three describe
> the mechanism; this one describes how the mechanism was triggered in production.

## 1. Symptom

On 2026-09-17, in production session `OvDLzEPKcEK84Hpfq0U6LK` (agent `agt_cda27bbfbf4a84e2dfa6`,
no project, channel=web, 132 messages): the agent twice reported that it had "restored the reverted
deliverables", and the same files were then overwritten back to earlier versions automatically, with no
error returned to the agent or the user at any point.

Byte counts of the three files, newest to oldest:

| File | What the agent believed was current | After the revert | Restored by a write | Reverted again |
|------|-------------------------------------|------------------|---------------------|----------------|
| `byo-account-design.html` | 50,282 (with appendices A/B) | 41,262 | 55,527 (12:07) | **41,262** (12:15) |
| `qc-email-draft.md` | 6,893 (5-question version) | 5,705 (4-question version) | 6,893 (12:07) | **5,705** (12:15) |
| `todo.md` | 462 (this round's plan) | 580 (previous round) | 462 (12:07) | **580** (12:15) |

Files created this round that never entered the sandbox (`quantconnect-mcp-integration.md` 19,041,
`quantconnect-overview.html` 23,285) stayed **intact throughout**.

## 2. Evidence chain

### 2.1 Durable storage (S3, DO Spaces, bucket `fastagent-nyc3`)

Prefix `prod/agt_cda27bbfbf4a84e2dfa6/sessions/OvDLzEPKcEK84Hpfq0U6LK/`:

| Object | Bytes | LastModified (UTC) |
|--------|-------|--------------------|
| `byo-account-design.html` | 41,262 | 2026-09-17T12:15:16 |
| `qc-email-draft.md` | 5,705 | 2026-09-17T12:15:16 |
| `todo.md` | 580 | 2026-09-17T12:15:17 |
| `quantconnect-mcp-integration.md` | 19,041 | 12:06:13 |
| `quantconnect-overview.html` | 23,285 | 08:19 |
| `quantconnect.html` / `quantconnect-gonogo.html` | 86,580 / 24,083 | 15:57 / 15:42 |

The three reverted objects' `LastModified` values **land in the same evict sync** (12:15:16–17) and
clearly differ from the others — which directly rules out "some editing tool wrote garbage" and points
at a batch overwrite.

### 2.2 Gateway logs (production namespace)

```
05:25:33  e2b sandbox hydrated            sandboxID=iknoadnp4ho8un3yhmjr2 workspaceFiles=1
05:25:50…07:30:13  sandbox sync: snapshot failed ×42   (over the 32.0 MB cap)
06:26:14 / 07:38:05 / 07:52:58  extend timeout … 404   (that sandbox no longer exists)
07:56:44  e2b sandbox expired, recreating
07:56:53  e2b sandbox hydrated            sandboxID=iydyr4nz1rt1kxq57a1mh workspaceFiles=10
07:59:21  e2b sandbox adopted (local cache stale)
08:09:47  sandbox synced to workspace store  cause=evict      files=2
08:24:18  sandbox synced to workspace store  cause=evict      files=3
12:05:26  sandbox synced to workspace store  cause=post-exec  files=1
12:15:17  sandbox synced to workspace store  cause=evict      files=3
```

Note two timestamps: the three files were first written by a host tool at **08:04**, while the second
sandbox `iydyr4nz1rt1kxq57a1mh` hydrated at **07:56:53** — that is, **it held the old version from the
moment it was born** and was never updated for the remaining 4.5 hours of the session (host tools do not
mirror, see [01](./01-current-implementation.md) §3.1).

In the same period there were many refused syncs (background to this incident, see §2.4):

```
sandbox sync: snapshot failed … over the 32.0 MB cap …
  Largest entries: 1371391 /workspace, 1371387 /workspace/qcdocs, 584799 /workspace/qcdocs/.git
```

### 2.3 Session messages (`session_messages`, increasing seq)

| seq | Time (UTC) | Event |
|-----|-----------|-------|
| 258 | 08:04:09 | `edit_file` writes `qc-email-draft.md` → 6,893 |
| 264 | 08:04:36 | `apply_patch` updates `qc-email-draft.md` |
| 279 | 08:15:16 | `list_dir` shows `byo-account-design.html` 50,282 / `qc-email-draft.md` 5,705 |
| 280 | 08:15:26 | the agent notices the revert for the first time ("5,705 is exactly the size of the original write") |
| 283 | 08:15:44 | `write_file` repairs `qc-email-draft.md` → 6,893 |
| 307 | 12:05:45 | `write_file` creates `quantconnect-mcp-integration.md` → 6,748 |
| 309 | 12:06:13 | `edit_file` on that file → 19,041 |
| 311 | 12:06:17 | `list_dir` shows `byo-account-design.html` **41,262** ← the agent's second discovery |
| 318–320 | 12:07:00 | the three files are repaired (6,893 / 55,527 / 462) |
| 322 | 12:07:06 | `list_dir` confirms the restore |
| — | 12:15:17 | **the evict sync overwrites them again** (no session activity, see §2.2) |

### 2.5 Reading the live sandbox directly (the closing evidence, re-checked 2026-09-17)

Via `sandbox_leases` we located the live sandbox `iydyr4nz1rt1kxq57a1mh` (state=paused), obtained a token
through `POST /sandboxes/{id}/connect`, and read `/workspace` directly:

```
GET /workspace/byo-account-design.html : 200, 41,262 bytes
GET /workspace/qc-email-draft.md       : 200, 5,705 bytes
GET /workspace/todo.md                 : 200, 580 bytes
GET /workspace/sessions/OvDLzEPKcEK84Hpfq0U6LK/byo-account-design.html : 404
```

The three numbers match the byte counts written to the store by the third, fourth and fifth syncs
exactly, and match the current S3 objects; the 404 on the fourth request rules out "there is another copy
under the session prefix". After reading, `POST /sandboxes/{id}/pause` restored the state.

Byte-level re-check: `md5sum byo-account-design.html` inside the sandbox =
`305a43137bf54d18235ef50647579413`, identical to the S3 object's ETag (what the store holds *is* the
sandbox's copy, not another version of the same length). And per `stat`: all three files'
**birth times equal sandbox B's hydrate moment, 07:56:53**, while their mtimes preserve the store's
`LastModified` from that time (07:39–07:41) — consistent with "hydrate writes S3's modification time into
the tar", and evidence that **mtime cannot distinguish old from new**.

**This step upgrades §3's root cause from inference to measurement**: the sandbox copy really did stay at
the hydrate-time version while the store's author was a host tool — the fork between the two copies is a
physical fact, not a modelling assumption.

Postscript: the second self-audit also found two attachments present in the store but **not in the
sandbox** (`mcp-oauth-authorization-flow.md`, `mcp-oauth-design.md`, uploaded at 12:04), showing that
`Sync` is add-only and that there is no "re-hydrate after an upload" mechanism; the consequences and the
response are in [07-formal-rootcause-and-fix.md](./07-formal-rootcause-and-fix.md) part 5.

### 2.6 A local audit of historical reverts (7 days, 2026-09-17)

Gateway logs are not forwarded and are lost on a pod restart (only the ~18 hours from 04:02 that day
remain), but **the long-term evidence lives in two other sources**: `session_messages` records every tool
write's byte count and edit delta, and object storage records each object's current size and
`LastModified`. The audit script
[scripts/workspace_revert_audit.py](../../scripts/workspace_revert_audit.py) decides from them:

```
expect ≥ the last host write + (the net gain of edits after it)
if the store is clearly smaller than expect, and the agent's own edits cannot explain it → a revert
```

Data sources (one command each; the script only consumes TSV):

```sql
-- 1) messages (the JSON of tool_calls, and the byte counts in tool results)
select session_key, to_char(created_at,'YYYY-MM-DD HH24:MI:SS'), role,
       replace(coalesce(content,''), chr(10), ' '),
       replace(coalesce(tool_calls,''), chr(10), ' ')
  from session_messages where created_at > now() - interval '7 days' order by session_key, seq;
```

```bash
# 2) the object listing
aws --endpoint-url "$ENDPOINT" s3api list-objects-v2 --bucket fastagent-nyc3 \
    --prefix prod/ --max-items 100000 --output json
```

Seven-day result (3,215 messages / 7,274 objects):

| Session | Path | Last host write | Net gain of later edits | Lower bound | Current store value | Shortfall |
|---------|------|-----------------|-------------------------|-------------|---------------------|-----------|
| `OvDLzEPKcEK84Hpfq0U6LK` | `byo-account-design.html` | 13,915 | +41,706 | 55,621 | 41,262 | **14,359** |
| `OvDLzEPKcEK84Hpfq0U6LK` | `qc-email-draft.md` | 6,893 | 0 | 6,893 | 5,705 | **1,188** |
| `82c14309-c2ec-4862-a846-3fde81168f68` | `reddit_pull.py` | 2,244 | 0 | 2,244 | 1,976 | **268** |

**3 paths / 2 sessions / a lower bound of 15,815 bytes**, clustered in the class of sessions where the
sandbox outlived a host edit — consistent with the mechanism (in other sessions the syncs are almost all
new `exec` artefacts, which is the allowed branch).

Two calibrations:

- the shortfalls for `qc-email-draft.md` and `reddit_pull.py` are **exact** (no edits followed);
- the 14,359 for `byo-account-design.html` is an **upper** estimate derived from tool text: the incident
  itself has a `list_dir` at `12:07:06` recording 55,527 bytes, while the store now holds 41,262, so
  **the exact shortfall is 14,265**. The 94-byte difference comes from estimating the patch/edit text
  (within 1.7%), which shows the method and the measurement are self-consistent.

Eight more paths in the same session (`quantconnect*.html`, `qc-lean-mcp/*`,
`quantconnect-mcp-integration.md`, …) show "the store disagrees with the last host write" but their edit
text is not enough to decide, and the script deliberately excludes them — **better to under-report**.
So 15,815 bytes is a lower bound, not the whole picture.

> The report at the time named only three files (41,262 / 5,705 / 580). This audit confirms two of them
> as quantified data loss (14,265 + 1,188 bytes) and **found a third site in another session the same
> day** (`reddit_pull.py`, 268 bytes) — so this was not a single-session event.

### 2.4 Timeline alignment (against [03](./03-state-machine-and-timing.md) §4)

| Model event | What actually happened |
|-------------|------------------------|
| `Hydrate` (`X ← S`) | 05:25:33, the sandbox is created; the three files' `X` stops at the then-current version |
| `HostWrite` (`S ← new version`) | 08:04–08:19 several times; 12:05–12:07 again |
| `Sync()` | 08:09:47 / 08:24:18 / 12:05:26 / 12:15:17 |
| Symptom | `S` is overwritten with `X`'s old content |

The 12:15:17 run in particular shows the nature of the problem: it happened **after the user session had
ended and the agent had stopped** (the last message is 12:12:02), triggered by idle eviction.

The five syncs' byte counts against the sandbox and object storage (§2.1 + §2.5):

| Sync | cause | files | Bytes written | Agrees with the direct sandbox read |
|------|-------|-------|---------------|-------------------------------------|
| 08:09:47 | evict (sandbox A) | 2 | the old versions | — (A was destroyed) |
| 08:24:18 | evict (sandbox B) | 3 | 41,262 / 5,705 / 580 | ✅ |
| 12:05:26 | post-exec (B) | 1 | (a single file) | ✅ |
| 12:15:17 | evict (B) | 3 | 41,262 / 5,705 / 580 | ✅ (the current store values) |

## 3. Root cause

> **One logical path exists as two copies (the store and the sandbox's `/workspace`), and the write-back
> channel `syncSnapshot` is one-directional with "are the byte counts equal?" as its criterion for
> whether an overwrite is needed.**

- that criterion's correctness presupposes "there is only one writer", which holds by construction on
  docker thanks to the bind mount and does not hold on e2b / boxlite;
- the host file tools (`write_file` / `edit_file` / `apply_patch`) write the store **without updating the
  sandbox**, so the sandbox's old copy becomes the "looks newer" candidate;
- the criterion only looks at size: different size → overwrite with the sandbox's copy. The old copy
  therefore overwrites the new version.

The full mechanism, the semantic mismatch and the state model are in
[01](./01-current-implementation.md) / [02](./02-semantics-and-architecture.md) /
[03](./03-state-machine-and-timing.md) respectively.

### 3.1 Why "fix the store from the host side" does not work

The repair action is itself a `HostWrite`, changing only `S`; the old copy in `X` is still there, and the
next `Sync()` promptly overwrites `S` back to the old value. In the incident the agent actually repaired
twice (08:15, 12:07) and both failed — the second failure's log line is the 12:15:17
`cause=evict files=3`.

**As long as the sandbox copy is alive, any host-side repair is temporary.** This is the point most
easily misdiagnosed as "a tool bug" or "an agent hallucination".

### 3.2 Why "new files are fine"

`Sync()` is trustworthy for paths the store does not have (see [03](./03-state-machine-and-timing.md) §6).
All three victims were paths that existed at `Hydrate` time and were later changed by a host tool. So the
agent's observation that "the reverted files were the ones that had been through `apply_patch`" is a
**correlation**: the real criterion is "the path has an old copy inside the sandbox".

## 4. Blast radius

### 4.1 Every session on the same backend

The trigger conditions are independent of the user and of agent behaviour; they depend only on
"a separated backend + the path exists in the sandbox + the host wrote it". Write-back counts over the
same 48-hour window (logs from both gateway replicas):

```
cause=post-exec  21
cause=evict       4
sessions involved: OvDLzEPKcEK84Hpfq0U6LK(4), Tnv9L1hqRzoNgrwUDtfPv8(9), 82c14309-c2ec-4862-a846-3fde81168f68(12)
```

Those three sessions' records only show that **a write-back happened**, which is not the same as
**a revert happened** — separating the two needs the diagnostic means in §4.2. This is also the
observability gap the post-mortem exposed.

### 4.2 Existing diagnostic means (read-only)

```bash
# 1) an object's last write time (is it later than a repair in the session?)
aws --endpoint-url https://nyc3.digitaloceanspaces.com s3api head-object \
  --bucket fastagent-nyc3 --key "prod/<agent>/sessions/<sid>/<path>"

# 2) write-back events (files=N, cause)
kubectl -n production logs <gateway-pod> --timestamps \
  | grep "session=<sid>" | grep -E "synced to workspace store|snapshot failed|hydrated"

# 3) byte-count changes inside the session: tool-role messages carry byte counts
#    session_messages.session_key = '<sid>' and role = 'tool'
#    messages look like "Written 554 bytes to todo.md" / "Edited x.html (1 replacement(s))"
```

This only reaches the level of "inference" — which is what §5 has to add.

## 5. Attribution corrections (to prevent misdiagnosis)

Two conclusions proposed during the post-mortem need correcting:

| Claim that appeared | Correction |
|---------------------|-----------|
| "`apply_patch` caused the reverts" | `apply_patch` is merely the **most thorough trigger** (it never mirrors into the sandbox); `write_file` / `edit_file` in a non-coding session also only write the store and also trigger it. The tool is not the cause — "two writers + a size criterion" is |
| "the agent misreported or wrote the wrong thing" | No. The writes at 08:15 and 12:07 both succeeded (tool results plus `list_dir` re-checks); the reverts were caused by the evict syncs at 08:24 / 12:15 |

## 6. Historical attribution: did leases introduce it?

**No.** The logical defect first appeared in commit `950070b` on 2026-04-20
("feat: cloud-ready architecture — stateless gateway, per-key scoping, provider tools") — the same
commit contains both the host `write_file → workspaceStore.Put` and `flushIfSupported`'s "skip when the
sizes are equal, otherwise overwrite with the sandbox".

Timeline (in the `fastagent` repo):

| Date | commit | Content | Relation to the defect |
|------|--------|---------|------------------------|
| 2026-04-20 | `950070b` | `LifecyclePool` appears | **the defect is born** (host writes the store + a size criterion writes back) |
| 2026-04-28 | `047f107` | per-session workspace and sandbox isolation | the scope changed, the criterion did not |
| 2026-05-01 | `527b8fb` | E2B hydrate + **post-exec sync** | **first amplification**: write-back goes from "only on eviction" to "after every exec" |
| 2026-05-07 / 05-08 | `5c0d74b` / `7c98b37` | `edit_file` / `apply_patch` | modifying existing files from the host becomes ordinary practice |
| 2026-05-09 | `1fbbbb8` | `mirrorSandboxWrite` | adds one host→sandbox mirror (covering only some paths) |
| 2026-09-06 | `e359bf0` et al. | cross-pod E2B leases, adoption, pause/resume, epoch | **second amplification** (not introduction) |

### 6.1 Why it might not have been visible before leases

The trigger needs three conditions at once (see [03](./03-state-machine-and-timing.md) §7). The first
version had only docker implementing `SnapshotWorkspace`, and docker is a bind mount — the two copies are
one and the same, sizes always match, everything is skipped. E2B only implemented
`SnapshotWorkspace` in `527b8fb` (05-01), and only then did "one path, two contents" really become
possible.

The precise statement: **the logical defect was there on 04-20, but it can only be triggered on a
separated backend.**

### 6.2 What leases actually changed

| Change | Impact |
|--------|--------|
| cross-pod adoption (`e2b sandbox adopted from shared lease`) | the pod writing files and the pod taking the snapshot may be different processes, so neither side can arbitrate from local state |
| `pause/resume` + a 10-minute idle sleep | one sandbox instance spans many turns and long user waits, so the old copy lives much longer (this incident's sandbox lived about 7 hours) |
| longer-lived, more shared instances | the latency of a revert expands from "within one session" to "any eviction hours later" |
| epoch / CAS hardening | solves "the same sandbox held by two pods at once", which is unrelated to "one file with two write paths", so it did not incidentally fix this defect |

Conclusion: leases raised the probability from "occasional" to "the norm" without touching the
arbitration logic.

## 7. Post-mortem takeaways (reusable discipline)

1. **"No error was returned" does not mean "nothing happened"**: this incident's first two reverts were
   noticed by the agent only by comparing byte counts, and the third (12:15) still has no automatic alert.
   Any path that asynchronously overwrites user data in the background must produce observable output.
2. **Verify against the durable layer**: `list_dir` / `read_file` read the store, so a re-check through
   them is valid; had it been done against the sandbox view (`exec`), the conclusion would have been the
   opposite.
3. **A repair must cover the source side**: fixing the store without fixing the sandbox copy makes the
   repair temporary (§3.1).
4. **Correlation ≠ causation**: `apply_patch` correlates strongly with the reverts, but the deciding
   condition is "the path exists in the sandbox" (§3.2).
