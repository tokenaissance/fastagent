# 11 · Change register: formal system → code → UT → live e2e

> Status: register (2026-09-18) · It answers one question: **given the file-system formalisation (F1/F2/F3)
> and the state-observability principle, what did we change, and where are the UT and the live e2e for each
> change?**
> **Deployment status**: every row's "deployed" column is ❌ — all of it lives in the **working tree**
> (uncommitted, undeployed). Production runs `HEAD` (2026-09-17), which contains **none** of it. See the
> "deployment status" banner in [10](./10-harness-state-audit.md).
> Division of labour: **10 §4** tracks the *state of the gaps*, **05** holds the *plans and decisions*, **this
> document** maps *change points to their evidence*.

## 0. How to read this

| Column | Meaning |
|--------|---------|
| **Formal (duty)** | One of the three formal systems in [00](./00-formal-systems.md): **F1** preconditions / zero migration, **F2** observability, **F3** delivery; the parenthesised O1–O5 come from [08 §2.2](./08-state-observability-principle.md). `—` means it belongs to none of them (a product decision / single source / protocol compliance / dead code) |
| **Code anchor** | A function name (preferred: line numbers drift) or `file:line` |
| **UT** | In-process test (`go test`). Where marked "falsified", reverting the fix turns it red |
| **Live e2e** | An E2B test needing `FASTAGENT_E2B_LIVE=1 E2B_API_KEY=…`; the run command is in each file's header |
| **Deployed** | ❌ = working tree only (currently everything); it becomes ✅ when the change ships |

**Four-layer placement** (Clean Architecture): the F1 group sits in the **Use Case** (the reconcile policy) and
the **Frameworks** (store / sandbox); the F2 group spans **Entities** (the turn receipt) → **Use Cases** (signal
rendering) → **Interface Adapters** (tool results); and all seven rows of §4 land on the **Interface Adapters ↔
Use Cases port** — not a coincidence: G21/G17/G22 all failed at the same seam, the ownership of "which scope
holds this key".

**Coverage (checked file by file, 2026-09-18)**: this register covers **every** change in the working tree — all
81 entries of `git status --porcelain` (47 modified + 34 added/renamed/deleted) find a row: F1 in #1–#4, F2 in
#5–#18, F3 in #19–#20, path and scope in #21–#27 and #31, the rest in #28–#30. **No family is left out**; and the
reverse also holds — there is no row that exists only in this document and not in the code (every row carries a
function-name anchor).

## 1. F1 · Preconditions / zero migration (the incident's root cause and the reconcile)

| # | Change | Formal (duty) | Code anchor | UT | Live e2e | Deployed |
|---|--------|---------------|-------------|----|----------|----------|
| 1 | The sync's criterion changed from **"`Size == len(data)` ⇒ skip, else let the sandbox overwrite the store"** to **size+mtime → byte comparison → `BLOCKED` refuse-and-report on divergence** | F1 (R1/R2 zero migration) | `sandbox/lifecycle.go` `syncSnapshot` | `TestSyncContract_StoreEditIsNotOverwritten`, `_SurvivesPostExecTrigger`, `_NewPathIsFlushed`, `TestSyncVerdictDoesNotDependOnThePoolThatMakesIt` (**falsification: §10-1**) | `TestE2BLiveRepro` (**falsification: §10-2**) | ❌ |
| 2 | The **pod-local baseline** was deleted (an in-memory criterion cannot survive a lease hand-off) → memory-free reconcile | F1 (R3 convergence) | `sandbox/lifecycle.go` (baseline/digest tables removed), [10 §9](./10-harness-state-audit.md) | `TestSyncVerdictDoesNotDependOnThePoolThatMakesIt` (two worlds: one hydrated, one merely adopted), `TestEvictSignalOutlivesThePoolThatProducedIt` (**falsification: §10-3**) | `TestE2BLiveRepro` | ❌ |
| 3 | The **delivery stamp**: hydrate and the write-through both write the store's mtime onto the sandbox copy (the premise of the cheap criterion) | F1 (a recomputable criterion) | `sandbox/e2b_executor.go` hydrate, the STAMP step in `sandbox/lifecycle.go` `WriteThrough` | `TestWriteThroughStampsTheSandboxCopyWithTheStoreTime` (pins **the instant in the command**, not "some reads"; **falsification: §10-4**) | `TestE2BLiveHydrateKeepsStoreStamp` (**falsification: §10-5**) | ❌ |
| 4 | Disk layout agrees with hydrate: `/workspace/<path>` maps one-to-one onto the store key (a loose session drops the `sessions/<sid>/` prefix) | F1 (R4 locality) | `sandbox/e2b_executor.go` hydrate | — (only a live sandbox can witness it; **falsification: §10-6**) | `TestE2BLiveLooseScopeLayout` | ❌ |

## 2. F2 · Observability (who has to speak)

