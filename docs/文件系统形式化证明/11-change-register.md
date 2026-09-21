# 11 · 改动点总册：形式系统 → 代码 → UT → 真机 e2e

> 状态：登记册（2026-09-18）· 回答一个问题：**基于文件形式化（F1/F2/F3）与状态可观测性原则，
> 我们改了哪些点，每个点的 UT 与真机 e2e 在哪。**
> **部署状态（2026-09-19 更正）**：本册 #1–#31（T1–T6）**已提交**在分支 `fastagent`（HEAD `8984c99`，
> `a0080a5` 是锚点），**dev 已部署** `8984c99`；**production 仍是 `a24c0a8`（落后 34 个提交），
> 所以对 production 而言"上线"列依然全是 ❌**。原文"只在工作区（未提交）"写于 09-18，已过期；
> 两个远端：`fastagent`（私有，已含 `8984c99`）与 `origin` = `tokenaissance/fastclaw`
> （**公开镜像，落后 34 个提交，按决定暂不推送**）。
> 对照表与理由见 [10 的"部署状态"横幅](./10-harness-state-audit.md)。
> 与其它文档的分工：**10 §4** 管"缺口的状态"，**05** 管"方案与决策记录"，**本文** 管"改动点 ↔ 证据"。

## 0. 怎么读

| 列 | 含义 |
|----|------|
| **形式（义务）** | [00](./00-formal-systems.md) 的三套形式系统：**F1** 前置条件/零迁移、**F2** 可观测性、**F3** 投递；括号里是 [08 §2.2](./08-state-observability-principle.md) 的义务 O1–O5。`—` 表示不属于这三套（产品决策 / 单一来源 / 协议合规 / 死码） |
| **代码锚点** | 函数名或 `文件:行`；行号会漂，函数名不会，所以以函数名为准 |
| **UT** | 进程内测试（`go test`）。标「反证」的，表示把修复改回去该测试会红 |
| **真机 e2e** | 需要 `FASTAGENT_E2B_LIVE=1 E2B_API_KEY=…` 的 E2B 测试；运行命令写在各自文件头部 |
| **上线** | ❌ = 只在工作区（当前全部如此）；部署之后才变 ✅ |

> **本册不收录「控制动作形态」。** 形态是**缺口**的属性（不提供／提供／顺序／时长），
> 逐格判定与可核查读数在 [10 §4 的表头](./10-harness-state-audit.md)；本册只做「改动点 ↔ 证据」。
> **同一条事实不写两份表达式**——要按形态查，去 10 §4；要按改动查，留在这里。
> （理由：改动与缺口不是一对一——#23 一次关掉两格，新改动可能还不属于任何既有缺口。
> 硬塞一列，那一格迟早会说谎。）

**四层归属**（Clean Architecture）：F1 组落在 **Use Case（对账策略）+ Frameworks（store / sandbox）**；
F2 组横跨 **Entities（回合收据）→ Use Cases（信号渲染）→ Interface Adapters（工具结果）**；
第 4 组的七条**全部**落在 **Interface Adapters ↔ Use Cases 的端口**上——那不是巧合，G21/G17/G22
三处缺陷的共同位置就是这条缝（谁拥有"这个键在哪个作用域"这个事实）。

**覆盖范围（2026-09-18 逐文件核对）**：本册对应工作区的**全部**改动 —— `git status --porcelain`
里的 81 个文件（47 改 + 34 增/改名/删）每一族都能找到一行：F1 组 #1–#4、F2 组 #5–#18、
F3 组 #19–#20、路径与作用域 #21–#27 与 #31、其余 #28–#30。**没有一族落在表外**；反向也成立 ——
表里没有"只写在文档里、代码里并不存在"的行（每条都给了函数名锚点）。

## 1. F1 · 前置条件 / 零迁移（事故根因与对账）

| # | 改动点 | 形式（义务） | 代码锚点 | UT | 真机 e2e | 上线 |
|---|--------|-------------|---------|----|---------|------|
| 1 | 同步判据从 **`Size == len(data)` 就跳过**（否则沙箱覆盖 store）换成 **size+mtime → 字节比较 → 分歧则 `BLOCKED` 拒绝并报告** | F1（R1/R2 零迁移） | `sandbox/lifecycle.go` `syncSnapshot` | `TestSyncContract_StoreEditIsNotOverwritten`、`_SurvivesPostExecTrigger`、`_NewPathIsFlushed`、`TestSyncVerdictDoesNotDependOnThePoolThatMakesIt`（**反证见 §10-1**） | `TestE2BLiveRepro`（**反证见 §10-2**） | ❌ |
| 2 | 删掉 **pod-local 基线**（内存判据跨副本失效）→ 无记忆对账 | F1（R3 收敛） | `sandbox/lifecycle.go`（baseline/digest 表删除）、[10 §9](./10-harness-state-audit.md) | `TestSyncVerdictDoesNotDependOnThePoolThatMakesIt`（两个世界：一个 hydrate 过、一个只是接管）、`TestEvictSignalOutlivesThePoolThatProducedIt`（**反证见 §10-3**） | `TestE2BLiveRepro` | ❌ |
| 3 | **交付戳**：hydrate 与穿透都把 store 的 mtime 写到沙箱副本上（廉价判据的前提） | F1（判据可重算） | `sandbox/e2b_executor.go` hydrate、`sandbox/lifecycle.go` `WriteThrough` 的 STAMP 段 | `TestWriteThroughStampsTheSandboxCopyWithTheStoreTime`（钉住**命令里那个时刻**，不是"若干读"；**反证见 §10-4**） | `TestE2BLiveHydrateKeepsStoreStamp`（**反证见 §10-5**） | ❌ |
| 4 | 磁盘布局与 hydrate 的一致性：`/workspace/<path>` 与 store 键一一对应（宽松会话不带 `sessions/<sid>/` 前缀） | F1（R4 局部性） | `sandbox/e2b_executor.go` hydrate | —（只有真机能证；**反证见 §10-6**） | `TestE2BLiveLooseScopeLayout` | ❌ |

## 2. F2 · 可观测性（谁必须说话）

