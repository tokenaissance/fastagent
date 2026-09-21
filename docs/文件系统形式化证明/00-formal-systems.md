# 00 · 三套形式化系统：总索引

> 状态：索引 · 最后核对：2026-09-20（新增 §4.1 的单一来源族与其边界备注）
> 本目录（`文件系统形式化证明`，前身 `文件系统`）实际承载了**三套**形式化系统：目录名只描述了第一套的
> 应用场景，后两套约束的是整个 harness。本文是它们的**唯一入口**——谁定义什么、如何组合、符号落在
> 哪些代码上、每条义务由哪条测试钉住。
> **这也是全仓形式化推理的唯一入口**：§4.1 把"形式系统 → 机制 → 它约束的设计文档"（含本目录之外的
> `../session-turn-integrity.md`、`../sandbox-pool-leases.md`、`../chat-event-delivery.md` 等）编成一张反向索引。
> **全量清点**（A 形式系统 / B 机制层 / C 子系统契约 / D 模型 / E 未形式化）见 §7。

## 1. 三套系统，三个问题

| # | 形式系统 | 定义在 | 回答的问题 | 判据形态 | 不满足的代价 |
|---|---------|--------|-----------|---------|-------------|
| **F1** | **前置条件 / 零迁移**（Cordis：reconciler、左逆、keyed diff、系统边界） | [06](./06-cordis-review.md) · [07 第二部分](./07-formal-rootcause-and-fix.md) | 这次迁移**允不允许发生**？不允许时做什么？ | 声明式：每 key 一次迁移 + 显式前置条件；违反 ⇒ **报错 + 零迁移**（R1/R2）；幂等收敛（R3）、局部性（R4） | 静默腐蚀：覆盖别人写过的内容（2026-09-17 事故） |
| **F2** | **可观测性**（`δ` / `σ`、`Belief` / `World`） | [08 §2、§2.1、§3](./08-state-observability-principle.md) | 哪些变更**必须说**？说的内容必须真？ | 全称命题：**∀ 由 harness 触发的 δ，∃ agent 可消费的 σ**；推论 C1/C2/C3 | 漏信号 → 数据损失 + **认知污染**（agent 基于一个已不存在的世界推理） |
| **F3** | **投递**（produce / place / take、O1–O5、P1） | [08 §2.2](./08-state-observability-principle.md) | σ 怎样**真的到达** agent？ | 构造性：三个角色 + 不变量 **I1** + 五条义务 + "只有 pull 可达"（**P1**） | 有信号但送不到（§6.1 的空闲驱逐、G3 的进程内队列、G12 的只进 slog） |

**一句话分工**：**F1 决定"能不能动"，F2 决定"动了有没有说"，F3 决定"说了能不能到"。**
三者是**合取**——任何一条不成立，这次改动就不成立。

## 2. 组合形态：三套串成一个判定程序

```
一个会改变 agent 世界的机制 M：

① F1  这次迁移的前置条件是什么？（07 §3.3 的判定表 / §3.11.3 的三行表）
         不成立 → 报错 + 零迁移，并且**给出正确的说法**（拒绝 ≠ 沉默）
② F2  谁改的？
         agent 自己 → 工具结果即回执，结束
         harness 自己 → 必须有 σ，且 σ 必须是真话（O1）
③ F3  谁 place？落在 D₁（调用回执）还是 D₂（轮次入口）？（O2）
④ F3  谁 take、何时 take？（O3：消费侧的下一次读取）
⑤ F3  从 place 到 take 之间会不会丢？（O4：进程内状态 = 会丢）
⑥ F2  没有 δ 时是否静默？（C3 = O5）
```

## 3. 符号总表（三套合一，含代码落点）

| 符号 | 属于 | 含义 | 代码落点 |
|------|------|------|---------|
| `World(t)` / `Belief(t)` | F2 | 世界真实状态 / agent 对它的信念 | `envSnapshot` |
| `δ` | F2 | 一次世界变化的事实 | `sandbox.delta`、`sandbox.WriteThroughOutcome`、`envSnapshot` 的各项 diff |
| `σ(δ)` | F2 | 把 δ 变成 agent 可读的一句话 | `signalsFor(delta)`、`Registry.writeThroughSignal`、`envTracker.signal` |
| C1 / C2 / C3 | F2 | 进真正读的通道 / 沉默 ≠ 无变化 / 异常通道 | 三个出口 + §5 的 witness |
| `produce` / `place` / `take` | F3 | 产生 / 投递 / 取走 | 见 [08 §2.2.1](./08-state-observability-principle.md) 的角色表 |
| **I1** | F3 | 三权分离：`place` 归变更侧，`take` 归消费侧 | — |
| **D₁ / D₂** | F3 | 调用回执（工具结果）/ 轮次入口（回合提示词） | `lazyExecutor.Exec` 的结果 / `ContextBuilder` 提示词末尾 |
| **O1–O5** | F3 | 产生真话 / 落点 / 取走时机 / 不丢 / 不扰 | 见 [08 §2.2.3](./08-state-observability-principle.md) 的义务表 |
| **P1 / P1′** | F3 | 只有 pull 形态可达；每个 δ 必须落在 D₁ 或 D₂ | 反证：代码里**没有** `AgentInbox` 一类接口 |
| 前置条件 / 零迁移 | F1 | `set(k,v)` 要求 `k∉dom`；违反 ⇒ 报错且不改任何状态 | `LifecyclePool.WriteThrough`、`syncSnapshot` 的分支 |
| R2 / R3 / R4 | F1 | 零迁移 / 收敛 / 局部性 | `lifecycle_sync_contract_test.go` |
| inside / outside（emission） | F1 | 可恢复 vs 只能补偿 | [07 §3.5](./07-formal-rootcause-and-fix.md) 的边界表 |
| 左逆 `g(δ)=γ` | F1 | 逆操作在**应用现场**产出 | `edit_file` 的 `old_string` 匹配；`<mcp-undo>` 范式 |