| # | Change | Formal (duty) | Code anchor | UT | Live e2e | Deployed |
|---|--------|---------------|-------------|----|----------|----------|
| 5 | Write-through generalised: all three write tools mirror on **every remote backend** (before: coding sessions only, and `apply_patch` never did) | F2 (C1 reach the channel the agent really reads) | `agent/tools/file.go` `writeThroughSignal`, `agent/tools/apply_patch.go` | `TestWriteFileSignalsReplacedVersion`, `TestWriteFileSignalsUnreachableSandbox` | `TestE2BLiveWriteThroughReachesSandbox` | ❌ |
| 6 | `CompareResult`'s **four states** replaced a single `Compared bool` (G5: a 5-byte file used to be announced as "over 2 MiB") | F2 (O1 say only true things) | `sandbox/lifecycle.go` `CompareResult`, `agent/tools/file.go` | `TestWriteFileSignalsUncheckedReplacement`, `TestWriteFileStaysQuietOnASharedBackend`, `TestSyncContract_WriteThroughWithoutExpectationClaimsNothing` | `TestE2BLiveWriteThroughReachesSandbox` | ❌ |
| 7 | **G6**: the pre-image travels with the call, so a write tool can say "replaced a different version (N bytes)" for the first time | F2 (O1) | `agent/tools/file.go` `previousStoreVersion`, `apply_patch.go` `plannedWrite.previous` | `TestWriteFilePassesThePreviousStoreVersionAsExpectation` | as above | ❌ |
| 8 | **G4/G7a**: `StoreOnlyLine` — the sync and `list_dir` share **one sentence** naming the paths the store has and the sandbox lacks, with the workaround, and **refusing to claim an origin** | F2 (O1) + F3 (O2) | `sandbox/lifecycle.go` (`storeOnly` + one extra List), `agent/tools/file.go` `storeOnlySignal` | `TestSyncReportsPathsTheStoreHasAndTheSandboxDoesNot`, `TestSyncDoesNotReportTheSkillsNamespace`, `TestListDirNamesPathsTheSandboxDoesNotHave`, `TestListDirStaysQuietWhenStoreAndSandboxAgree`, `TestListDirStaysQuietWhenTheSandboxCannotBeAsked` | `TestE2BLiveSandboxEditOfStorePathIsRefusedAndReported` (the refusal path is covered by the same run) | ❌ |
| 9 | **G19**: the unhydrated declaration rides the **instance** in `sandbox_leases.unhydrated` and is read back on adoption | F2 (O1) | `sandbox/lease.go`, `store/sandbox_leases.go`, `sandbox/e2b_executor.go` `publishUnhydrated` / `setWorkspaceUnhydrated`, `store/database.go` (the retrofit migration for existing databases) | `TestSandboxLeaseUnhydratedRidesTheInstance`, `TestE2BPoolAdoptionCarriesTheUnhydratedFact`, `TestE2BPoolPublishesTheUnhydratedFactOnCreate` | `TestE2BLiveUnhydratedFactSurvivesPodHandoff` | ❌ |
| 10 | **G8**: identity-file fingerprints enter the turn-level sample (which file changed, never its content) | F2 (O1) | `agent/env_changes.go` `identityFingerprints` | `TestEnvSignalCarriesIdentityFileChanges` | none (see §7) | ❌ |
| 11 | **G9 → G20**: the configuration baseline moved from process memory into the **turn receipt** (`run_receipt`), the `envTracker` table was deleted, and all three turn entry points sample and stamp | F2 (O1) + F3 (O4) | `agent/env_changes.go`, `session/manager.go` `SetRunReceipt`/`RunReceiptOf`, `session/store_adapter.go`, `agent/loop.go` | `TestEnvBaselineComesFromTheTurnReceipt`, `TestRebuiltAgentStatesTheChangeFromTheReceipt`, `TestReceiptCarriesEverySampledFamily`, `TestRunReceiptStampSurvivesAReload`, `TestEnvSignalStatesAConfigChangeThatOutlivedItsInstance` | none (see §7) | ❌ |
| 12 | **G10**: the cron list enters the sample (excluding `LastRun`/`NextRun` bookkeeping; an unreadable list says so) | F2 (O1) | `agent/env_changes.go` `cronFingerprint`, `cronLabels` | `TestEnvSignalCarriesScheduledJobChanges`, `TestCronFingerprintIgnoresRunBookkeeping`, `TestEnvSignalStatesUnreadableJobListWithoutClaimingDeletion` | none (see §7) | ❌ |
| 13 | An incomplete skill list is declared as incomplete (instead of letting the agent deny a capability it has) | F2 (O1) | `agent/skills.go` (`hydrateFailed`), `agent/env_changes.go` | `TestEnvSignalCarriesIncompleteSkillListOnFirstTurn`, `TestEnvSignalStopsCarryingIncompleteSkillsAfterRecovery` | none (see §7) | ❌ |
| 14 | A pruned tool result says **what happened** (not just "truncated") | F2 (O1) | `agent/compaction.go` | no UT (text only, see §7) | none | ❌ |
| 15 | **G11, stdio half**: MCP notifications have a receiving port (`NotificationSink`) → a 30s-per-server gate → reuse of `mcpConfigNotify` → the turn-level tool-set signal | F2 (O1) | `mcp/client.go`, `mcp/stdio.go`, `mcp/manager.go` | `TestStdioClientHandsNotificationsToTheHandler`, `TestManagerWiresNotificationsThroughTheGate`, `TestManagerDoesNotWireATransportWithoutNotifications` | none (the HTTP half is still open, see §7) | ❌ |
| 16 | **G12**: dropped automatic turns log full details per event, and a user-created cron gets a note in that session | F2 (O2) | `gateway/deferred_turns.go`, `gateway/gateway.go` (the note's exit, a bounded 250 ms send) | `TestDeferredTurnsAnnouncesADroppedScheduledTask`, `TestDeferredTurnsDropsMessagesPastBudget`, `TestDroppedCronNoteWithoutAJobName` | none (see §7) | ❌ |
| 17 | **G14**: heartbeat and the prompt read the **same** `HEARTBEAT.md` (single source) | — (single source) | `agent/heartbeat.go` `loadHeartbeatTasks` | `TestHeartbeatReadsWhatThePromptShows`, `TestHeartbeatFallsBackToTheDiskCopy`, `TestHeartbeatWithNoFileSendsNothing` | none (see §7) | ❌ |
| 18 | **One exit for the environment signal**: everything that says "your world changed" goes through **one exit** (instead of each subsystem inventing its own line) and renders as the **last section of the prompt** (every earlier section stays byte-identical, so prompt caching survives); tool names and memory join the same snapshot; the terminology is aligned `notice → signal` (σ) | F2 (C1 — into the channel that is actually read + C3 — silence when nothing changed) | `agent/env_changes.go` `signalEnvironmentChanges` / `renderEnvDelta`, `agent/context.go` `SetEnvironmentSignal`, `agent/tools/registry.go` `ToolNames` | `TestEnvSignalCarriesRemovals`, `TestEnvSignalCarriesAdditionsAndEdits`, `TestEnvSignalIsSilentWhenNothingChanged`, `TestEnvSignalIsSilentOnFirstObservation`, `TestEnvSignalIsPerSession`, `TestEnvSignalIgnoresContentPreservingMemoryRewrite`, `TestReceiptCarriesEverySampledFamily`; the renamed `TestWorkspaceSignalSurvivesTheLifecycleProxyAndTheMetaStrip` | none (see §7) | ❌ |

## 3. F3 · Delivery (how a signal actually arrives)

| # | Change | Formal (duty) | Code anchor | UT | Live e2e | Deployed |
|---|--------|---------------|-------------|----|----------|----------|
| 19 | **G3**: a signal produced by idle eviction moved from an **in-process queue** to a **durable carrier** (scope-keyed `configs_kv`); recomputable facts (refusals/failures) are not queued, only the one that cannot be recomputed (moved); the "your sandbox was replaced" note rides the call that discovered it | F3 (O4) + O2 | `gateway/sandbox_signals.go`, `gateway/userspace.go` / `gateway/reload.go` (the `SetSignalStore` wiring — both places that build a pool), `sandbox/lifecycle.go` `parkSignal` / `takeSignals` / `takeReplacedNote` | `TestEvictSignalOutlivesThePoolThatProducedIt`, `TestReplacedSandboxNoteRidesTheCallThatFoundIt` | `TestE2BLiveRepro` (the eviction path) | ❌ |
| 20 | "Where must a criterion live?" formalised as the **three placements** (recomputable / reuse an existing durable record / a new carrier); the environment baseline followed it into the turn receipt (same origin as #11) | F3 (O4's second form) | `sandbox/lifecycle.go` `liveInstance`, [08 §2.2.3](./08-state-observability-principle.md); code in #11 | see #11 | see #11 | ❌ |

## 4. F1-entity · Path and scope invariants (G18 / G21+d1 / G17-G+H+A / G22)

> These seven rows are one sentence, landed seven times: **"which scope holds this key" is a fact the party who
> knows it must state — nobody guesses at the arbitration point.**

| # | Change | Formal (duty) | Code anchor | UT | Live e2e | Deployed |
|---|--------|---------------|-------------|----|----------|----------|
| 21 | **G18 (01 §8)**: all 6 of `apply_patch`'s store touchpoints use the same resolution as `write_file`/`edit_file` (`scopeSessionID()` + `wsPath()`) | F1 (one path, one key) | `agent/tools/apply_patch.go` | `TestApplyPatchUsesTheSameStoreKeyAsWriteFile`, `TestApplyPatchDeleteUsesTheSameStoreKey`, `TestApplyPatchKeyInANonCodingSession`, `TestWriteThroughMirrorsOneKeyAndOnePath` | `TestE2BLiveOnePathIsOneKey` | ❌ |
| 22 | **G21, Fix 0**: the panel delete uses the **same path convention as download** (a path carrying its prefix is read as agent-relative; the older unprefixed shape keeps working) | F1 (one path, one key) | `setup/handlers_agents.go` `handleAgentFileDelete`, `sandbox/workspace_paths.go` `StorePathScope` | `TestHandleAgentFileDelete_UsesThePathThePanelClicked`, `_ProjectPathTargetsTheChatSandbox`, `_ScopeRelativePathStillWorks`, `TestStorePathScope` | `TestE2BLivePanelDeleteSticks` | ❌ |
| 23 | **d1**: the delete also writes through to the **live** sandbox, and **never creates one** (`LiveExecutorPool` looks at live instances only); a single-copy backend (docker) is a no-op | F1 (write-path symmetry) + not a delivery duty | `gateway/workspace_files.go`, `setup/handlers_agents.go` (the panel side's narrow-interface assertion), `sandbox/lifecycle.go` `RemoveLiveWorkspaceFile`, `sandbox/executor.go`, `sandbox/workspace_paths.go` | `TestRemoveWorkspaceFile_AsksTheExecutorsCapability`, `_NoPoolIsANoOp`, `_SingleCopyBackendIsANoOp`, `TestSandboxPathForStorePath`, `TestE2BPoolLiveExecutorDoesNotCreate`, `TestDockerPoolLiveExecutorDoesNotCreate` | `TestE2BLivePanelDeleteSticks` | ❌ |
| 24 | **G17-G**: the preview container is addressed by PROJECT (one preview container per project, both entry points on it) | — (scope invariant) | `runtime/runtime.go` `previewSandboxSession` | `TestPreviewSandboxSession` | covered by #25's live run | ❌ |
| 25 | **G17-H**: writes and deletes are **broadcast to every live container of the project** (per-chat shells kept) | — (scope invariant) | `sandbox/lifecycle.go` `mirrorToProjectPeers` + the delete fan-out, `sandbox/executor.go` `LiveProjectExecutors` | `TestWriteThroughReachesEveryContainerOfTheProject`, `TestWriteThroughCountsAContainerItCouldNotReach`, `TestRemoveLiveWorkspaceFileReachesEveryContainerOfTheProject` | `TestE2BLiveProjectWriteReachesSiblingContainer` | ❌ |
| 26 | **G17-A**: the sync's write-back is collapsed to the **project root** (`syncStoreScope`) — no more `<pid>/<chat>/…` duplicates, and `exec` artefacts are immediately visible to the tools | — (scope invariant) | `sandbox/lifecycle.go` `syncStoreScope` | `TestSyncWritesBackToTheProjectRootNotTheChatSubdir`, `TestSyncScopeEqualsHydrateScopeForProjects` | `TestE2BLiveProjectSessionKeepsOneTree` | ❌ |
| 27 | **G22**: the writer hands the store scope down (`sandbox.StoreScope`); the stamp no longer infers it from the container | F1 (one path, one key) | `sandbox/executor.go` `StoreScope`, `sandbox/lifecycle.go`, `agent/tools/file.go` | `TestWriteThroughStampsWithTheStoreScopeItWasGiven` (**falsification: §10-7**), `TestWriteThroughStampsTheSandboxCopyWithTheStoreTime` | `TestE2BLiveSyncReadsNoBodiesForStampablePaths` (**measured**: whole-object reads for that path **1 → 0**; but see §10-8 — that count is a **measurement, not a falsification**: the ±1s tolerance hides a missing stamp) | ❌ |
| 31 | **G23**: "a project session ⇒ keys land at the project root" and the **layout table** are each one pure function now (`workspace.ScopeSegments` / `workspace.WriteScope`) — the file tools, the sandbox's write-back, the panel's parser, LocalFS and S3 all call them; `Registry.codingRootScope` / `SetCodingRootScope` are deleted and `sandbox.StoreScope` became an **alias** of `workspace.Scope` | — (scope invariant, fourth/fifth instance of the family; F1's "one path, one key") | `workspace/scope.go` (new), `workspace/localfs.go`, `workspace/s3.go`, `agent/tools/registry.go`, `agent/loop.go`, `sandbox/lifecycle.go` `syncStoreScope`, `sandbox/executor.go` `StoreScope` | `TestScopeSegmentsIsTheLayoutTable`, `TestWriteScopeCollapsesInsideAProject`, `TestAWriterScopeIsTheScopeItsKeysLandIn`, `TestLayoutWriteScopeAndParserAgree`, `TestProjectWritersAndTheSyncShareOneScope`, `TestAProjectChatSubdirKeyIsItsOwnPath`, `TestScopeSessionIDCollapsesInsideAProject` (**falsified**: point `scopeSessionID()` back at `r.sessionID`, or `syncStoreScope` back at "no collapse" ⇒ two/three tests go red, measured) | the scope logic changed ⇒ the live suite was re-run (`TestE2BLiveProjectSessionKeepsOneTree`, `TestE2BLiveOnePathIsOneKey`, `TestE2BLiveProjectWriteReachesSiblingContainer`, `TestE2BLivePanelDeleteSticks` all sit on this path) | ❌ |

## 5. Changes that are not F1–F3

| # | Change | Kind | Code anchor | UT | e2e | Deployed |
|---|--------|------|-------------|----|-----|----------|
| 28 | **G16**: a skill secret was overwritten by its own **mask** — the rule now has one home (`mergeSkillEntry` + `mergeSkillEntries`), shared by both write paths, with `cloneSkillEntries` fixing the JSON decoder **reusing** maps | a setup-API defect | `setup/handlers.go` | `TestMaskedGlobalSkillSecretKeepsTheStoredValue`, `TestMaskedAgentSkillSecretKeepsTheStoredValue`, `TestMergeSkillEntriesSemantics`, `TestMaskedValueIsWhatThePanelSends` | none (HTTP layer, see §7) | ❌ |
| 29 | **Dead code and legacy plumbing**: `WorkspaceSync` (121 lines, never wired), `makeExecTool`, the whole `BoxliteClientID` chain (config/env/admin/web/pool/executor), `generateRandomToken`, `filterAccounts`, `defaultIfEmpty`, `delta.deleted`, `Registry.sandboxSessionID` | dead code (CCP: no reason to change ⇒ no reason to exist) | the table in [10 §9](./10-harness-state-audit.md) | no UT (the deletion is the change); verification = repo-wide reference counts + `go build`/`vet` | none | ❌ |
| 30 | **Forensics and cleanup tooling**: `scripts/workspace_revert_audit.py` (the incident audit, offline TSV inputs), `scripts/workspace_project_chat_duplicate_cleanup.py` (the duplicates A left behind; `--selftest`; it only lists a copy for deletion when the same bytes survive at the project root) | operations tooling | `scripts/` | the script's own `--selftest` (six shapes) | none (offline; run it on production in the order of §8) | ❌ |

## 6. Verification commands

```bash
# everything (after any change)
go build ./... && go vet ./internal/... && go test ./internal/... -count=1

# the live suite (E2B, ~2 minutes)
FASTAGENT_E2B_LIVE=1 E2B_API_KEY=… go test ./internal/sandbox/ ./internal/agent/tools/ -run 'TestE2BLive' -count=1

# by group (the numbers refer to the rows above)
go test ./internal/sandbox/ -run 'TestSyncContract' -count=1                     # #1 #2 #6
go test ./internal/sandbox/ -run 'Test(Evict|Replaced|SyncReports)' -count=1     # #8 #19
go test ./internal/store/   -run TestSandboxLeaseUnhydrated -count=1             # #9
go test ./internal/agent/   -run 'TestEnvSignal|TestEnvBaseline|TestReceipt|TestHeartbeat' -count=1  # #10–#17
go test ./internal/mcp/     -run 'TestStdioClient|TestManager' -count=1          # #15
go test ./internal/gateway/ -run 'TestDeferredTurns|TestDroppedCronNote|TestRemoveWorkspaceFile' -count=1  # #16 #23
go test ./internal/setup/   -run 'TestHandleAgentFileDelete|TestMasked|TestMergeSkillEntries' -count=1      # #22 #28
go test ./internal/sandbox/ -run 'TestWriteThrough|TestRemoveLiveWorkspaceFileReaches|TestSyncWritesBack|TestSyncScope|TestSandboxPath|TestStorePathScope|TestE2BPoolLiveExecutor|TestDockerPoolLiveExecutor' -count=1   # #5 #18 #25 #26 #27
go test ./internal/sandbox/ -run 'TestSyncContract|TestSyncVerdict|TestWriteThroughStamps' -count=1   # #1 #2 #3 (the §10 falsification targets)
go test ./internal/runtime/ -run TestPreviewSandboxSession -count=1               # #24
go test ./internal/session/ -run TestRunReceipt -count=1                          # #11
go test ./internal/sandbox/ -run 'TestLayoutWriteScopeAndParserAgree|TestProjectWritersAndTheSyncShareOneScope|TestAProjectChatSubdirKeyIsItsOwnPath' -count=1   # #31
go test ./internal/workspace/ -run 'TestScopeSegments|TestWriteScope|TestAWriterScope' -count=1  # #31
```

## 7. Rows with no live e2e, and why

* **Turn-level environment signals (#10–#14, #17)**: the mechanism is entirely in-process plus the DB and never
  touches a sandbox. Their e2e equivalent is the **real-sqlite round trip** (#11's
  `TestRunReceiptStampSurvivesAReload`) plus per-line rendering assertions; running it on E2B would add sandbox
  cost and prove nothing new.
* **The compaction placeholder (#14)**: text only; pinned by the principle review in 08 and by reading the code.
* **The skill mask (#28)**: an HTTP-layer defect with no sandbox semantics.
* **Dead-code removal (#29)**: the deletion is its own verification (reference counts + compilation).
* **Still open, hence no e2e**: the **HTTP half of G11** (the transport has no channel at all → SSE or a periodic
  re-list) and **G15** (`notifications/initialized`, which needs a live check that the handshake is unaffected).
  Both belong to the MCP family and are out of this round's scope by decision.

## 8. Suggested shipping order (by risk)

1. **#1–#4**: the incident's root cause. It **still reproduces** in production (HEAD skips on equal size and
  otherwise lets the sandbox overwrite the store).
2. **#21–#27**: the path/scope family. **#22** (the panel delete is a silent no-op) and **#26** (duplicates are
   still being produced) are user-visible problems happening in production right now.
   **#31** closes that family out (the sixth instance of "one home for the rule"). It ships with that group and
   carries no risk of its own, but it **must ship together with it**: its only behavioural delta exists in a
   deployment that has projects and no runtime manager — where projects cannot be created either.
3. **#5–#20**: signals and delivery. They do not fix data, but they decide whether the agent can notice the next
   problem by itself.
4. **#30's cleanup script**: run it only **after #26 ships**, or the old behaviour keeps recreating the copies
   (the script's docstring says so at the top).

## 9. Found in review → landed the same day

The previous review found two more duplicated expressions at the seam in §4 ([**G23** in 10 §4](./10-harness-state-audit.md)):
"a project session ⇒ keys land at the project root" was written as two predicates in three places (the agent
side's `scopeSessionID()` via `codingRootScope`, the sandbox side's `syncStoreScope()` via `projectID != ""`, the
panel side's `StorePathScope()` via the prefix test). Following that thread turned up a **fifth**: the layout
table (`pid/sid` → directory) was written out again in `LocalFS.scopeDir` and in `S3.key` / `S3.scopePrefix`.

Both rules now live in [`internal/workspace/scope.go`](../../internal/workspace/scope.go):
`ScopeSegments` (the layout) and `WriteScope` (the writers' collapse). That is row **#31** above; the
falsifications and measurements are in #31 and in 10 §4's G23 row.

## 10. Falsification log (2026-09-18, each one run for real)

Each row is a **controlled falsification**: put one change back the way it was before the repair (or into a
plainly wrong shape), run the named test, record that it goes red. Everything was reverted afterwards — the
tree carries no `FALSIFY` markers (`rg FALSIFY internal/` finds nothing).

| # | Target | What was reverted | Which test went red | The output observed |
|---|--------|-------------------|--------------------|---------------------|
| 10-1 | #1 (offline) | `syncSnapshot`'s criterion, back to HEAD's **"same size ⇒ skip, otherwise overwrite the store"** | `TestSyncContract_StoreEditIsNotOverwritten`, `_StoreEditSurvivesPostExecTrigger`, `_SandboxEditOfStorePathIsRefused` | `the sandbox copy overwrote the host's version — store: "OLD SNAPSHOT VERSION"; want: "THE HOST WROTE THIS LONGER VERSION"` (the incident reproduced **offline** for the first time) |
| 10-2 | #1 (live) | Same (criterion back to HEAD) | `TestE2BLiveRepro` | `the store lost the host's version — the incident is back`; `store after sync: "OLD SNAPSHOT VERSION (1111111111111111)"` and an empty sync log (the old rule overwrites silently) |
| 10-3 | #2 | The **pod-local baseline** put back in its minimal form (remember at hydrate what the sandbox was handed; decide from that) | `TestSyncVerdictDoesNotDependOnThePoolThatMakesIt` | ① the incident: `{[] [] [] }` (the pod WITH memory **says nothing at all**) vs `{[] [report.html] [] }` (the adopting pod reports BLOCKED); ③ the sandbox edit: `{[report.html] [] [] }` (**it writes the unattributed sandbox edit straight back to the store**) vs `{[] [report.html] [] }`. One state, two pods, two verdicts — which is the reason it was deleted |
| 10-4 | #3 (offline) | The write-through stamp switched to **the mirror's own clock** (`time.Now()`) instead of the store object's mtime | `TestWriteThroughStampsTheSandboxCopyWithTheStoreTime` | `want a command: touch -d @1700000001 '/workspace/app/notes.md'` |
| 10-5 | #3 (live) | Hydrate given **the zero time** instead of `obj.ModTime` (the sandbox copy no longer carries the store's stamp) | `TestE2BLiveHydrateKeepsStoreStamp` | `sandbox file mtime 1789734932 is +8s from the store's LastModified 1789734924` |
| 10-6 | #4 (live) | Hydrate materialising the **scope prefix** inside `/workspace` (`/workspace/sessions/<sid>/<path>` — the shape the mirror's old hardcoded path would produce) | `TestE2BLiveLooseScopeLayout` | `a loose scope's file is not at /workspace/<path> (got "no…")` + `the sandbox has a /workspace/sessions tree`; in the same run the signal layer correctly reported `[workspace] 1 path(s) are in the workspace store but NOT in this sandbox …` |
| 10-7 | #27 | The stamp's `Stat` back on the **sandbox scope** (G22's original defect) | `TestWriteThroughStampsWithTheStoreScopeItWasGiven` | `primary container was not stamped — the store scope the caller stated was ignored: []` |
| 10-8 | #27 (a boundary) | The write-through stamp **removed** (the whole `touch` made a no-op) | `TestE2BLiveSyncReadsNoBodiesForStampablePaths` **did NOT go red** | `one sync in a project session: 0 whole-object reads, 2 stats` — the criterion's ±1s tolerance (`sameVersion`) absorbs "the store write and the mirror write landed in the same second", so **that count is a measurement, not a falsification**. What does pin the stamp is 10-4 (the command) and 10-5 (the hydrate stamp) |

**A fixture finding (also measured)**: `syncFixture` used to create the divergence and *then* hydrate — and a hydrate
copies the store back into the sandbox, so the divergence was erased before the reconcile ever saw it. That is why
the three offline contract tests above passed **even against HEAD's old criterion** (the first 10-1 run was green).
The fixture now hands both sides the same bytes, hydrates, and only then applies the divergence — which is what
makes 10-1 a real falsification.

### 10.1 The repo-wide sweep: any other "empty fixtures"?

The criterion is empirical, not a code reading: **put #1's criterion back to HEAD and run the whole
`./internal/sandbox/` and `./internal/agent/tools/` packages**, then see which tests go red. Two measured runs:

| Shape | Red tests under HEAD's criterion |
|-------|----------------------------------|
| **Old fixture** (divergence first, hydrate after) | **3**: `TestExecObservesRefusedPaths`, `TestBlockedPathsAreReDerivedRatherThanCarried`, and this register's new `TestSyncVerdictDoesNotDependOnThePoolThatMakesIt` — while the three `TestSyncContract_*` incident assertions were **green**, i.e. the incident could not turn CI red at all |
| **New fixture** (hand-off → hydrate → divergence) | **6**: the three above plus `TestSyncContract_StoreEditIsNotOverwritten`, `_StoreEditSurvivesPostExecTrigger`, `_SandboxEditOfStorePathIsRefused` |

Every other test that can hydrate was read and is **not** this class, for these specific reasons:

* `sync_project_scope_test.go` calls the **inner** `pool.Get` directly and never hydrates; it also uses a real
  `LocalFS`, because the fake store's `scopeForKey` collapses `(pid, sid)` to `p:<pid>` — itself the blind spot for
  "project root vs project chat".
* `broadcastFixture`: the hydrate writes the *pre-write* content of the same path and the write-through overwrites
  it right after; the assertions look at the final content and the stamp.
* The live tests: either the store is seeded **before** the pool exists (`liveE2B` and the `e2b_live_*` files) or the
  change happens **after birth** (`e2b_live_project_broadcast_test`, `e2b_live_stamp_cost_test`).
* `internal/agent/tools/*`: a bare executor (no lifecycle pool ⇒ no hydrate).
* `lifecycle_test.go`'s hydrate family: those tests *are* about hydration, so seeding the store first is correct.

**Two known fixture blind spots** (not defects, but they decide where a test can live): the fake store has **no
mtime** (so timestamp claims must pin a command or go live — see 10-4/10-5), and its `scopeForKey` **collapses
(pid, sid)** (so "project root vs project chat" needs a real `LocalFS` or a live sandbox — see
`sync_project_scope_test.go` and 10-7).

## 11. Shipping groups (proposal, for review)

> Status: **a proposal**. This step only groups; **nothing has been committed**. All 81 changed files are still in
> the working tree and production still runs `HEAD` (`16a7532`).

### 11.1 Six commits

> Two were added on 2026-09-18: **T0** (gate the live-network tests, so CI can be trusted first) and **T5** (bring
> the formalisation docs into the repo — §11.6). Neither depends on T1–T4 in code; put T0 first and T5 last.

| Commit | Contents | Formal | Register rows | Files | Can it ship alone? |
|--------|----------|--------|---------------|-------|--------------------|
| **T0** | Test hygiene: gate the live-network tests | — (test hygiene) | the external dependency in §11.5 | 6 files (one `live_net_test.go` helper per package + the 4 test files that call the gate) | ✅ no product code |
| **T1** | Sandbox reconcile and scope | F1 + the scope invariant | #1–#4, #21–#27, #31 | 25 whole files + the T1 parts of 10 split files | ✅ it is §8's steps 1–2 (root cause + path/scope) — the incident fix itself |
| **T2** | Signals and delivery | F2 + F3 | #5–#20 | 36 whole files + the T2 parts of 10 split files | ✅ but it **must follow T1**: several of its σ describe behaviour T1 introduces |
| **T3** | Dead code and config cleanup | — (CCP) | #29 | 7 whole files + the T3 parts | ✅ pure deletion, shippable any time |
| **T4** | Scripts and in-repo documentation | — (ops) | #30 | 3 whole files | ✅ offline tooling, no runtime effect |
| **T5** | Formalisation docs into the repo | — (docs, GEB isomorphism) | §11.6 | 26 doc files + the 13 comments/scripts that named the old path | ✅ docs only |

**Why T1 and T2 are two commits**: T1 is "the data stops being overwritten"; T2 is "next time the agent can see it
for itself". They can ship separately (§8's steps 1 and 3), but the **order cannot be reversed** — shipping T2
first would describe a sync that still overwrites.

### 11.2 Whole-file assignment

**T1 (25)**

* `internal/workspace/{scope.go, scope_test.go, localfs.go, s3.go}`
* `internal/sandbox/{workspace_paths.go, workspace_paths_test.go, executor.go, docker_executor.go, lifecycle_sync_contract_test.go, sync_project_scope_test.go, project_broadcast_test.go, live_executor_test.go, e2b_live_repro_test.go, e2b_live_panel_delete_test.go, e2b_live_project_broadcast_test.go, e2b_live_stamp_cost_test.go}`
* `internal/runtime/{runtime.go, preview_scope_test.go}`
* `internal/gateway/{workspace_files.go, workspace_files_test.go}`
* `internal/setup/handlers_agent_file_delete_panel_test.go`
* `internal/agent/tools/{apply_patch_path_scope_test.go, apply_patch_live_scope_e2e_test.go, coding_scope_test.go, apply_patch_test.go}`

**T2 (36)**

* `internal/agent/{env_changes.go, env_changes_test.go, heartbeat_source_test.go, receipt_baseline_test.go, context.go, heartbeat.go, skills.go, compaction.go, workspace_signal_e2e_test.go (renamed from workspace_notice_e2e_test.go)}`
* `internal/session/{manager.go, store_adapter.go, run_receipt_test.go}`
* `internal/store/{database.go, sandbox_leases.go, sandbox_leases_unhydrated_test.go}`
* `internal/sandbox/{lease.go, lease_pool_test.go, signal_carrier_test.go, exec_change_signal_test.go, e2b_live_unhydrated_handoff_test.go}`
* `internal/gateway/{sandbox_signals.go, deferred_turns.go, deferred_turns_test.go, reload.go, gateway.go, sandbox_pool_lease_test.go}`
* `internal/mcp/{client.go, manager.go, stdio.go, notification_test.go}`
* `internal/setup/{handlers.go, skill_entry_mask_test.go}`
* `internal/agent/tools/{write_through_signal_test.go, store_only_signal_test.go, workspace_signal_test.go (renamed from workspace_notice_test.go), bash_session_test.go}`

**T3 (7)**: `internal/sandbox/workspace_sync.go` (deleted), `internal/agent/tools/exec.go`,
`internal/config/{config.go, env.go}`, `internal/setup/{handlers_admin.go, handlers_agent_channels.go}`, `web/src/lib/api.ts`

**T4 (3)**: `scripts/{workspace_revert_audit.py, workspace_project_chat_duplicate_cleanup.py}`, `docs/sandbox-scope-leak.md`

### 11.3 Cross-group files (10, needing a hunk-level split)

| File | T1 part | T2 part | T3 part |
|------|---------|---------|---------|
| `internal/sandbox/lifecycle.go` | `syncSnapshot`, `statsFor`, `sameVersion`, `equalToStore`, `delta.changed`, `syncStoreScope`, `WriteThrough`, `mirrorToProjectPeers`, `lazyExecutor.WriteThroughScope`, `liveInstance`, `RemoveLiveWorkspaceFile` | `SetSignalStore`, `parkSignal`, `takeSignals`, `signalsFor`, `StoreOnlyLine`, `movedLine`, `takeReplacedNote`, the unhydrated part of `getInner`, the rendering part of `lazyExecutor.execOnce` | — |
| `internal/sandbox/e2b_executor.go` | `LiveExecutor`, `LiveProjectExecutors`, struct fields | `publishUnhydrated`, `adoptFromLease`, `reconcileLocalLease` | — |
| `internal/sandbox/lifecycle_test.go` | the `fakeExecutor.commands` recorder (used by §10's 10-4) | `snapshottingExecutor`'s single-copy note, `fakeLeaseStore`'s unhydrated field | — |
| `internal/agent/tools/file.go` | the five `StoreScope` pass-throughs | `writeThroughSignal`, `storeOnlySignal`, the `list_dir` rendering | — |
| `internal/agent/tools/apply_patch.go` | the key resolution at its six store touchpoints | the sandbox-mode mirror + signal | — |
| `internal/agent/tools/registry.go` | `scopeSessionID`, removal of `codingRootScope` | `ToolNames`, `declareUnhydratedWorkspace` / `withWorkspaceSignal` | the `sandboxSessionID` field (dead code) |
| `internal/agent/loop.go` | the collapse wiring in `bindSession` | sampling at the three turn entry points + the unhydrated part of `refreshSkills` | — |
| `internal/gateway/userspace.go` | — | the `SetSignalStore` wiring (both places that build a pool) | removal of the `BoxliteClientID` wiring |
| `internal/setup/handlers_agents.go` | `handleAgentFileDelete` + the narrow interface | — | the onboard boxlite fields |
| `internal/sandbox/boxlite_executor.go` | the `LiveExecutorPool` implementation (no-op semantics) | — | the whole `clientID` chain |

### 11.4 Two ways to do it — pick one

| Way | Commits | Cost |
|-----|---------|------|
| **A (recommended)** | 4 (as above) | 10 files need a hunk-level split; every commit must **compile and pass**; T1's commit cannot contain T2's tests (they reference T2 symbols) |
| **B (less work)** | 3: T1+T2 merged into one "the repair", then T3 and T4 | no hunk splitting; the cost is a 60+ file commit — a bigger review unit and a coarser rollback (you cannot roll back "just the signals") |

### 11.5 The bar for each commit

| Commit | Bar |
|--------|-----|
| T1 | `go build ./... && go vet ./internal/...` + `go test ./internal/{sandbox,agent/tools,workspace,runtime,setup}/ -count=1` + live `-run 'TestE2BLive(Repro\|PanelDeleteSticks\|ProjectWriteReachesSiblingContainer\|OnePathIsOneKey\|ProjectSessionKeepsOneTree\|SyncReadsNoBodiesForStampablePaths)'` |
| T2 | the full offline suite `go test ./internal/... -count=1` + the full live suite `-run TestE2BLive` |
| T3 | the full offline suite (deletions: reference count + compile + tests) |
| T4 | `python3 scripts/workspace_project_chat_duplicate_cleanup.py --selftest` |

**An external dependency (handled in T0)**: the offline suite used to contain **two** ungated network tests, both
making "green" unreliable:

| Package | Test | Before | Now |
|---------|------|--------|-----|
| `internal/skills` | `TestInstallFromSkillsSh_RepoField` (downloads a real tarball) + 4 `TestDiagnose_*` diagnostics | 156s, 1 failure (`probe HTTP 404`) | **0.55s green** (gated) |
| `internal/setup` | `TestRunInstall_GitHub_ReturnsRepoInResult` (really installs a skill) | 122s, intermittent failure (`runInstall github: …404`) | **7.8s green** (gated) |

**How**: each package has a `requireLiveNet(t)` helper (`internal/skills/live_net_test.go`,
`internal/setup/live_net_test.go`) demanding an explicit `FASTAGENT_NET_LIVE=1` — the same convention as the live E2B
suite's `FASTAGENT_E2B_LIVE=1`, with the variable named in the skip message. **Measured**: with
`FASTAGENT_NET_LIVE=1` both install tests still reproduce the upstream 404 — the failure is now **explicit** (run it
and you see it) instead of hidden.

### 11.6 The formalisation docs are now in the repo (done 2026-09-18)

**The problem**: this register and 00–10 (both languages) used to live in
`/Users/reina/Project/tokenaissance/docs/fastagent/`, and that directory **belongs to no git repository** (the
workspace root holds dozens of independent repos) — so the documents had no version history at all. That is both why
this session's earlier "deliverable rolled back" incidents could only be detected by counting bytes, and why "one
commit for code plus its doc update" (GEB isomorphism) was physically impossible.

**Done**: the whole set now lives in this repository, the two languages still siblings:

* `docs/文件系统形式化证明/` (Chinese, the origin)
* `docs/fs-formal-proof/` (the 1:1 English edition)

The move changed link **depth**, because the new location is two levels below the repo root: links into code went
from `../../../fastagent/...` to `../../internal/...` (**126** of them), and links to other in-repo documents from
`../../../fastagent/docs/X` to `../X`. Links between the two language sets (`../fs-formal-proof/...`) did not
change — they were siblings already.

**Checked**: after the move, a link check over `docs/文件系统形式化证明/*.md` and `docs/fs-formal-proof/*.md`
finds **0 dead relative links**; the 13 code comments/scripts that named the old path were updated in the same pass
(`rg 'docs/fastagent/文件系统形式化证明'` finds nothing).

**So from this commit on the rule is**: a code change commit must carry its matching documentation change (or the
other way round) — otherwise it is not finished. Verify each commit against the bars in §11.5.