| # | 改动点 | 形式（义务） | 代码锚点 | UT | 真机 e2e | 上线 |
|---|--------|-------------|---------|----|---------|------|
| 5 | 写入穿透泛化：三个写工具在**所有远程后端**都镜像（此前只在 coding 会话、且 `apply_patch` 不镜像） | F2（C1 进真正读的通道） | `agent/tools/file.go` `writeThroughSignal`、`agent/tools/apply_patch.go` | `TestWriteFileSignalsReplacedVersion`、`TestWriteFileSignalsUnreachableSandbox` | `TestE2BLiveWriteThroughReachesSandbox` | ❌ |
| 6 | `CompareResult` **四态**替代单个 `Compared bool`（G5：5 字节文件曾被报"超过 2 MiB"） | F2（O1 说真话） | `sandbox/lifecycle.go` `CompareResult`、`agent/tools/file.go` | `TestWriteFileSignalsUncheckedReplacement`、`TestWriteFileStaysQuietOnASharedBackend`、`TestSyncContract_WriteThroughWithoutExpectationClaimsNothing` | `TestE2BLiveWriteThroughReachesSandbox` | ❌ |
| 7 | **G6**：pre-image 随调用传下去，写工具因此第一次能说"替换了不同版本（N 字节）" | F2（O1） | `agent/tools/file.go` `previousStoreVersion`、`apply_patch.go` `plannedWrite.previous` | `TestWriteFilePassesThePreviousStoreVersionAsExpectation` | 同上 | ❌ |
| 8 | **G4/G7a**：`StoreOnlyLine` —— 同步与 `list_dir` 共用**一句话**点名"store 有、沙箱没有"，并给出补救；**拒绝声称归属** | F2（O1）+ F3（O2） | `sandbox/lifecycle.go`（`storeOnly` + 额外一次 List）、`agent/tools/file.go` `storeOnlySignal` | `TestSyncReportsPathsTheStoreHasAndTheSandboxDoesNot`、`TestSyncDoesNotReportTheSkillsNamespace`、`TestListDirNamesPathsTheSandboxDoesNotHave`、`TestListDirStaysQuietWhenStoreAndSandboxAgree`、`TestListDirStaysQuietWhenTheSandboxCannotBeAsked` | `TestE2BLiveSandboxEditOfStorePathIsRefusedAndReported`（拒绝路径同期覆盖） | ❌ |
| 9 | **G19**：未水合声明随**实例**持久化到 `sandbox_leases.unhydrated`，采纳时读回 | F2（O1） | `sandbox/lease.go`、`store/sandbox_leases.go`、`sandbox/e2b_executor.go` `publishUnhydrated` / `setWorkspaceUnhydrated`、`store/database.go`（老库 retrofit 迁移） | `TestSandboxLeaseUnhydratedRidesTheInstance`、`TestE2BPoolAdoptionCarriesTheUnhydratedFact`、`TestE2BPoolPublishesTheUnhydratedFactOnCreate` | `TestE2BLiveUnhydratedFactSurvivesPodHandoff` | ❌ |
| 10 | **G8**：身份文件指纹进回合采样（只说改了哪个文件，不泄内容） | F2（O1） | `agent/env_changes.go` `identityFingerprints` | `TestEnvSignalCarriesIdentityFileChanges` | 无（见 §7） | ❌ |
| 11 | **G9 → G20**：配置指纹的基线从进程内搬进**回合收据**（`run_receipt`），`envTracker` 整表删除；三条回合入口统一采样 + 盖戳 | F2（O1）+ F3（O4） | `agent/env_changes.go`、`session/manager.go` `SetRunReceipt`/`RunReceiptOf`、`session/store_adapter.go`、`agent/loop.go` | `TestEnvBaselineComesFromTheTurnReceipt`、`TestRebuiltAgentStatesTheChangeFromTheReceipt`、`TestReceiptCarriesEverySampledFamily`、`TestRunReceiptStampSurvivesAReload`、`TestEnvSignalStatesAConfigChangeThatOutlivedItsInstance` | 无（见 §7） | ❌ |
| 12 | **G10**：cron 清单进采样（排除 `LastRun`/`NextRun` 记账字段；读不到就声明读不到） | F2（O1） | `agent/env_changes.go` `cronFingerprint`、`cronLabels` | `TestEnvSignalCarriesScheduledJobChanges`、`TestCronFingerprintIgnoresRunBookkeeping`、`TestEnvSignalStatesUnreadableJobListWithoutClaimingDeletion` | 无（见 §7） | ❌ |
| 13 | 技能列表读不全 ⇒ 声明"这份列表不完整"（而不是让 agent 否认自己有某技能） | F2（O1） | `agent/skills.go`（`hydrateFailed`）、`agent/env_changes.go` | `TestEnvSignalCarriesIncompleteSkillListOnFirstTurn`、`TestEnvSignalStopsCarryingIncompleteSkillsAfterRecovery` | 无（见 §7） | ❌ |
| 14 | 工具输出被剪枝时**说清发生了什么**（不是只留一句 "truncated"） | F2（O1） | `agent/compaction.go` | 无 UT（纯文案，见 §7） | 无 | ❌ |
| 15 | **G11 stdio 半边**：MCP 通知有接收口（`NotificationSink`）→ 30s/服务闸门 → 复用 `mcpConfigNotify` 重建 → 回合级工具集信号 | F2（O1） | `mcp/client.go`、`mcp/stdio.go`、`mcp/manager.go` | `TestStdioClientHandsNotificationsToTheHandler`、`TestManagerWiresNotificationsThroughTheGate`、`TestManagerDoesNotWireATransportWithoutNotifications` | 无（HTTP 半边仍开放，见 §7） | ❌ |
| 16 | **G12**：自动回合被丢弃时逐条带全信息；用户创建的 cron 额外在该会话发一条注记 | F2（O2） | `gateway/deferred_turns.go`、`gateway/gateway.go`（注记出口，有界发送 `250ms`） | `TestDeferredTurnsAnnouncesADroppedScheduledTask`、`TestDeferredTurnsDropsMessagesPastBudget`、`TestDroppedCronNoteWithoutAJobName` | 无（见 §7） | ❌ |
| 17 | **G14**：heartbeat 与提示词读**同一个** `HEARTBEAT.md`（单一来源） | —（单一来源） | `agent/heartbeat.go` `loadHeartbeatTasks` | `TestHeartbeatReadsWhatThePromptShows`、`TestHeartbeatFallsBackToTheDiskCopy`、`TestHeartbeatWithNoFileSendsNothing` | 无（见 §7） | ❌ |
| 18 | **统一环境信号出口**：所有"你的世界变了"走**一个出口**（不是每个子系统各自发明一句），渲染在**提示词最后一段**（之前的各段逐字节不变，保住提示词缓存）；工具名与记忆进同一份快照；术语对齐 `notice → signal`（σ） | F2（C1 进真正读的通道 + C3 无变化不说话） | `agent/env_changes.go` `signalEnvironmentChanges` / `renderEnvDelta`、`agent/context.go` `SetEnvironmentSignal`、`agent/tools/registry.go` `ToolNames` | `TestEnvSignalCarriesRemovals`、`TestEnvSignalCarriesAdditionsAndEdits`、`TestEnvSignalIsSilentWhenNothingChanged`、`TestEnvSignalIsSilentOnFirstObservation`、`TestEnvSignalIsPerSession`、`TestEnvSignalIgnoresContentPreservingMemoryRewrite`、`TestReceiptCarriesEverySampledFamily`；改名后的 `TestWorkspaceSignalSurvivesTheLifecycleProxyAndTheMetaStrip` | 无（见 §7） | ❌ |

## 3. F3 · 投递（信号怎样真的到达）

| # | 改动点 | 形式（义务） | 代码锚点 | UT | 真机 e2e | 上线 |
|---|--------|-------------|---------|----|---------|------|
| 19 | **G3**：空闲驱逐产生的信号从**进程内队列**换成**耐久载体**（`configs_kv`，scope 级）；可重算的（拒绝/失败）不排队，只排队不可重算的（moved）；"沙箱被替换"的注记搭在发现它的那次调用上返回 | F3（O4）+ O2 | `gateway/sandbox_signals.go`、`gateway/userspace.go` / `gateway/reload.go`（`SetSignalStore` 接线，两个建池入口都接）、`sandbox/lifecycle.go` `parkSignal` / `takeSignals` / `takeReplacedNote` | `TestEvictSignalOutlivesThePoolThatProducedIt`、`TestReplacedSandboxNoteRidesTheCallThatFoundIt` | `TestE2BLiveRepro`（驱逐路径） | ❌ |
| 20 | 把"判据该落在哪里"形式化为**三落点**（可重算 / 复用既有耐久记录 / 新建载体），并据此把环境基线搬进回合收据（与 #11 同源） | F3（O4 的第二种形态） | `sandbox/lifecycle.go` `liveInstance`、[08 §2.2.3](./08-state-observability-principle.md)；代码见 #11 | 见 #11 | 见 #11 | ❌ |

## 4. F1-实体 · 路径与作用域不变式（G18 / G21+d1 / G17-G+H+A / G22）

> 这七条是同一句话的七次落地：**"这个键在哪个作用域"这个事实必须由知道的人声明，不能在仲裁点猜。**

