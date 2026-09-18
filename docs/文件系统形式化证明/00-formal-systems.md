# 00 · 三套形式化系统：总索引

> 状态：索引 · 最后核对：2026-09-18
> 本目录（`文件系统形式化证明`，前身 `文件系统`）实际承载了**三套**形式化系统：目录名只描述了第一套的
> 应用场景，后两套约束的是整个 harness。本文是它们的**唯一入口**——谁定义什么、如何组合、符号落在
> 哪些代码上、每条义务由哪条测试钉住。

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
| [08](./08-state-observability-principle.md) | **F2 + F3** | §2/§2.1/§3（F2）、**§2.2（F3）**、§5 审计、§6 清单、§9.1 出口 |
| [09](./09-sandbox-lifecycle-audit.md) | F2/F3 在**沙箱生命周期**上的逐格应用 | §2 迁移判定表、§3 G1–G4 |
| [10](./10-harness-state-audit.md) | F2/F3 在**全 harness**上的应用 | §1 全景表、**§4 缺口表（含义务列）** |
| [**11**](./11-change-register.md) | **这三套系统的交付索引**：每一处改动 ↔ 代码锚点 ↔ UT ↔ 真机 e2e ↔ 上线状态 | 逐行登记（F1 #1–#4 · F2 #5–#18 · F3 #19–#20 · 路径/作用域 #21–#27） |

## 5. 义务 ↔ 缺口 ↔ witness（可核查索引）

| 义务 | 违反它的缺口 | 钉住它的测试 |
|------|-------------|-------------|
| **O1** 产生真话 | ~~G5~~（假 σ）、~~G6~~（缺渲染）、~~G8~~、~~G10~~、~~G11~~（stdio 半边）、~~G4~~（信号半边）、~~G19~~ | `TestWriteFileSignalsUncheckedReplacement`、`TestEnvSignalCarriesIdentityFileChanges`、`TestCronFingerprintIgnoresRunBookkeeping`、`TestStdioClientHandsNotificationsToTheHandler`、`TestE2BLiveUnhydratedFactSurvivesPodHandoff` |
| **O2** 投递 | ~~G12~~、G1/G2 | `TestDeferredTurnsAnnouncesADroppedScheduledTask`、`TestEvictionSignalReachesNextToolResult` |
| **O3** 取走时机 | 无违反实例；**G13 是它的正面样本**（pull σ：判据可重算，消费侧下一次读取就是投递点） | `TestBashOutputTool_DrainsTailOnExit`、`TestSandboxJobOutputReturnsDeltaThenStatus` |
| **O4** 不丢 | ~~G3~~、~~G9~~、~~G20~~ | `TestEvictSignalOutlivesThePoolThatProducedIt`（另一个 pool 实例投递）、`TestReplacedSandboxNoteRidesTheCallThatFoundIt`、`TestRunReceiptStampSurvivesAReload` |
| **O5** 不扰 | —（迄今没有"无变化也说话"的实例） | `TestExecIsQuietWhenNothingChanged`、`TestWriteFileStaysQuietOnASharedBackend` |
| **F1** 前置条件 / 零迁移 | ~~事故 D~~ | `TestSyncContract_StoreEditIsNotOverwritten`、`SecondReconcileWritesNothing`、`DomainUnchanged`、`TestE2BLive*` |
| **F1** 边界（inside / outside） | **G4**（删除不可逆、无快照） | —（缺 witness，本身就是缺口的一部分） |

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

> **当前净剩（2026-09-18 收尾）**：形式化意义上的**只有 G11 的 HTTP 半边**
> （MCP 传输层没有通知通道，需要 SSE 或定期 re-list）。G15（`notifications/initialized`）
> 与 MCP 同族，按决策不在本轮范围。**唯一非缺口但值得记一笔的**：决策 A 之前产生的
> "chat 子目录重复副本"仍在库里（不再刷新、也无人清理）——一次性清理脚本：`fastagent/scripts/workspace_project_chat_duplicate_cleanup.py`（`--selftest` 自检；只在「同样字节在项目根另有存活」时才列入删除；**先上线 A 再跑**，否则会清了又长）。，见下面的"已关闭"清单与 [10 §4](./10-harness-state-audit.md)。

## 7. 一句话总结

> 三套形式化回答三个不同的问题，缺一套就有一种失败方式：
> **没有 F1** → 机制会覆盖别人写过的内容；
> **没有 F2** → agent 会在一个不存在的世界里推理；
> **没有 F3** → 信号产生了却永远送不到。
> 一次改动只有三问都答得出，才算设计完整。