## 4. 文档地图：哪一篇承担哪一套

| 文档 | 承担 | 关键章节 |
|------|------|---------|
| [01](./01-current-implementation.md) | 事实基础（三套的共同前提） | §2 后端物理事实、§3.5 与 §8 路径映射 |
| [02](./02-semantics-and-architecture.md) | F1/F3 的**分层归属** | §1 四层映射、§5 所有权声明、§7 Musk 五步 |
| [03](./03-state-machine-and-timing.md) | F1 的时序 | §3 双寄存器、§7 触发条件清单 |
| [04](./04-incident-workspace-2026-09-17.md) | 实证 | §2 证据链、§6 历史归属（租约不是引入者） |
| [05](./05-remediation-plan.md) | F1 的**历史方案** | §2 P0（已被 06 修正）、§5 测试矩阵 T1–T7 |
| [06](./06-cordis-review.md) | F1 的判据来源 | §1 七条原则、§4 修正后的设计 |
| [07](./07-formal-rootcause-and-fix.md) | F1 的**权威定义** | 第二部分（论域与构造性证明）、§3.3、§3.11.3 |
| [08](./08-state-observability-principle.md) | **F2 + F3** | **§1.1 分层声明（两层的接口与张力）**、§2/§2.1/§3（F2）、**§2.2（F3）**、§5 审计、§6 清单、§9.1 出口 |
| [09](./09-sandbox-lifecycle-audit.md) | F2/F3 在**沙箱生命周期**上的逐格应用 | §2 迁移判定表、§3 G1–G4 |
| [10](./10-harness-state-audit.md) | F2/F3 在**全 harness**上的应用 | §1 全景表、**§4 缺口表（含义务列）** |
| [**11**](./11-change-register.md) | **这三套系统的交付索引**：每一处改动 ↔ 代码锚点 ↔ UT ↔ 真机 e2e ↔ 上线状态 | 逐行登记（F1 #1–#4 · F2 #5–#18 · F3 #19–#20 · 路径/作用域 #21–#27） |
| [12](./12-lease-formal-design.md) | **F1 的机制层**：从既有四个租约实现归纳出的契约（L1–L7）、as-built 归类、反例 G25、`session_turns` 的实例化与四层归属 | §3 义务表、§4 归类表、§5 反例（实测）、§6 设计规则 |

### 4.1 反向索引：形式系统 → 机制 → 它约束的设计文档

> 上一张表是"文档 → 系统"；这张表是它的**反向**，也是**形式化推理的唯一入口**：
> 先在这里找到"我在动的东西被哪套系统约束"，再去读该机制后面列的设计文档。
> 目录内链接用相对路径；目录外的文档用 `../` 或仓库路径。**新增机制/文档时先挂到这里。**