| # | 改动点 | 形式（义务） | 代码锚点 | UT | 真机 e2e | 上线 |
|---|--------|-------------|---------|----|---------|------|
| 21 | **G18（01 §8）**：`apply_patch` 的 6 个 store 触点改用与 `write_file`/`edit_file` 同一个解析（`scopeSessionID()` + `wsPath()`） | F1（一条路径一个键） | `agent/tools/apply_patch.go` | `TestApplyPatchUsesTheSameStoreKeyAsWriteFile`、`TestApplyPatchDeleteUsesTheSameStoreKey`、`TestApplyPatchKeyInANonCodingSession`、`TestWriteThroughMirrorsOneKeyAndOnePath` | `TestE2BLiveOnePathIsOneKey` | ❌ |
| 22 | **G21 Fix 0**：面板删除与下载**同一路径约定**（带前缀的路径按 agent 相对解释；旧的无前缀形状继续可用） | F1（同一路径一个键） | `setup/handlers_agents.go` `handleAgentFileDelete`、`sandbox/workspace_paths.go` `StorePathScope` | `TestHandleAgentFileDelete_UsesThePathThePanelClicked`、`_ProjectPathTargetsTheChatSandbox`、`_ScopeRelativePathStillWorks`、`TestStorePathScope` | `TestE2BLivePanelDeleteSticks` | ❌ |
| 23 | **d1**：删除也穿透到**活**沙箱，且**绝不建实例**（`LiveExecutorPool` 只找活实例）；docker 那种单副本后端是 no-op | F1（写路径对称）+ 非投递 | `gateway/workspace_files.go`、`setup/handlers_agents.go`（面板侧的窄接口断言）、`sandbox/lifecycle.go` `RemoveLiveWorkspaceFile`、`sandbox/executor.go`、`sandbox/workspace_paths.go` | `TestRemoveWorkspaceFile_AsksTheExecutorsCapability`、`_NoPoolIsANoOp`、`_SingleCopyBackendIsANoOp`、`TestSandboxPathForStorePath`、`TestE2BPoolLiveExecutorDoesNotCreate`、`TestDockerPoolLiveExecutorDoesNotCreate` | `TestE2BLivePanelDeleteSticks` | ❌ |
| 24 | **G17-G**：预览容器统一按项目寻址（一个项目一个预览容器，两个入口同一个） | —（作用域不变式） | `runtime/runtime.go` `previewSandboxSession` | `TestPreviewSandboxSession` | 由 #25 的真机覆盖 | ❌ |
| 25 | **G17-H**：写入与删除**广播到项目内所有活容器**（保留每 chat 独立 shell） | —（作用域不变式） | `sandbox/lifecycle.go` `mirrorToProjectPeers` + 删除扇出、`sandbox/executor.go` `LiveProjectExecutors` | `TestWriteThroughReachesEveryContainerOfTheProject`、`TestWriteThroughCountsAContainerItCouldNotReach`、`TestRemoveLiveWorkspaceFileReachesEveryContainerOfTheProject` | `TestE2BLiveProjectWriteReachesSiblingContainer` | ❌ |
| 26 | **G17-A**：同步回写折叠到**项目根**（`syncStoreScope`）——不再产生 `<pid>/<chat>/…` 重复副本，`exec` 的产物工具立刻可见 | —（作用域不变式） | `sandbox/lifecycle.go` `syncStoreScope` | `TestSyncWritesBackToTheProjectRootNotTheChatSubdir`、`TestSyncScopeEqualsHydrateScopeForProjects` | `TestE2BLiveProjectSessionKeepsOneTree` | ❌ |
| 27 | **G22**：写入方把 store 作用域一起交下来（`sandbox.StoreScope`），盖章不再从容器推断 | F1（同一路径一个键） | `sandbox/executor.go` `StoreScope`、`sandbox/lifecycle.go`、`agent/tools/file.go` | `TestWriteThroughStampsWithTheStoreScopeItWasGiven`（**反证见 §10-7**）、`TestWriteThroughStampsTheSandboxCopyWithTheStoreTime` | `TestE2BLiveSyncReadsNoBodiesForStampablePaths`（**实测**：同一路径的整对象读取 **1 → 0**；但见 §10-8：这条读数是**测量不是反证**——±1 秒容差会把"没盖章"也盖住） | ❌ |
| 31 | **G23**：「项目会话 ⇒ 键落项目根」与**布局表**各收成**一个纯函数**（`workspace.ScopeSegments` / `workspace.WriteScope`）——文件工具、沙箱回写、面板解析、LocalFS、S3 全部改为调用它；`Registry.codingRootScope` / `SetCodingRootScope` 删除，`sandbox.StoreScope` 变成 `workspace.Scope` 的**别名** | —（作用域不变式，同一族的第四/第五处；F1 的"一条路径一个键"） | `workspace/scope.go`（新）、`workspace/localfs.go`、`workspace/s3.go`、`agent/tools/registry.go`、`agent/loop.go`、`sandbox/lifecycle.go` `syncStoreScope`、`sandbox/executor.go` `StoreScope` | `TestScopeSegmentsIsTheLayoutTable`、`TestWriteScopeCollapsesInsideAProject`、`TestAWriterScopeIsTheScopeItsKeysLandIn`、`TestLayoutWriteScopeAndParserAgree`、`TestProjectWritersAndTheSyncShareOneScope`、`TestAProjectChatSubdirKeyIsItsOwnPath`、`TestScopeSessionIDCollapsesInsideAProject`（**反证**：`scopeSessionID()` 改回 `r.sessionID`、`syncStoreScope` 改回"不折叠" ⇒ 各两条/三条变红，实测） | 作用域逻辑变了 ⇒ 复跑真机全套（`TestE2BLiveProjectSessionKeepsOneTree`、`TestE2BLiveOnePathIsOneKey`、`TestE2BLiveProjectWriteReachesSiblingContainer`、`TestE2BLivePanelDeleteSticks` 等都在这条路径上） | ❌ |

## 5. 不属于 F1–F3 的改动

| # | 改动点 | 类别 | 代码锚点 | UT | e2e | 上线 |
|---|--------|------|---------|----|-----|------|
| 28 | **G16**：技能密钥被自己的**遮罩**覆盖 —— 规则收成一个家（`mergeSkillEntry` + `mergeSkillEntries`），两条写入路径共用；深拷贝 `cloneSkillEntries` 修掉 JSON 解码器**复用 map** 的坑 | setup API 缺陷 | `setup/handlers.go` | `TestMaskedGlobalSkillSecretKeepsTheStoredValue`、`TestMaskedAgentSkillSecretKeepsTheStoredValue`、`TestMergeSkillEntriesSemantics`、`TestMaskedValueIsWhatThePanelSends` | 无（HTTP 层，见 §7） | ❌ |
| 29 | **死码与遗留配置清理**：`WorkspaceSync`（121 行，从未接线）、`makeExecTool`、整条 `BoxliteClientID` 链（config/env/管理端/web/池/执行器）、`generateRandomToken`、`filterAccounts`、`defaultIfEmpty`、`delta.deleted`、`Registry.sandboxSessionID` | 死码（CCP：没有变更原因就不该存在） | 见 [10 §9](./10-harness-state-audit.md) 的表 | 无 UT（删除本身）；验证 = 全仓引用计数 + `go build` / `vet` | 无 | ❌ |
| 30 | **取证与清理工具**：`scripts/workspace_revert_audit.py`（事故回退取证，离线 TSV 输入）、`scripts/workspace_project_chat_duplicate_cleanup.py`（A 之前产生的重复副本，`--selftest` 自检，只在"字节在项目根另有存活"时才列入删除） | 运维工具 | `scripts/` | 脚本自带 `--selftest`（6 种形状） | 无（离线；生产上按 §8 的顺序执行） | ❌ |

## 6. 复核命令

```bash
# 全量（每次改动后）
go build ./... && go vet ./internal/... && go test ./internal/... -count=1

# 真机全套（E2B；约 2 分钟）
FASTAGENT_E2B_LIVE=1 E2B_API_KEY=… go test ./internal/sandbox/ ./internal/agent/tools/ -run 'TestE2BLive' -count=1

# 分组（对应上面的编号）
go test ./internal/sandbox/ -run 'TestSyncContract' -count=1                     # #1 #2 #6
go test ./internal/sandbox/ -run 'Test(Evict|Replaced|SyncReports)' -count=1     # #8 #19
go test ./internal/store/   -run TestSandboxLeaseUnhydrated -count=1             # #9
go test ./internal/agent/   -run 'TestEnvSignal|TestEnvBaseline|TestReceipt|TestHeartbeat' -count=1  # #10–#17
go test ./internal/mcp/     -run 'TestStdioClient|TestManager' -count=1          # #15
go test ./internal/gateway/ -run 'TestDeferredTurns|TestDroppedCronNote|TestRemoveWorkspaceFile' -count=1  # #16 #23
go test ./internal/setup/   -run 'TestHandleAgentFileDelete|TestMasked|TestMergeSkillEntries' -count=1      # #22 #28
go test ./internal/sandbox/ -run 'TestWriteThrough|TestRemoveLiveWorkspaceFileReaches|TestSyncWritesBack|TestSyncScope|TestSandboxPath|TestStorePathScope|TestE2BPoolLiveExecutor|TestDockerPoolLiveExecutor' -count=1   # #5 #18 #25 #26 #27
go test ./internal/sandbox/ -run 'TestSyncContract|TestSyncVerdict|TestWriteThroughStamps' -count=1   # #1 #2 #3（含 §10 的反证目标）
go test ./internal/runtime/ -run TestPreviewSandboxSession -count=1               # #24
go test ./internal/session/ -run TestRunReceipt -count=1                          # #11
go test ./internal/sandbox/ -run 'TestLayoutWriteScopeAndParserAgree|TestProjectWritersAndTheSyncShareOneScope|TestAProjectChatSubdirKeyIsItsOwnPath' -count=1   # #31
go test ./internal/workspace/ -run 'TestScopeSegments|TestWriteScope|TestAWriterScope' -count=1  # #31
```

## 7. 没有真机 e2e 的条目，以及为什么

* **回合级环境信号（#10–#14、#17）**：机制完全在进程内 + DB，不引入沙箱。它们的 E2E 等价物是
  **真 sqlite 的往返测试**（#11 的 `TestRunReceiptStampSurvivesAReload`）与逐条渲染断言；用真机跑一遍
  只会增加沙箱开销，不会多证明一件事。
* **压缩占位符（#14）**：纯文案；由 08 的原则审查 + 代码阅读钉住。
* **技能遮罩（#28）**：HTTP 层缺陷，与沙箱无关；真机没有对应语义。
* **死码清理（#29）**：删除本身就是验证（引用计数 + 编译）。
* **仍然开放、因此没有 e2e 的**：**G11 的 HTTP 半边**（传输层没有通道 → 需要 SSE 或定期 re-list）与
  **G15**（`notifications/initialized`，需真机验证握手不受影响）。两者同属 MCP 族，按决策不在本轮范围。

## 8. 上线顺序建议（按风险）

1. **#1–#4**：事故根因。线上现在**仍可复现**（HEAD 的判据是"size 相同就跳过，否则沙箱覆盖 store"）。
2. **#21–#27**：路径/作用域那一族。其中 **#22（面板删除静默无效）**与 **#26（重复副本仍在产生）**
   是线上正在发生的用户可见问题。
   **#31** 是这一族的收口（同一族第六处：把规则收成一个家）——它与这一组一起上线，没有独立风险，
   但**必须整组一起发**：它的行为差只存在于"有项目、没有 runtime manager"的部署（那种部署里项目
   也建不出来）。
3. **#5–#20**：信号与投递。它们不修数据，但决定"以后出问题时 agent 能不能自己看出来"。
4. **#30 的清理脚本**：必须在 **#26 上线之后**执行，否则旧行为会继续产生副本（脚本 docstring 顶部已写明）。

## 9. 评审发现 → 已落地（同日）

上一轮评审在 §4 那条缝上又找到两处重复表达式（[10 §4 的 **G23**](./10-harness-state-audit.md)）：
「项目会话 ⇒ 键落项目根」被写成两种判据、出现在三处（agent 侧 `scopeSessionID()` 的 `codingRootScope`、
沙箱侧 `syncStoreScope()` 的 `projectID != ""`、面板侧 `StorePathScope()` 的前缀判定）；顺这条线再查，
**布局表**（`pid/sid` → 目录）又在 `LocalFS.scopeDir` 与 `S3.key`/`S3.scopePrefix` 各写了一遍。

两条规则现在都在 [`internal/workspace/scope.go`](../../internal/workspace/scope.go)：`ScopeSegments`
（布局）与 `WriteScope`（写者折叠）。这就是上表 **#31**，反证与实测写在 #31 与 10 §4 的 G23 行里。

## 10. 反证记录（2026-09-18，逐条实测）

每一行 = 一次**受控反证**：把某处改动改回它修之前的样子（或改成明显错误的形状），跑指定测试，记录它变红。
全部做完后已撤回，仓库里没有 `FALSIFY` 残留（`rg FALSIFY internal/` 零命中）。

| # | 针对 | 把什么改回去 | 哪个测试变红 | 观察到的输出 |
|---|------|-------------|-------------|-------------|
| 10-1 | #1（离线） | `syncSnapshot` 的判据改回 HEAD 的 **"size 相同就跳过，否则覆盖 store"** | `TestSyncContract_StoreEditIsNotOverwritten`、`_StoreEditSurvivesPostExecTrigger`、`_SandboxEditOfStorePathIsRefused` | `the sandbox copy overwrote the host's version — store: "OLD SNAPSHOT VERSION"; want: "THE HOST WROTE THIS LONGER VERSION"`（事故第一次在**离线**被复现） |
| 10-2 | #1（真机） | 同上（判据改回 HEAD） | `TestE2BLiveRepro` | `the store lost the host's version — the incident is back`；`store after sync: "OLD SNAPSHOT VERSION (1111111111111111)"`，同步日志为空（旧逻辑静默覆盖） |
| 10-3 | #2 | 把 **pod-local 基线**以最小形态放回（hydrate 时记住"交给沙箱的是什么"，同步时据此判定） | `TestSyncVerdictDoesNotDependOnThePoolThatMakesIt` | ①事故场景：`{[] [] [] }`（有记忆的 pod **什么都不说**）vs `{[] [report.html] [] }`（接管的 pod 报 BLOCKED）；③沙箱改动场景：`{[report.html] [] [] }`（**直接把未归属的沙箱改动写回 store**）vs `{[] [report.html] [] }`。同一状态、两个 pod、两种裁决 —— 这就是删掉它的理由 |
| 10-4 | #3（离线） | 穿透盖章改成写**镜像自己的时钟**（`time.Now()`）而不是 store 对象的 mtime | `TestWriteThroughStampsTheSandboxCopyWithTheStoreTime` | `want a command: touch -d @1700000001 '/workspace/app/notes.md'` |
| 10-5 | #3（真机） | hydrate 传 **零时间**而不是 `obj.ModTime`（沙箱副本不再带 store 的时间戳） | `TestE2BLiveHydrateKeepsStoreStamp` | `sandbox file mtime 1789734932 is +8s from the store's LastModified 1789734924` |
| 10-6 | #4（真机） | hydrate 把**作用域前缀**也铺进沙箱（`/workspace/sessions/<sid>/<path>`，即镜像旧硬编码路径会产生的形状） | `TestE2BLiveLooseScopeLayout` | `a loose scope's file is not at /workspace/<path> (got "no…")` + `the sandbox has a /workspace/sessions tree`；同一轮里信号层正确地报出 `[workspace] 1 path(s) are in the workspace store but NOT in this sandbox …` |
| 10-7 | #27 | 盖章的 `Stat` 改回**沙箱作用域**（G22 的原缺陷） | `TestWriteThroughStampsWithTheStoreScopeItWasGiven` | `primary container was not stamped — the store scope the caller stated was ignored: []` |
| 10-8 | #27（边界） | **移除**穿透盖章（整条 `touch` 变 no-op） | `TestE2BLiveSyncReadsNoBodiesForStampablePaths` **没有变红** | `one sync in a project session: 0 whole-object reads, 2 stats` —— 判据的 ±1 秒容差（`sameVersion`）把"store 写和镜像写在同一秒"这件事盖住了，所以**这条读数是测量，不是反证**。真正能钉住盖章的是 10-4（命令）与 10-5（hydrate 的时间戳） |