```
F1 前置条件 / 零迁移（判据来源 06，权威定义 07）
├── 机制 1 · 对账（reconciler + BLOCKED + 零迁移）
│     └─ 05 §2/§5 · 07 §3.3 · 11 册 #1–#2 · 代码 sandbox/lifecycle.go syncSnapshot
├── 机制 2 · 写入穿透 + 交付戳（宿主写完，沙箱副本同步并盖 store 的时间戳）
│     └─ 07 §3.3/§3.11.3 · 11 册 #3–#4 · 代码 lifecycle.go WriteThrough
├── 机制 3 · 租约（跨副本单写者；L1–L7 契约）
│     ├─ 本目录 12（归纳 + 反例 G25 + session_turns 实例化）
│     ├─ ../session-turn-integrity.md（A1 准入 / A1.4 围栏 / IfIdle 路径）
│     └─ ../sandbox-pool-leases.md（as-built 的一格：U/A/I 三条款）
└── 机制 4 · 条件写（**已决定走 B 族：版本条件写**；计划见 ../session-turn-integrity.md A3 的改动清单 B1–B11）→ 10 §4 G24
      （选族的判据是 **L7 前置条件可求值**：见证与效果的执行者同址 ⇒ B；只 in-band 在副本里 ⇒ A。见 [12 §3/§3.1](./12-lease-formal-design.md)）

F2 可观测性（定义 08 §2/§2.1；**含分层声明与两层张力 §1.1**）
├── 机制 1 · 回合收据（基线随回合走，不留在进程里）→ 10 §4 G9/G20 · 11 册 #11
├── 机制 2 · 统一环境信号出口（身份文件 / 配置 / 定时任务 / 技能与工具集）
│     └─ 08 §5 · 10 §4 G8/G10/G14 · 11 册 #10/#12/#13/#18
├── 机制 3 · 工具结果即回执（穿透、替换了不同版本、store-only、未水合声明）
│     └─ 11 册 #5–#9 · ../tool-output-limits.md（有界 + delivered-frames）
└── 机制 4 · 投影不说假话（pad 三形态：持有者已死 / 还活着 / 无事实）
      └─ ../session-turn-integrity.md A2/A2.1 · 10 §4 G5/G6

F3 投递（定义 08 §2.2）
├── 机制 1 · 三权分立与四个出口（工具结果 D₁ / 回合提示词 D₂ / 异常通道）→ 08 §2.2/§9.1 · 11 册 #19–#20
├── 机制 2 · 持久载体（信号随实例/回合落库，不靠进程内队列）
│     └─ 09 §3 G3 · 10 §4 G3/G19 · 11 册 #9
├── 机制 3 · 事件日志即传输层（跨副本投递 + seq 去重）→ ../chat-event-delivery.md
└── 机制 4 · 客户端只读事实（turnActive / queued{holder,ETA} / progress.id + 五态）
      └─ ../session-turn-integrity.md A4 · [tokenaissance-cloud › design/09-delegate-task-design.md](https://github.com/tokenaissance/tokenaissance-cloud/blob/develop/docs/fastagent/design/09-delegate-task-design.md)

子系统契约（同一风格、各自字母；是上面三套的实例，不是第四套）
├── W/P/O/T 会话轮次完整性 → ../session-turn-integrity.md §Invariant
├── U/A/I 沙箱池租约 → ../sandbox-pool-leases.md
├── 输出有界 + delivered-frames → ../tool-output-limits.md
├── 路径与作用域同一性（一条路径一个键）→ 01 §3.5/§8 · 02 §5
├── 项目树不变式（一个项目一棵树）→ 10 §4 G17
├── 数据域 scope 契约 → ../configs-kv-scope-decision.md · ../configs-kv-scope-adaptation.md
├── 身份与每-chatter 路由 → ../per-chatter-files.md
└── 协议合规族（不属于 F1–F3）→ ../mcp-oauth-design.md · ../issues/ext-skills-conformance-checklist.md

**单一来源 / 表达式唯一族**（**平级契约，不是第四套**；判据形态是"这条事实被写成了几份"，
与 F1–F3 的三种都不同。依据：§7 对 S1/S2/S3 的同一判词——"从权威来源重算，不要记住"是 F2 的对偶，即一个 C）
├── 同一条规则不许有第二种表达式（布局表 / 写者折叠各收成一个纯函数）→ 10 §4 G23 · 代码 `workspace/scope.go` 的 `ScopeSegments` / `WriteScope`
├── 同一份输入不许有两个来源（心跳文件：提示词读 store，触发却读磁盘）→ 10 §4 G14 · 代码 `agent/heartbeat.go` 的 `loadHeartbeatTasks`
├── 同一条守卫不许写两遍（技能遮罩合并）→ 10 §4 G16 · 代码 `setup/handlers.go` 的 `mergeSkillEntry` / `mergeSkillEntries` / `cloneSkillEntries`
├── 判据不许新造第二份拷贝（先问"这份事实是否已有主"）→ 08 §2.2.3 落点 2 · 10 §4 G20（回合收据承载整张快照；反例是 `cfg_seen` 行）
├── 作用域契约（blob 权威 / KV 忠实镜像——**迁移期的临时不变式**，退出条件是翻转权威）→ ../configs-kv-scope-decision.md · ../configs-kv-scope-adaptation.md
└── 每个类别一个出口（同一个包里，机制不许各自拼字符串递给模型）→ 08 §9.1

```

> **与上面「子系统契约」那一块的区别在切法**：那一块按**域**切（谁有权动这个键），本族按**形态**切
> （这条事实被写了几份）。所以 G17/G22/G23 会在两处各出现一次——缺口表记的是域，这里记的是形态。
> **一处边界留待决定**：§7 给 A/C 定的分界是"C 的行与 A 同问同形态"，而本族的判断形态与 A 不同；
> 把它记作 C 是沿用了 §7 对 S1/S2/S3 的判词，不是由那条分界推出来的。见 §7 的备注。