**一条夹具发现（同样实测）**：`syncFixture` 原先**先制造分歧、后 hydrate**，而 hydrate 会把 store 的内容倒回沙箱 ——
分歧在同步看到它之前就被抹掉了，所以上面三条离线契约测试在 **HEAD 的旧判据下也照样绿**（10-1 第一次跑就没有红）。
夹具已改成"先交接（两边同字节）→ hydrate → 再制造分歧"，10-1 才成为真正的反证。

### 10.1 全仓扫描：还有没有同类的"空夹具"

判据是经验性的，不靠读代码：**把 #1 的判据改回 HEAD，整包跑 `./internal/sandbox/` 与 `./internal/agent/tools/`**，
看哪些测试会红。两次对照（都是实测）：

| 形态 | HEAD 判据下的红灯 |
|------|------------------|
| **旧夹具**（分歧先、hydrate 后） | **3 条**：`TestExecObservesRefusedPaths`、`TestBlockedPathsAreReDerivedRatherThanCarried`、本册新增的 `TestSyncVerdictDoesNotDependOnThePoolThatMakesIt` —— 而三条 `TestSyncContract_*` 事故断言**全绿**，即：事故在 HEAD 上**根本进不了 CI 的红灯** |
| **新夹具**（交接 → hydrate → 分歧） | **6 条**：上面 3 条 + `TestSyncContract_StoreEditIsNotOverwritten`、`_StoreEditSurvivesPostExecTrigger`、`_SandboxEditOfStorePathIsRefused` |

逐夹具核对其余会 hydrate 的测试，结论是**它们不属于这一类**（各条的理由）：

* `sync_project_scope_test.go`：直接调**内层** `pool.Get`，从不 hydrate；且它用真 `LocalFS`，因为假 store 的
  `scopeForKey` 会把 `(pid, sid)` 折叠成 `p:<pid>` —— 这本身是"项目根 vs 项目 chat"差别的盲点。
* `broadcastFixture`：hydrate 写的是同一条路径的**写前**内容，写穿透随后覆盖它；断言看的是最终内容与盖章。
* 真机各条：要么在**建池之前**就把 store 种好（`liveE2B` 与 `e2b_live_*`），要么在**出生之后**再改
  （`e2b_live_project_broadcast_test`、`e2b_live_stamp_cost_test`）。
* `internal/agent/tools/*`：用裸执行器（没有 lifecycle 池 ⇒ 不会 hydrate）。
* `lifecycle_test.go` 的 hydrate 系列：它们**就是要**测 hydrate，先种 store 是正确顺序。

**夹具的两个已知盲点**（不是缺陷，但决定了用例该放哪）：假 store **没有 mtime**（所以时间戳类断言只能钉命令或上真机，
见 10-4/10-5）；假 store 的 `scopeForKey` **折叠 (pid, sid)**（所以"项目根 vs 项目 chat"只能靠真 `LocalFS`
或真机，见 `sync_project_scope_test.go` 与 10-7）。

## 11. 上线分组清单

> §11.1–§11.4 是**计划**（评审时定的边界）；**§11.7 是实际落地**（七笔提交、每笔的 SHA 与验证），
> 两者不一致处以 §11.7 为准，差异与原因也列在那里。§11.5 是每笔要过的门槛，§11.6 是文档入库的结果。

### 11.1 计划：六笔提交

> 2026-09-18 追加两笔：**T0**（给联网测试加闸门，先让 CI 可信）与 **T5**（形式化文档入库，见 §11.6）。
> 它们与 T1–T4 无代码依赖，顺序上 T0 放最前、T5 放最后即可。

| 提交 | 内容 | 形式 | 本册行 | 文件 | 能单独上线吗 |
|------|------|------|--------|------|-------------|
| **T0** | 测试卫生：联网测试加闸门 | —（测试卫生） | §11.5 的外部依赖 | 6 个文件（两个包各一个 `live_net_test.go` helper + 4 个挂了闸门的测试文件） | ✅ 不含产品代码 |
| **T1** | 沙箱对账与作用域 | F1 + 作用域不变式 | #1–#4、#21–#27、#31 | 25 个整文件 + 10 个跨组文件的 T1 部分 | ✅ 即 §8 的第 1、2 步（根因 + 路径/作用域），事故修复本身 |
| **T2** | 信号与投递 | F2 + F3 | #5–#20 | 36 个整文件 + 10 个跨组文件的 T2 部分 | ✅ 但**必须紧跟 T1**：其中几条 σ 描述的是 T1 引入的行为 |
| **T3** | 死码与配置清理 | —（CCP） | #29 | 7 个整文件 + 跨组文件的 T3 部分 | ✅ 纯删除，随时可发 |
| **T4** | 脚本与仓库内文档 | —（运维） | #30 | 3 个整文件 | ✅ 离线工具，无运行时影响 |
| **T5** | 形式化文档入库 | —（文档，GEB 同构） | §11.6 | 26 个文档文件 + 13 处引用旧路径的注释/脚本 | ✅ 纯文档 |

**为什么 T1、T2 要分成两笔**：T1 是"数据不再被覆盖"，T2 是"以后出事 agent 能自己看出来"。两者可以分开发布
（§8 的第 1、3 步），但**顺序不能反**——先发 T2 的话，那些 σ 描述的是一次仍然会覆盖的同步。

### 11.2 整文件归属

**T1（25）**

* `internal/workspace/{scope.go, scope_test.go, localfs.go, s3.go}`
* `internal/sandbox/{workspace_paths.go, workspace_paths_test.go, executor.go, docker_executor.go, lifecycle_sync_contract_test.go, sync_project_scope_test.go, project_broadcast_test.go, live_executor_test.go, e2b_live_repro_test.go, e2b_live_panel_delete_test.go, e2b_live_project_broadcast_test.go, e2b_live_stamp_cost_test.go}`
* `internal/runtime/{runtime.go, preview_scope_test.go}`
* `internal/gateway/{workspace_files.go, workspace_files_test.go}`
* `internal/setup/handlers_agent_file_delete_panel_test.go`
* `internal/agent/tools/{apply_patch_path_scope_test.go, apply_patch_live_scope_e2e_test.go, coding_scope_test.go, apply_patch_test.go}`

**T2（36）**

* `internal/agent/{env_changes.go, env_changes_test.go, heartbeat_source_test.go, receipt_baseline_test.go, context.go, heartbeat.go, skills.go, compaction.go, workspace_signal_e2e_test.go（改名自 workspace_notice_e2e_test.go）}`
* `internal/session/{manager.go, store_adapter.go, run_receipt_test.go}`
* `internal/store/{database.go, sandbox_leases.go, sandbox_leases_unhydrated_test.go}`
* `internal/sandbox/{lease.go, lease_pool_test.go, signal_carrier_test.go, exec_change_signal_test.go, e2b_live_unhydrated_handoff_test.go}`
* `internal/gateway/{sandbox_signals.go, deferred_turns.go, deferred_turns_test.go, reload.go, gateway.go, sandbox_pool_lease_test.go}`
* `internal/mcp/{client.go, manager.go, stdio.go, notification_test.go}`
* `internal/setup/{handlers.go, skill_entry_mask_test.go}`
* `internal/agent/tools/{write_through_signal_test.go, store_only_signal_test.go, workspace_signal_test.go（改名自 workspace_notice_test.go）, bash_session_test.go}`

**T3（7）**：`internal/sandbox/workspace_sync.go`（删除）、`internal/agent/tools/exec.go`、
`internal/config/{config.go, env.go}`、`internal/setup/{handlers_admin.go, handlers_agent_channels.go}`、`web/src/lib/api.ts`

**T4（3）**：`scripts/{workspace_revert_audit.py, workspace_project_chat_duplicate_cleanup.py}`、`docs/sandbox-scope-leak.md`

### 11.3 跨组文件（10 个，需要按 hunk 拆）

| 文件 | T1 部分 | T2 部分 | T3 部分 |
|------|---------|---------|---------|
| `internal/sandbox/lifecycle.go` | `syncSnapshot`、`statsFor`、`sameVersion`、`equalToStore`、`delta.changed`、`syncStoreScope`、`WriteThrough`、`mirrorToProjectPeers`、`lazyExecutor.WriteThroughScope`、`liveInstance`、`RemoveLiveWorkspaceFile` | `SetSignalStore`、`parkSignal`、`takeSignals`、`signalsFor`、`StoreOnlyLine`、`movedLine`、`takeReplacedNote`、`getInner` 的未水合段、`lazyExecutor.execOnce` 的渲染段 | — |
| `internal/sandbox/e2b_executor.go` | `LiveExecutor`、`LiveProjectExecutors`、结构体字段 | `publishUnhydrated`、`adoptFromLease`、`reconcileLocalLease` | — |
| `internal/sandbox/lifecycle_test.go` | `fakeExecutor.commands` 记录器（§10 的 10-4 用它） | `snapshottingExecutor` 的单副本一致性说明、`fakeLeaseStore` 的未水合字段 | — |
| `internal/agent/tools/file.go` | 5 处 `StoreScope` 传递 | `writeThroughSignal`、`storeOnlySignal`、`list_dir` 的渲染 | — |
| `internal/agent/tools/apply_patch.go` | 6 个 store 触点的键解析 | 沙箱模式的镜像 + 信号 | — |
| `internal/agent/tools/registry.go` | `scopeSessionID`、`codingRootScope` 删除 | `ToolNames`、`declareUnhydratedWorkspace` / `withWorkspaceSignal` | `sandboxSessionID` 字段（死码） |
| `internal/agent/loop.go` | `bindSession` 里的折叠接线 | 三条回合入口的采样 + `refreshSkills` 的未水合段 | — |
| `internal/gateway/userspace.go` | — | `SetSignalStore` 接线（两个建池入口） | `BoxliteClientID` 接线删除 |
| `internal/setup/handlers_agents.go` | `handleAgentFileDelete` + 窄接口 | — | onboard 的 boxlite 字段 |
| `internal/sandbox/boxlite_executor.go` | `LiveExecutorPool` 实现（no-op 语义） | — | `clientID` 整条链 |

### 11.4 两种做法，选一个

| 做法 | 提交数 | 代价 |
|------|--------|------|
| **A（推荐）** | 4 笔（上表） | 10 个文件要按 hunk 拆；每笔都必须**能编译、能跑绿**；T1 那一笔里不能出现 T2 的测试（它们引用 T2 的符号） |
| **B（省事）** | 3 笔：T1+T2 并成一笔"修复主体"，再加 T3、T4 | 不需要拆 hunk；代价是这一笔 60+ 文件，review 单位大、回滚粒度粗（想只回滚"信号"做不到） |

### 11.5 每笔的验证门槛

| 提交 | 门槛 |
|------|------|
| T1 | `go build ./... && go vet ./internal/...` + `go test ./internal/{sandbox,agent/tools,workspace,runtime,setup}/ -count=1` + 真机 `-run 'TestE2BLive(Repro\|PanelDeleteSticks\|ProjectWriteReachesSiblingContainer\|OnePathIsOneKey\|ProjectSessionKeepsOneTree\|SyncReadsNoBodiesForStampablePaths)'` |
| T2 | 离线全量 `go test ./internal/... -count=1` + 真机全套 `-run TestE2BLive` |
| T3 | 离线全量（删除类改动：引用计数 + 编译 + 测试） |
| T4 | `python3 scripts/workspace_project_chat_duplicate_cleanup.py --selftest` |

**外部依赖（已在 T0 处理）**：离线全量套件里原有**两处没有闸门的联网测试**，都让"全绿"不可靠：

| 包 | 测试 | 之前 | 现在 |
|----|------|------|------|
| `internal/skills` | `TestInstallFromSkillsSh_RepoField`（下真 tarball）+ 4 条 `TestDiagnose_*` 诊断 | 156 秒、1 失败（`probe HTTP 404`） | **0.55 秒全绿**（闸门后） |
| `internal/setup` | `TestRunInstall_GitHub_ReturnsRepoInResult`（真装一个 skill） | 122 秒、偶发失败（`runInstall github: …404`） | **7.8 秒全绿**（闸门后） |

**闸门做法**：两个包各有一个 `requireLiveNet(t)` helper（`internal/skills/live_net_test.go`、
`internal/setup/live_net_test.go`），要求显式 `FASTAGENT_NET_LIVE=1` —— 与真机 E2B 套件的
`FASTAGENT_E2B_LIVE=1` 同一约定，skip 消息里写清要设哪个变量。**实测**：带 `FASTAGENT_NET_LIVE=1`
时两条安装测试仍能复现上游 404，即失败被**显式化**（要跑就看得见），而不是被藏起来。

### 11.6 形式化文档已入库（2026-09-18 已落）

**问题**：本册与 00–10 的中英文档原先在 `/Users/reina/Project/tokenaissance/docs/fastagent/`，而那个目录
**不属于任何 git 仓库**（工作区根下挂着几十个独立仓库）—— 文档没有任何版本记录。这既解释了本次会话早先
"交付物被回退"只能靠数字节发现，也让"代码改动与文档更新同笔提交"（GEB 同构）在物理上做不到。

**已落**：整套文档进入本仓库，两个语言仍是兄弟目录：

* `docs/文件系统形式化证明/`（中文，origin）
* `docs/fs-formal-proof/`（英文 1:1 译本）

搬动时改了链接的**深度**，因为新位置离仓库根只有两级：指向代码的 `../../../fastagent/...` 改成
`../../internal/...`（共 **126 处**），指向仓库内其它文档的 `../../../fastagent/docs/X` 改成 `../X`。
文档之间的相互链接（`../fs-formal-proof/...`）不变，因为它们本来就是兄弟目录。

**校验**：搬完跑链接检查 —— `docs/文件系统形式化证明/*.md` 与 `docs/fs-formal-proof/*.md` 的相对链接
**0 条死链**；13 个引用旧路径的代码注释/脚本同步改过（`rg 'docs/fastagent/文件系统形式化证明'` 零命中）。

**因此从这一笔起，规则是**：改代码的那一笔里必须有对应的文档改动（或反之），否则视为未完成；
发布前用 §11.5 的门槛逐笔核对。

## 11.7 实际落地（as-shipped，2026-09-18）

分支 `ship/incident-2026-09-17`（从 `fastagent` 的 `16a7532` 起），**七笔**：

| 提交 | SHA | 内容 | 该笔自身的验证 |
|------|-----|------|---------------|
| **T0** | `e278e6b` | 给两个包的联网测试加闸门（`FASTAGENT_NET_LIVE=1`） | build ✓；`internal/skills` 0.59s、`internal/setup` 7.38s 全绿（此前 156s+失败 / 122s+偶发失败） |
| **T1** | `8fde5da` | 对账（size+mtime → 字节比较 → `BLOCKED` 零迁移）、删 pod 本地基线、交付戳、作用域不变式（#1–#4、#21–#27、#31） | build ✓；`internal/{sandbox,agent/tools,workspace,runtime,setup}` 全绿；真机子集 ✓（sandbox 110.1s） |
| **T2** | `39da597` | 信号与投递（#5–#20）：穿透泛化、`CompareResult` 四态、store-only、G19、G3 载体、统一环境信号出口、G11/G12/G14/G16 | 离线 **34/34 包**；真机全套 ✓（sandbox 169.5s / tools 50.0s） |
| **T3** | `175c8d6` | 死码与配置清理（#29） | build ✓；离线 34/34；pre-commit 的 eslint 通过 |
| **T4** | `260ec1e` | 离线脚本 + `docs/sandbox-scope-leak.md`（#30） | `--selftest` 通过（三类副本判定符合预期） |
| **T5** | `a087ec4` | 形式化文档入库（26 文件） | 链接检查 0 死链（见 §11.6） |
| **T6** | 本笔 | 把本节（实际切分）写进册子，使清单与落地一致 | 文档改动，无代码影响 |