```

还没被任何一套覆盖 → §7 的 E 桶（F4 候选：并发与可见性，暂不落地）
```

## 5. 义务 ↔ 缺口 ↔ witness（可核查索引）

| 义务 | 违反它的缺口 | 钉住它的测试 |
|------|-------------|-------------|
| **O1** 产生真话 | ~~G5~~（假 σ）、~~G6~~（缺渲染）、~~G8~~、~~G10~~、~~G11~~（stdio 半边）、~~G4~~（信号半边）、~~G19~~；`{baseDir}` 诊断的触发条件（登记册第 39 行） | `TestWriteFileSignalsUncheckedReplacement`、`TestEnvSignalCarriesIdentityFileChanges`、`TestCronFingerprintIgnoresRunBookkeeping`、`TestStdioClientHandsNotificationsToTheHandler`、`TestE2BLiveUnhydratedFactSurvivesPodHandoff`、`TestBaseDirTokenInABundledFileIsNotAReaderDifference`、`TestBaseDirWarningListsEveryCarrierNotOnlyTheManifest`、`TestCatalogHandlerCarriesTheCodesAndTheWarnings` |
| **O2** 投递 | ~~G12~~、G1/G2 | `TestDeferredTurnsAnnouncesADroppedScheduledTask`、`TestEvictionSignalReachesNextToolResult` |
| **O3** 取走时机 | 无违反实例；**G13 是它的正面样本**（pull σ：判据可重算，消费侧下一次读取就是投递点） | `TestBashOutputTool_DrainsTailOnExit`、`TestSandboxJobOutputReturnsDeltaThenStatus` |
| **O4** 不丢 | ~~G3~~、~~G9~~、~~G20~~ | `TestEvictSignalOutlivesThePoolThatProducedIt`（另一个 pool 实例投递）、`TestReplacedSandboxNoteRidesTheCallThatFoundIt`、`TestRunReceiptStampSurvivesAReload` |
| **O5** 不扰 | —（迄今没有"无变化也说话"的实例） | `TestExecIsQuietWhenNothingChanged`、`TestWriteFileStaysQuietOnASharedBackend` |
| **O7** 已投递但含义变了的事实 | 2026-09-21 出口审计（无 G 编号：记在 [11](./11-change-register.md) 第 38 行） | `skills-list-chain.test.ts`（投递点）+ `catalog.test.ts`、`skills-service.test.ts`、`policy.test.ts`、`tools-service.test.ts` |
| **F1** 前置条件 / 零迁移 | ~~事故 D~~ | `TestSyncContract_StoreEditIsNotOverwritten`、`TestSyncContract_SecondReconcileWritesNothing`、`TestSyncContract_DomainUnchanged`、`TestE2BLive*` |
| **F1** 边界（inside / outside） | **G4**（删除不可逆、无快照） | —（缺 witness，本身就是缺口的一部分） |

> O6 与 D₃ 是在这张表写完之后才在 [08 §10.3 / §10.2](./08-state-observability-principle.md) 里
> 升格为义务的，本表还没有它们的行；它们的见证在产生它们的 cloud 那几轮里。O7 是连行一起加的，
> 免得这个索引继续漂移。

### 5.1 witness 有两半（2026-09-21，由 cloud 侧再审计带回）

> **这一条只动上表的第三列：不加义务、不加缺口族、不产生任何设计工作。**它规定的是"什么才算钉住"
> ——**验证侧**的规则，不是关于系统的陈述。F1–F3 与 O1–O7 没有任何一条改变含义。

那一列今天只能回答一个问题：**规则被测了吗？** 但"产生事实"和"取走事实"是两个不同的事件，
一条义务通常有多条投递链（渲染、store、wire、另一个视图）。于是存在一种状态：一切都绿，
而用户仍然什么都看不到——规则被测、文案齐备，唯一把事实送到终点的那个**调用点**从没有任何测试碰过。

判据：

> **一条义务算落地 ⇔ 存在规则见证，且每一个投递点各有见证；反证必须能打红投递点那条测试**
> ——只打红规则见证，对该投递点不构成任何证据。

适用范围（否则它会退化成"多写测试"的口号）：只在**把事实送出去的义务**上要求（O1–O7、D₃ 族）。
线上形状 / 字段拼写这类契约（C 族）没有独立的投递点——**规则见证就是投递点见证**。

**已退役的失败形状（本轮实例）**：cloud 的 `src/features/chat/turn-state.ts:143` 的 `toolRowLabelKey`
是规则见证，它的投递点是同一侧的 `message-list.tsx:61` 调用点。那个调用点当时只传本地 `turnOver`，
于是渲染层 `grep tool_running_elsewhere` **零命中**，行永远说 "interrupted"；而 group header 走的是
另一条独立推导、所以是对的——测试只断言 header，整套云侧单测因此是绿的。同一个 grep 今天仍然零命中，
含义却相反：调用点已经把事实传下去了，并且有 `describe('the live-turn fact reaches the tool ROW')`
四例（含"unknown 时 header 不得说 stopped"）。一个 grep 事实，两种相反含义，差别只在
**事实有没有到那个点**。

（这条是 [08 §6.1](./08-state-observability-principle.md) 的反面镜像：那里从产生侧说"信号产生了 ≠ 送达了"，
这里说的是它在**验证侧**的同一句话。）

## 6. 还没做完的，按形式系统归类

| 项 | 属于 | 状态 |
|----|------|------|
| **G11** HTTP 侧通知 | F2 · O1（传输层根本没有通道） | 开放（stdio 侧已修；HTTP 需 SSE 或定期 re-list） |
| **G4** 沙箱内删除的**归属** | F2 · O1（**已修到"事实已声明"**）+ F1 边界（不可逆操作缺快照） | **已决策：不做归属（2026-09-18）** —— 后果已送达，清单只买因果而代价是每次 hydrate 上千行；见 [05 §8](./05-remediation-plan.md) 决策记录 |
| **G7b** 上传/删除写进活沙箱 | **不属于 F1–F3**：写路径对称性 | **上传半边：已决策 a（不穿透，"面板＝文件库"）**；**删除半边：已决策 d1 并已修**（穿透到活沙箱，绝不建实例） |
| ~~**G21**~~ 面板删除静默无效（路径/作用域双重前缀） | **属于 F1**（"同一路径一个键"） | **已修（2026-09-18）**：Fix 0（删除与下载同一路径约定）+ d1（同时删掉活沙箱那份），两者成对落地；真机两半都钉住（不加 d1 会复活、加了不复活） |
| ~~**G17**~~ 项目里"一个文件树、多个容器"（预览容器错位 + 兄弟容器收不到写入 + 同步回写到 chat 子目录） | **不属于 F1–F3**：作用域不变式 | **已决策 + 已修（2026-09-18：G+H+A）**：预览容器按项目寻址（G）；写入/删除广播到项目内所有活容器（H）；**同步回写折叠到项目根**（A，`syncStoreScope`）；**保留每 chat 独立 shell**。未做迁移：折叠前产生的副本仍在库里 |
| ~~**G22**~~ 写入穿透的 mtime 盖章在项目会话里静默失效（查错 store 作用域） | **属于 F1**（“同一路径一个键”的第三处） | **已修（2026-09-18）**：写入方把 store 作用域交下来（`sandbox.StoreScope`）；真机实测同一路径的整对象读取 **1 → 0**；这条缝的三处缺陷（G21 / G17-A / G22）已全部修完 |
| **G15** `notifications/initialized` 未发送 | **不属于 F1–F3**：协议合规 | 开放（需真机验证握手不受影响） |

**已关闭（同日，供索引对照；细节见 [10 §4](./10-harness-state-audit.md)）**：
G1–G3（投递点/耐久载体）、G4 的信号半边、G5/G6（σ 说假话）、G7a（分歧可见）、G8/G9/G10（外部改写）、
G11 的 stdio 半边、G12（丢弃有痕迹）、G14（单一来源）、G16（遮罩写回）、**G18**（01 §8 路径解析）、
**G19**（未水合声明随实例换手丢失）、**G20**（环境采样基线进回合收据，`envTracker` 删除）。
其中 **G13 被改判为"非缺口"**：它是 pull 形态的 σ（判据可重算 ⇒ 落点 1），不是 F3 的缺口。

这张表本身就是一次分类结论：**"还开着的事"里只有一部分是形式化意义上的缺陷**——
G11 是（传输层没有通道），G4 的归属半边是（缺持久清单），
其余三件是别的族（产品决策 / 作用域不变式 / 协议合规），不该被记成"可观测性没做完"。

**2026-09-19 追加一格（F1 族，不是 F2/F3）**：**G25** —— 沙箱租约的围栏令牌 `epoch`
每次接管归 `1`（`internal/store/sandbox_leases.go:72`/`:81`），因此"同一个 `owner` 换代后，
迟到的释放能删掉新世代的活行"（探针实测 `released=true`）。它违反的是
[12 §3](./12-lease-formal-design.md) 的 **L4(c)（令牌必须逐次获取唯一）**，
修法是一条款（抢占分支 `epoch = epoch + 1`）——**已修（2026-09-19，工作区）**，witness 是 `TestSandboxLeaseEpochNeverResetsAcrossTakeover`（反证已实跑）。同一节还给出该义务在 `session_turns` 上的落法，其中**A1 已全部落地（工作区）**：`internal/store` 的 `session_turns` 四方法、`internal/agent/sessionlease.go` 的端口、`internal/gateway/sessionlease.go` 的适配器、两个准入入口与 IfIdle 的接线、以及围栏（`…Fenced` 写语句）。