**最终状态校验（T5 之后的 HEAD 上）**：`git status` 干净；对 BK3 基线 `verify.sh <repo> HEAD` →
`verify OK: 112 files match`（拆分没丢任何文件）；离线全量 34/34；真机全套 sandbox 179.0s、
agent/tools 42.9s 全绿。**当时的**线上是 `16a7532`。（2026-09-19 实测：dev 已部署 `8984c99`——
这七笔都在里面；production 仍是 `a24c0a8`。）

### 11.7.1 与 §11.3 计划的差异（六处，每处都写在对应 commit message 里）

| 文件 | 计划 | 实际 | 原因（实测） |
|------|------|------|-------------|
| `internal/sandbox/lifecycle.go` | T1/T2 按函数拆 | **整份随 T1** | 它的对账、写穿透与**信号端口**挤在同一个 diff 区段；部分切分三次都编不过（`errors` 未使用、`strconv`/`movedLine`/`takeReplacedNote` 未定义）。所以信号端口作为**定义**落在 T1，**接线与渲染**在 T2（`gateway/userspace.go` 的 `SetSignalStore` 等） |
| `internal/sandbox/boxlite_executor.go` | 全部 T3（`clientID` 链） | **随 T2** | 它唯一的调用方 `gateway/userspace.go`（`NewBoxliteExecutorPool`，去掉 `clientID` 参数）在 T2；拆开编不过 |
| `internal/agent/tools/apply_patch_live_scope_e2e_test.go` | T1 | **随 T2** | 其中的 `TestE2BLiveOnePathIsOneKey` 断言沙箱**镜像**收到了写，而镜像是 T2 的工具层（T1 里它真的红：`the write-through did not reach the sandbox`）。G18 的三条离线单测仍在 T1 |
| `internal/setup/handlers_agents.go` | 拆（删除处理 T1 / boxlite 字段 T3） | **整份随 T1** | 它的 T3 部分实际不在这个文件（在 `handlers_admin.go`/`handlers_agent_channels.go`，已在 T3） |
| `internal/agent/tools/registry.go` 的 `sandboxSessionID` | T3（死码） | **随 T1** | 它与 T1 删掉的 `codingRootScope` 是同一个字段 hunk |
| `internal/gateway/userspace.go` 的 boxlite 接线 | T3 | **随 T2** | 与同文件的 `SetSignalStore` 接线相邻，且必须与 `boxlite_executor.go` 的签名一起走 |

除这六处，§11.2 的整文件归属与 §11.3 的其余函数级归属与实际完全一致。

## 12. 跨副本轮次完整性（2026-09-19 采纳的设计，**已于 2026-09-19/20 实现**）

> 本节与上面所有节的区别：**它们是设计，不是改动**。经用户决定"先把设计做完，再一次性实现"
> （[../session-turn-integrity.md](../session-turn-integrity.md) 的 Adopted 节）。
> 所以每一行的"上线"列都是 ❌ —— 本册这一列的含义是**未部署到 production**（§0 的图例），
> 不是"待批准"。当时登记在这里，是为了让"设计 → witness"的缺口可见，而不是预支完成度。
> 形式化依据见 [12](./12-lease-formal-design.md)。

> **状态更正（2026-09-20）：它已经实现了。** 上面那段写的时候为真，现在是假的 —— A1-a…A1-e、
> 按显式签名落地的围栏（`session.WriteScope` + 内层自有的 `ErrSessionFenceLost`）、A2 的投影词汇、
> 取消契约（`chat/cancel` 返回 `{canceled, wasRunning, isRunning}`，`409 already_started` 已退役）、
> 取消路径的端到端测试（`TestCancelledTurnStopsAndSignalsOnce` —— 对端的印章是在租约端口上**模拟**的，
> 不是双副本那一跑），以及 cloud 那一半（X7：停止入口读事实并调服务端）
> 都在 2026-09-19/20 落地，各自带见证。**真正还没做完、因而让本节保持诚实的**：工具路径删除的
> **E2B 真机证明**（[10 §4](./10-harness-state-audit.md) G7b）、客户端那半的 **chat e2e**、
> 以及**双副本**那一跑（见 #32 的真机 e2e 格）—— 三者都需要凭据 / 开发服务器，这里一个也不声称。