> **当前净剩（2026-09-18 收尾）**：形式化意义上的**只有 G11 的 HTTP 半边**
> （MCP 传输层没有通知通道，需要 SSE 或定期 re-list）。G15（`notifications/initialized`）
> 与 MCP 同族，按决策不在本轮范围。**唯一非缺口但值得记一笔的**：决策 A 之前产生的
> "chat 子目录重复副本"仍在库里（不再刷新、也无人清理）——一次性清理脚本：`fastagent/scripts/workspace_project_chat_duplicate_cleanup.py`（`--selftest` 自检；只在「同样字节在项目根另有存活」时才列入删除；**先上线 A 再跑**，否则会清了又长）。，见下面的"已关闭"清单与 [10 §4](./10-harness-state-audit.md)。

## 7. 全量清点：形式系统 ↔ 机制层 ↔ 子系统契约 ↔ 模型 ↔ 未形式化

> 这一节回答"这个系统里现在到底有哪些形式化"。分四桶；判据是**问题与判断形态**，不是符号多少：
> **A 是形式系统、B 是形式系统的机制层；C 是同风格但属于 A 在某个子系统上的实例；
> D 只是被 A 当定义域用的模型；E 还没被任何一套覆盖。**
>
> A 与 C 的分界：A 的每一行回答一个**不同的问题**、判断形态也不同（声明式 / 全称 / 构造式）；
> C 的行与 A **同问同形态**，只是换了键与义务。由此得到**新增一套形式系统的门槛 = 新问题 + 新判断形态**；
> 否则它就是某一套的机制或实例——租约正是 F1 的机制层（[12](./12-lease-formal-design.md)）。

| 桶 | 成员 | 位置 | 为什么在这一桶 |
|---|------|------|---------------|
| **A** 形式系统 | **F1 / F2 / F3** | §1；定义在 [06](./06-cordis-review.md) · [07](./07-formal-rootcause-and-fix.md) · [08](./08-state-observability-principle.md) | 三个不同的问题 + 三种不同的判断形态，缺一就少一种失败方式 |
| **B** 机制层 | 租约契约 **L1–L7** + 四个既有实现的逐格归类 | [12](./12-lease-formal-design.md) | 回答的仍是 F1 的问题；判决是 F2 的 δ/σ、送达是 F3 的义务 ⇒ **不是第四套** |

**C. 子系统契约**（同一风格的可证伪条款表，各自一套字母；都是 A 的实例）

| 契约 | 定义 | 条款 | 代码里被引用的位置 | witness |
|------|------|------|-------------------|---------|
| 会话轮次完整性 | [../session-turn-integrity.md](../session-turn-integrity.md) §Invariant | **W** 单写者 · **P** 调用/回复成对 · **O** 顺序 · **T** 投影不说假话 | `internal/session/manager.go:86`、`internal/agent/loop.go:2322`/`:2526`/`:3279` | `turn_queue_test.go:97`/`:148`、`tool_recovery_test.go:215`、`TestConcurrentWebAndCronTurnSerialize` |
| 沙箱池租约 | [../sandbox-pool-leases.md](../sandbox-pool-leases.md) | **U** 命名权威 · **A** 免重建 · **I** 行指向真实实例 | `internal/sandbox/lease.go:57-61` | `lease_rebuild_test.go:421`/`:469`、`TestE2BPool*` |
| 工具输出有界 | [../tool-output-limits.md](../tool-output-limits.md) | 结果离开生产者即有上界 + **delivered-frames 条款** | `internal/sandbox/e2b_executor.go:1161` | `TestE2BExecClockHints` |
| 事件投递 | [../chat-event-delivery.md](../chat-event-delivery.md) | 事件日志即传输层：D1–D5 决策 + R1–R4 反证 | SSE 订阅 / tail 轮询 | — |
| 路径与作用域同一性 | [01 §3.5/§8](./01-current-implementation.md) · [02 §5](./02-semantics-and-architecture.md) | 一条路径一个键（G18/G21/G22 同族） | `scopeSessionID`/`wsPath`、`sandbox.StoreScope` | `TestE2BLiveOnePathIsOneKey` |
| 项目树不变式 | [10 §4](./10-harness-state-audit.md)（G17） | 一个项目一棵树、多容器、写广播 | `LiveProjectExecutors`、`syncStoreScope` | — |
| MCP OAuth 安全条款 | [../mcp-oauth-design.md](../mcp-oauth-design.md) | state 一次性（`Take` 读即删）+ PKCE S256 + TTL + 绑定 userID | `usecase/complete`、`pending_store.go` | `internal/mcp/oauth/...` |
| MCP 一致性（**协议合规族**，不属于 F1–F3） | [../issues/ext-skills-conformance-checklist.md](../issues/ext-skills-conformance-checklist.md) | 规范 MUST ↔ 出口实现；**声明即承诺** | `server/discover`、`skills/list` | 该清单的"现状"列；G11 HTTP 半边、G15 仍开放 |
| **单一来源 / 表达式唯一** | [10 §4](./10-harness-state-audit.md)（G14/G16/G20/G23）· [08 §2.2.3](./08-state-observability-principle.md)（落点 2）· [08 §9.1](./08-state-observability-principle.md) | **一条事实只有一种表达式**：来源唯一（G14）· 守卫唯一（G16）· 规则唯一（G23）· 判据复用既有主（§2.2.3 落点 2）· 出口唯一（§9.1） | `agent/heartbeat.go` `loadHeartbeatTasks`、`setup/handlers.go` `mergeSkillEntry(s)`/`cloneSkillEntries`、`workspace/scope.go` `ScopeSegments`/`WriteScope` | `TestHeartbeatReadsWhatThePromptShows`、`TestMaskedGlobalSkillSecretKeepsTheStoredValue`、`TestScopeSegmentsIsTheLayoutTable`、`TestAWriterScopeIsTheScopeItsKeysLandIn` |