| # | 改动点 | 形式（义务） | 代码锚点 | 计划 UT（含反证） | 真机 e2e | 上线 |
|---|--------|-------------|---------|------------------|---------|------|
| 32 | **A1** 跨副本轮次租约：store 新增 `session_turns`（键 = `sessions` 主键），`Acquire/Renew/Release/Get` CAS + 单调令牌；准入失败 ⇒ 既有 `queued` 事件 | F1（L1/L3/L5）+ F2（L6） | **已落地（工作区）**：`internal/store/database.go`（DDL + 四方法）、`internal/store/session_lease_test.go`（2 条）、`internal/agent/sessionlease.go`（端口 + `Turn` + `NopSessionLease`）、`internal/gateway/sessionlease.go`（适配器，持有者由适配器生成）。**已全部落地（工作区）**：两个准入入口（`loop.go` 的 `HandleMessage`/`HandleMessageStream`：先租约、后本地 FIFO 槽、最后释放租约）、`admission.go` 的 IfIdle 改读 `Live`、续租/释放/丢失信号（`internal/agent/turnlease.go`）、装配（`manager.go` 的 `WithSessionLease` + `gateway/userspace.go` 注入 `storeSessionLease`） | UT 已绿：store 侧 3 条（8 并发唯一赢家、过期移交、陈旧令牌不能续租/释放）+ agent 侧 3 条（等待并上报持有者与 ETA、自动回合延迟不入队、被接管即停并提示）。**两条反证已实跑**：把租约从准入拿掉 ⇒ `no queued event`；把 `Live` 从 IfIdle 拿掉 ⇒ `RunTurn error = <nil>` | cancel 路径已在**回合循环的边界**上端到端覆盖（`TestCancelledTurnStopsAndSignalsOnce`），反证已实跑 —— 但它的对端印章是在**租约端口上模拟**的（假租约记录下来），所以这不是双副本那一跑。真正仍欠、且这一格不应暗示的：**双副本**那一跑（两个 gateway 副本、同一会话、两次 `chat/stream` ⇒ 第二个排队）。没有测试碰过两个副本 | ❌ |
| 33 | **A1 围栏**：`AppendSessionMessage` / `SaveSession` 在**有围栏时**带 `EXISTS(session_turns …)` 谓词；令牌对不上 ⇒ 拒绝写入（`ErrSessionFenceLost`） | F1（L4a：围栏在**资源侧**、与写入同一原子步骤） | **已落地（工作区）**：`internal/store/sessionfence.go`（`SessionFence` + ctx 盖章 + `ErrSessionFenceLost`）、`database.go` 两条写语句（两个方言；`SaveSession` 加在 `ON CONFLICT … DO UPDATE … WHERE`，`AppendSessionMessage` 加在 `HAVING`）、`internal/session/manager.go`（`TurnFence` + `Set/ClearTurnFence`，由 `ctx()` 盖章）。**按 review 决定的形态**：`session.SessionStore` 的两个写方法加 `*session.TurnFence` 参数（端口只命名自己那层的类型），适配器翻译成 `store.SessionFence`；store 侧保留原方法不动、新增 `SaveSessionFenced`/`AppendSessionMessageFenced`（与 A3 计划中的 `PutIfVersion` 同形——带前置条件的形式单独成一个方法）。未用 ctx 隐式传递 | 已绿：`TestSessionFenceRefusesASupersededWriter`（活令牌两侧都落、接管后陈旧令牌两侧都返回 `ErrSessionFenceLost`、新持有者照常写入、无 fence 路径不变）；**反证：把 `EXISTS` 去掉 ⇒ 陈旧写者的断言不再失败** | 同 #32 场景下，被接管的回合不得再落一行 | ❌ |
| 34 | **A2** 投影不再断言"被打断"：三形态文案（持有者已死 / 持有者还活着 / 无事实），术语常量与"没有证据不许说 interrupted"的测试 | F2（O1 说真话） | **第 1 步已落地（工作区）**：`internal/provider/provider.go`（三句词表 + `SyntheticToolPads`）、`internal/agent/normalize.go`（当前一律用"无事实"句）。**两步都已落地（工作区）**：`internal/agent/turnlease.go` 的 `openCallAnswer`（租约读不到 ⇒ 无事实；对端持有 ⇒ 仍在运行；无其他持有者 ⇒ interrupted 可证），两个投影点改调 `normalizeForPromptWith`。| 已绿：`TestProjectionDoesNotClaimInterruptedWithoutEvidence`、`TestOpenCallAnswerFollowsTheLeaseFacts`（3 个子例）、3 条既有测试改为查整表；**两条反证已实跑**：把 `StoppedToolResult` 放回无条件路径 ⇒ 对端持有/读不到两个子例都红 | 无（纯投影，见 §7 的判据） | ❌ |
| 35 | **A3** 工具写 store 的覆盖保护 —— **已决定走 B 族（版本条件写）**，改动清单 B1–B11 见 [../session-turn-integrity.md](../session-turn-integrity.md) A3.1：`ObjectInfo.Version`（不透明令牌）、`PutIfVersion` + `ErrVersionConflict`、S3 用 ETag（`SetMatchETag`，minio-go v7.3.0 已核）、LocalFS 用 `size:mtime_ns` 并**声明为尽力而为**、每后端强度表、7 个写者接入 | F1（G24：`tool→store` 那条路的前置条件）+ F4 的首个声明片段（**B1–B5 已落地**：版本令牌 + LocalFS 尽力而为的条件写 + S3 的 ETag 条件 PUT + `Metered` 透传；UT `TestLocalFSPutIfVersionRefusesAStaleExpectation`，反证已实跑。**B6–B11a 已落地**（强度表 `01 §2.x`；三个文件工具、附件、技能发布；面板上传冲突返回 409 + 当前版本）；**B11-b 两侧均已落地** —— 服务端半接受可选 `expectedVersion`（⇒ 按版本替换；缺失 ⇒ 仅创建），cloud 半改为**一次请求一个文件**并提供三答案（保留两份 / 替换 / 取消）与自动改名。**09-21 补齐"投递点见证"**（`tools/write_guarded_peer_test.go`、`workspace/s3_version_test.go`、`agent/attachments_store_posture_test.go`、`skills/objectstore_posture_test.go`、cloud `attachment-conflict-flow.test.tsx`），每条反证均已实跑；附件姿态**已改**：由"拒绝 + 告警"改为"改名保留两份"，因为旧路会把一个**并未写入**的文件名放进 `[Attached: …]` 面包屑） | `internal/workspace/{workspace,s3,localfs,metering}.go`、`internal/agent/tools/{file,apply_patch}.go`、`agent/attachments.go`、`skills/objectstore.go`、`setup/handlers_agents.go`；cloud `src/features/chat/{upload-attachments,attachment-conflicts}.ts` + `upload-conflict-dialog.tsx` | 每个写者**两条**见证：规则（对端在读写之间写入 ⇒ 拒绝 + σ）与投递点（事实真正搭上的那个调用点）。**反证：去掉条件谓词 ⇒ 对端版本被静默覆盖** —— 已逐个实跑：`file.go:592`（2 红）、`file.go:702`（1）、`apply_patch.go:549`（1）、S3 的 `SetMatchETag` 分支（2）、附件 `PutIfVersion`→`Put`（3）、保留两份的循环（2）、技能发布的"先读再条件写"（1），以及 cloud 半的 4 处 | 面板上传 vs 回合并发（B11） | ❌ 未上线（代码已落地工作区；本册这一列＝未部署，见 §0） |
| 36 | **A4** 客户端不再从自己的 socket 推断服务端状态：三元（interrupted / unknown / running）+ `turnActive{holder, epoch, expiresAt}` + `queued{holder, ETA}` + `subagent_progress.id` | F3（取用侧不得回答只有产生侧能回答的问题） | **服务端已落地（工作区）**：`internal/setup/handlers.go` 的 `handleChatSubscribe`（`event: turn_active`）与 `handleChatHistory`（`turnActive` 字段）、`internal/agent/turnlease.go` 的 `queued{holder, expires_at}`。**2026-09-19/20 起 cloud 侧也已落地**：四态（running / interrupted / unknown / idle）由 `selectTurnState` 驱动加载气泡、工具行**与**停止入口（`chat-composer.tsx:94` 走 `turnIsRunning({turnState, locallyStreaming: streaming})`——本地 `streaming` 只是这个视图自己的正面证据，"谁在跑"由服务端事实回答；Stop 调 `chat/cancel`）；消息行改为接收 `turnState` 属性、不再由 `msg.streaming` 自行推导；队列 σ 的时效被遵守（`isQueuedTurnLive`，`use-stream-pipeline.ts:161`），且线上两处都写 `expiresAt`（上面的 `expires_at` 已退役）。**仍未做**：`subagent_progress.id` | 待补：三段事件序列 ⇒ 三种标签；**反证：还原措辞 ⇒ 陈旧视图重新渲染成 Interrupted** | 中途断开 SSE、再发一条 ⇒ UI 显示"仍在运行/已排队" | ❌ |
| 37 | **G25 修法**：沙箱租约的抢占分支 `epoch = epoch + 1`（令牌永不回到 1） | F1（L4c：令牌逐次唯一） | **✅ 已落地（工作区）**：`internal/store/sandbox_leases.go:69-80` | 已绿：`TestSandboxLeaseEpochNeverResetsAcrossTakeover`（严格递增 + 老令牌释放被拒 + 活行仍在）；**反证已实跑**：改回 `epoch = 1` ⇒ `gen1=1 gen2=1` 失败 | 无需真机（纯 store 语义） | ❌ |

| 38 | **O7**：客户端自己那份回答里带上 pod 发出的两条事实 —— 拒绝的稳定 **code**，以及"**已发布**的 skill 在 MCP 这一侧读起来不一样"的 **warning**（`_meta["com.tokenaissance/skills/warnings"]`，外加 `list_skills` 文本里一段）。出口自己那几条拒绝也给码：`no_name` / `name_mismatch` / `no_files` 刻意复用 pod 的拼法（一个事实一套词汇），扩展自己的上限则用 `egress_*` | **O7**（新增，[08 §10.7](./08-state-observability-principle.md)）+ 线上形状那半属 C1 | **✅ 已落地（工作区）**：tokenaissance-cloud `src/shared/services/mcp-oauth-server/{catalog,skills-service,skills-policy,policy,tools-service,index}.ts` | 已绿：`skills-list-chain.test.ts`（**投递点**：pod 应答 → 适配器 → 用例 → `_meta`），加上 `catalog.test.ts`、`skills-service.test.ts`、`policy.test.ts`、`tools-service.test.ts`；**反证已实跑**：还原 `{path, reason}` 重建 ⇒ 2 条红，返回空 warnings ⇒ 2 条红，去掉 `_meta` 键 ⇒ 3 条红 | 无（那一面目前只有清单里的实测覆盖；MCP 一致性套件仍无 SEP-2640 场景 —— 清单 E1） | ❌ 未部署（落在工作区） |

> 表中 `tokenaissance-cloud:` 前缀的路径相对 cloud 仓库（[tokenaissance/tokenaissance-cloud](https://github.com/tokenaissance/tokenaissance-cloud)，分支 `develop`）。