> **备注（2026-09-20，单一来源族入场时发现）**：本节开头那条分界（"C 的行与 A **同问同形态**"）
> **不覆盖**上表最后一行的单一来源族——它的判断形态（"这条事实被写成了几份"）与 F1–F3 的三种都不同。
> 把它归为 C，依据的是 §7 对 S1/S2/S3 的同一判词（"从权威来源重算，不要记住"是 F2 的对偶），
> 而不是那条分界。所以这里留下一条**尚未落地的区分**：要么承认"形态不同但同源（都是同一条纪律的实例）"
> 也算 C，要么给它单列一档。**暂不决定**——目前只有一个成员族，按"第三个变体证明了同一缝再抽"的惯例不动。

**D. 被当成定义域用的模型**（不是形式系统，是被 A 的判据指向的对象）

| 模型 | 位置 |
|------|------|
| 双寄存器（docker 单寄存器 / e2b 双寄存器 + 单向回写） | [03 §2/§3](./03-state-machine-and-timing.md) |
| 沙箱 7 状态生命周期与逐迁移判定 | [09 §1/§2](./09-sandbox-lifecycle-audit.md) |
| `delegate_task` 四态 → 五态（客户端侧） | [tokenaissance-cloud › design/09-delegate-task-design.md](https://github.com/tokenaissance/tokenaissance-cloud/blob/develop/docs/fastagent/design/09-delegate-task-design.md) |
| 四层映射（Clean Architecture） | [02 §1](./02-semantics-and-architecture.md) · [../mcp-oauth-design.md](../mcp-oauth-design.md) §1 |
| MCP OAuth 的流程状态（pending → code → tokens） | [../mcp-oauth-design.md](../mcp-oauth-design.md) |

**E. 还没被任何一套覆盖**（这一桶空着不是问题，**不知道它空着**才是）

| 缺口 | 今天靠什么 | 会被谁吸收 |
|------|-----------|-----------|
| store 的并发写语义：一次 `Put` 在并发下到底保证什么 | 隐含假设 last-writer-wins，**正在被 A3（B 族）首次声明**：`PutIfVersion` + 每后端强度表（S3 用 ETag 精确；LocalFS 为尽力而为） | 可能是**真正的新一套**（新问题：并发下的可见性与覆盖语义）；G24/A3 是它的第一个实例 |
| `session_key` 的生成竞态（IM 首条消息跨副本各铸一个键） | 无 | F1 的边界（"一个键一次迁移"），见 [12 §6](./12-lease-formal-design.md) |
| 保留 / GC 策略（谁在什么时候删对象） | 一次性清理脚本 + 人工判断 | 未定；与 F1 的"零迁移"相邻 |
| **进程驻留：harness 允许在内存里留住什么、留多久** | 隐含假设"留到进程结束"。2026-09-19 已审计（[10 §10](./10-harness-state-audit.md)）：三张表随历史增长 —— 生产在跑的构建里的 `session.Manager.sessions`（G26）、`tools.shellManager.shells` 与 `tools.sandboxJobs.live`（G27） | **不是新一套**：这是 F2 的对偶（S1/S2/S3 —— *从权威来源重算，不要记住*），即一个 C，不是新问题。判据本身已并入 10 §10 |

> **备注（F4 候选，2026-09-19：已分析，暂不落地）。** 候选 = **「一次操作的语义 / 并发与可见性」**：
> 问题不是"谁有权动"（那是 F1），而是"两个操作并发作用于同一个键时，哪个值可见、给谁、什么时候可见"；
> 判断形态是**执行历史的归属判定**（可线性化 / 读己写 / 单调读 / 快照读），与 F1/F2/F3 都不同 ⇒ 形式上够格。
> 同缝证据已有 **4 次**：`LocalFS.Put` 是 `O_TRUNC` 原地写（读者可见半份文件，`internal/workspace/localfs.go:88`）·
> `S3.Move` 自己注释 "Not atomic"（`internal/workspace/s3.go:165`）· G24（`Put` 无前置条件；09-18 同一交付物被写两次
> 15 348→11 492 字节）· A3 的三档修法。
>
> **决定：暂不落地。** 根因不是有人漏写语义，而是**原始设计的适用条件变了**（见 §7.1）：设计诞生于
> 单进程 + 本地文件（会话 JSONL、`workspace/` 只是模板目录），那时"一次写意味着什么"是免费的、无需声明的；
> 2026-04-20 `950070b`（cloud-ready / stateless gateway）同时引入 S3 后端、多写者与 `LifecyclePool`
> （也正是 [04 §6](./04-incident-workspace-2026-09-17.md) 定位的缺陷引入点），隐式保证从此变成没人写下的义务。
> 若将来要落地，落点是 [01 §2](./01-current-implementation.md) 的"每后端公理表"（零代码），**不是**新开第 13 篇。
>
> 重新打开的条件（任一）：① F1 的判据第一次**因为"读到的不是一份完整对象"而失效**（例如 LocalFS 上 hydrate 读到半份文件被实测到）；
> ② 出现**第二次**静默覆盖（目前只有 09-18 一次）；③ 出现需要同一份公理的**第三个后端**。

> 与 §6 的分工：§6 列的**是**三套系统内部的未完项（G11/G15/G4 归属半边…）；本节 E 桶是**还没被任何一套覆盖**的东西。
> 用法：读一段代码前先问"它被哪一套约束"；写新机制前回答 §6 的清单；若答案是"哪一套都不是"，
> 那要么它是 C（同一形态的新实例），要么我们在**提出第四套形式系统**——后者必须同时拿出新问题与新判断形态。

### 7.1 原始设计（为什么这些语义没有被写下来）

> 这一小节回答"是不是当初就漏了"。**不是漏了，是条件变了。** 证据按时间：

| 时间 | 事件 | 当时的语义前提 |
|------|------|---------------|
| 2026-03-09 | `d04a132` MVP；同日 `500b793` 把 `DESIGN.md`（184 行）**移出仓库并加进 .gitignore** | 原始设计明写 **"Minimal / Go-native / Message bus (channels) / Files as memory"**；会话是 **"append-only + JSONL 文件持久化"**；`workspace/` 只是**模板目录**（AGENTS.md/SOUL.md/USER.md/TOOLS.md）。**单进程、单机、单写者** ⇒ 原子性/可见性/版本都不需要声明 |
| 2026-03-17 | `0cdb64e` "pluggable storage backend (file + database)" | 第一次出现**两个后端**；此时差异还只体现在"存哪"，没有并发写者 |
| 2026-04-20 | `950070b` "cloud-ready architecture — stateless gateway, per-key scoping" | 一次提交同时引入 **S3 后端 + `LocalFS`+`S3` 双实现 + `LifecyclePool`（hydrate-on-create / flush-on-evict）**。多写者、两物理副本、三后端**同日成立**，而原始设计的隐式保证一条都没被重述 |
| 2026-09-17 | 生产交付物静默回退 | [04](./04-incident-workspace-2026-09-17.md)：缺陷诞生点即 `950070b`；租约只是放大器 |

两条至今可见的痕迹：

1. 今天的 README §Architecture 仍写着 **"Output files | Application | Your app / S3"** —— 在原始设计里，
   产出文件**不属于运行时**；`internal/workspace` 是把这件事吃进来之后才出现的（`950070b`）。
2. 原始设计里唯一被写死的"法律"是**提示词缓存友好的追加与位置约定**（"Session messages are append-only"、
   "Variable runtime info placed in user messages"）——同一份文件里，**被写在纸上的不变量活了下来**，
   没被写下来的（一次写的语义）在条件变化时默默失效。这正是本题（F2/F3 与 [08 §6](./08-state-observability-principle.md) 清单）存在的理由。


> **补丁（2026-09-19，由 cloud 侧复核带回）**：三套系统在"**消费者是用户界面**"这一情形下需要四条补充——
> **O1′**（状态 σ 必须随行携带时效/过期 ⇒ 降级为 unknown；**事件 σ** 不可变、不得被过期抹除）、
> **D₃**（UI 的投递点是"一次渲染"：读缓存/组件状态；O3/O4 必须在 D₃ 上重新陈述）、
> **O6**（δ 的**撤销**与出现同等可见——"缺省不可见"由推论升为义务）、
> 以及 C 族两条正交契约：**C1 一个事实一种线上形状**（同事实多出口必须同 schema，根治靠共享类型）、
> **C2 witness 取自生产者真实载荷**（契约测试夹具不得手写）。
> 完整表述见 [08 §10](./08-state-observability-principle.md)。

## 8. 一句话总结

> 三套形式化回答三个不同的问题，缺一套就有一种失败方式：
> **没有 F1** → 机制会覆盖别人写过的内容；
> **没有 F2** → agent 会在一个不存在的世界里推理；
> **没有 F3** → 信号产生了却永远送不到。
> 一次改动只有三问都答得出，才算设计完整。
