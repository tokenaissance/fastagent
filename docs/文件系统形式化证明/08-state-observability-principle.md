# 08 · 状态可观测性原则（harness 设计约束）

> 状态：原则 + 落地审计 · 最后核对：2026-09-18
> 来源：2026-09-17 交付物回退事故（[04](./04-incident-workspace-2026-09-17.md)）与随后六轮修复过程中
> 反复出现同一条判断，最终提炼为本原则。文件系统同步是它的第一个应用场景，
> 但它约束的是整个 harness（见 §5 的审计范围）。
> 前置阅读：[07 §3.11](./07-formal-rootcause-and-fix.md)（机制如何按它收敛到四个）

## 1. 原则

> **harness 内的状态变更必须让 agent 可感知。**
> **否则 agent 会基于一个已经不存在的世界推理——而它没有任何办法察觉。**

这不是"要写日志"的同义反复。日志的读者是运维；**这条原则的读者是 agent**，
所以信号必须出现在 agent 真正消费的通道里（工具结果、下一轮的上下文），
而不是 slog。

## 2. 形式化表述

设 agent 在时刻 `t` 对自身世界的信念为 `Belief(t)`——它对"文件是什么内容、
有哪些技能、记住了什么、能调用哪些工具"的全部认知。设世界的真实状态为 `World(t)`。

harness 的动作会改变 `World`。原则要求：

```
∀ 变更 δ : World(t) → World(t')
    若 δ 由 agent 自己的动作触发
        则 Belief 通过该动作的结果被更新（工具结果即回执）——天然满足
    若 δ 由 harness 自行触发（同步回写、驱逐 flush、后台压缩、记忆更新、技能刷新…）
        则必须存在一个 agent 可消费的信号 σ(δ)，使
              ¬∃t'' : World 与 Belief 在 σ 未送达前持续不一致且 agent 无从察觉
```

**违反的代价是可测量的**：事故中用户看到的三个文件被回退，而 agent 连续两轮报告"已恢复"
——因为 store 被改回去这件事**没有任何通道告诉它**。它的 `Belief` 停留在一个已经不存在的
`World` 上，于是所有后续推理（"接下来做什么"）都建立在错误前提上。
注意：这比"丢了一个文件"更严重——**丢一个文件是数据损失，认知不一致是推理污染**，
而后者会扩散到后续的每一次决策。

### 2.1 形式符号 ↔ 代码标识符

代码里不再出现 `report` / `notice` 作为机制名：**δ 是事实，σ 是给 agent 的那句话**，
标识符直接沿用这套符号，读代码时不必再翻译一层。

| 形式符号 | 含义 | 代码标识符 |
|---------|------|-----------|
| `World(t)` / `Belief(t)` | 世界真实状态 / agent 对它的信念 | `envSnapshot`（回合级采样，代表 harness 认为的 `World`）；`delta`（一次同步观测到的世界变化） |
| `δ`（delta） | **一次世界变化的事实**，结构化、可累积 | `sandbox.delta{moved, blocked, deleted, problem}`（`lifecycle.go:615`）、`sandbox.WriteThroughOutcome`（`lifecycle.go:1244`） |
| `σ(δ)`（signal） | 把 δ 变成 **agent 可读的一句话** | `signalsFor(delta)`（`lifecycle.go:1015`）、`Registry.writeThroughSignal`（`file.go:894`）、`envTracker.signal`（`env_changes.go:69`） |
| 出口（exit） | 每个类别里 σ 唯一的投递点 | `lazyExecutor.Exec` 的结果（追加）、`Registry.workspaceSignalExit`（`registry.go:1094`，追加/前缀）、`ContextBuilder.SetEnvironmentSignal`（`context.go:89`，拼进提示词末尾） |
| 排队 | δ 发生了但没有投递点时先存下 | **不再有进程内队列**：`sandbox.SignalStore` 端口 + `parkSignal` / `takeSignals`，实现是 `gateway.sandboxSignalStore`（scope 级的 `configs_kv` 行：跨进程、跨副本，投递后删除）。只承载**无法重算**的那一类事实（驱逐时被写进 store 的路径）；可重算的拒绝/失败不排队 |

纪律：**机制只产出 δ（事实），措辞与放置由该类别唯一的出口决定**，
出口之外不允许自己拼字符串（`addSignal` 拿不到出口时会 warn，而不是静默丢弃）。
测试名同样沿用这套词汇：`TestExecObservesSandboxChanges`（δ 被观测到）、
`TestEvictionSignalReachesNextToolResult`（σ 送达了一次）、
`TestWriteFileStaysQuietWhenMirrorIsUneventful`（没有 δ 就没有 σ）。

### 2.2 投递的形式化：谁产生、谁投递、谁取走

> **本篇装着两套形式化推理**，它们是同一条原则的两个谓词：
> 「harness 内的**状态变更**必须让 agent **可感知**」——前半句由 **§2 / §2.1 / §3（F2 可观测性）**
> 回答（哪些变更必须说、σ 必须是真的），后半句由 **§2.2（F3 投递）** 回答（谁 place、落在 D₁/D₂、
> 谁 take、途中会不会丢）。
> 文档集里还有**第三套**——**F1 前置条件 / 零迁移**（[06](./06-cordis-review.md) / [07](./07-formal-rootcause-and-fix.md)），
> 它回答"这次迁移允不允许发生"。三者的分工是：**F1 决定"能不能动"，F2 决定"动了有没有说"，
> F3 决定"说了能不能到"**，三问全答才算设计完整。总索引见 **[00-formal-systems.md](./00-formal-systems.md)**。

§2 只说"必须存在一个 agent 可消费的 σ"——"存在"是含糊的：一条 σ 完全可以被**渲染**出来而从未到达
agent（§6.1 就丢过一次）。本节把"存在"收紧成一个可判定的方法：三个角色、一个不变量、五条义务，
外加一条"只有一种形态可达"的命题。

#### 2.2.1 三个角色与不变量 I1（三权分离）

```
产生 produce(δ) → σ   谁改变了世界，谁拥有完整事实，谁渲染这句话
投递 place(σ)         把 σ 放到 agent 必然会读到的位置
取走 take(σ)          在 agent 的下一次读取时把它带走
```

> **不变量 I1（三权分离）**：`produce` 与 `place` 由**变更侧**承担；`take` 由**消费侧**在
> 自己的读取边界上完成。不允许把 `place` 推给消费侧（"等某个子系统来取"），也不允许让
> `produce` 去捅消费侧的内部（"直接告诉正在思考的模型"）。

| 角色 | 归属 | 本仓库落点 |
|------|------|-----------|
| `produce(δ) → σ` | 变更侧 | `signalsFor(delta)`、`writeThroughSignal`、`envTracker.signal`、重建通知的渲染 |
| `place(σ)` | 变更侧 | `parkSignal`（此刻没有读者）、工具结果内的追加、`SetEnvironmentSignal`（回合入口）、cron 丢弃时的**用户侧**出站注记 |
| `take(σ)` | 消费侧（读取边界） | `lazyExecutor.Exec` 结果里的 `takeSignals(...) + signalsFor(d)`；`BuildSystemPromptAs` 把环境信号拼进提示词 |

#### 2.2.2 投递点只有两类（外加一个"暂存"）

**投递点 = agent 必然会读到的位置。** 全 harness 只有两类：

```
D₁  调用回执（工具结果）      回答"我刚调用的东西发生了什么"   只有在 agent 发起调用时才存在
D₂  轮次入口（回合提示词）    回答"我不在的时候发生了什么"     只有在有新的一轮时才存在
```

**暂存不是投递点**：在 D₁/D₂ 到来之前，σ 需要一个能跨进程存活的地方（否则 O4 不成立）。
本仓库的暂存是 `sandbox.SignalStore`（scope 级 `configs_kv` 行）；**进程内队列不是暂存**，
因为它随进程消失（09 §3 G3）。

#### 2.2.3 五条义务

| # | 义务 | 违反的代价 | 本仓库的实例 |
|---|------|-----------|-------------|
| **O1 产生** | 谁改谁渲染，且只说真话 | 假 σ 会训练模型忽略整类信号 | `CompareResult` 四态（G5）；"读不到就声明读不到"（G10） |
| **O2 投递** | σ 必须落在 D₁ 或 D₂ 上 | 信号等于没产生 | §6.1 的空闲驱逐丢弃；G7a 的 `list_dir` 分歧行 |
| **O3 取走** | 取走发生在**消费侧的下一次读取**；不得要求消费侧主动订阅 | 要求订阅 ⇒ 需要一个不可实现的接口（见 P1） | `takeSignals` 在 exec；环境信号在提示词拼装；`bash_output`（退出状态每次读取从世界重算 ⇒ 判据可重算，落点 1） |
| **O4 不丢** | 从 `place` 到 `take` 的暂存必须跨进程、跨副本 | 重启/采纳即丢 | G3：可重算的（拒绝/失败）不排队，不可重算的（moved）进耐久载体 |
| **O5 不扰** | 无 δ 即无 σ；且不得打断正在进行的推理 | 常态噪音（C3）；打断在结构上不可实现（见 P1） | docker 上静默；读不到清单不谎报删除 |

**O4 的第二种形态（2026-09-18 补）**：O4 不只是"σ 在等待区里丢了"。**σ 的判据（基线）留在进程内**
也算违反，因为基线随实例消失时，σ 不是送丢的，而是**根本产生不出来**。
G9 是最干净的例子：配置变更**靠重建 Agent 生效**，所以"该报告它的那个 tracker"恰好被它要报告的
那次变更本身销毁了——"首次观察不算变化"于是吞掉的正是这一次变化。

判据的合法落点有三种，代价递增——**按顺序尝试**：

| # | 判据的落点 | 判据 | 实例 |
|---|-----------|------|------|
| 1 | **不需要**（可重算） | 下一次读取时能从世界本身推出来 | G3 的"拒绝/失败"不排队，靠重算 |
| 2 | **复用既有的耐久记录** | 这个事实本来就有主，且那条记录本来就在写 | G9 + G20：before = 对话自己的回合收据（`session_messages` 的 provider/model + metadata 的 `run_receipt`，装整张世界快照）——零新存储、零新写路径，五个族一起跨过重启与换副本 |
| 3 | **新建耐久载体** | 前两条都不成立，事实没有别的主 | G3 的 `moved` 进 `sandbox.SignalStore`（新行，取走即删） |

第 2 条是 2026-09-18 第三轮才用上的：第一版给配置基线新建了 `cfg_seen` 行（落点 3），能工作，
但那是同一事实的第二份拷贝——同一份文档集里反复出现的形状。**先问"这份事实是否已有主"，
再考虑新建载体。**

#### 2.2.4 命题 P1：只有 pull 形态可达（push 不可实现）

> **P1**：不存在"在 agent 正在推理时把 σ 送达它"的实现。
>
> **论证**：消费者是一次**同步的模型调用**——输入是 (messages, tools)，输出是响应，协议里没有
> "中途注入"的位置；驱动层（模型 provider）不暴露接收端。要在推理中途送达，就必须由消费侧暴露
> 一个收件箱接口，即要求驱动层提供注入通道，也就是**把驱动细节写进策略**（违反依赖方向，02 §1.3/§1.4）。
> 所以 push 形态既不可实现、也不必要。∎
>
> **唯一的近似通道是用户 steer**：它把用户消息缓冲到会话上，由运行中的循环在**两次工具迭代之间**
> 取走（`appendSteer`）。两点限定：① 它仍然是"在两次模型调用之间被检查"，不是注入生成过程；
> ② 它送的是**用户输入**，不是 harness 的状态变更。因此它不构成反例。
>
> **推论 P1′**：harness 的每个 δ 都必须落在 D₁ 或 D₂ 上。"迟于事实"可接受（09 §4 注意点 A），
> "没有投递点"不可接受。

#### 2.2.5 判定过程（新机制照这个走一遍）

```
给定一个会改变 agent 世界的机制 M：

1 produce  M 改了什么（δ）？谁做的？
     agent 自己 → 工具结果即回执，到此结束（C1）
     harness    → 继续
2 σ        把 δ 渲染成一句真话；说不真就改口径，不许硬说（O1）
3 place    这句话落在哪个投递点？
     有执行中的调用 → D₁（工具结果）
     只有轮次边界   → D₂（回合提示词）
     都没有         → 不能停在这里：必须显式设计"未来的投递点"（§6.1）
4 take     谁在何时取走？答案必须是"消费侧的下一次读取"
5 O4       从 place 到 take 之间会经历什么？
     同一调用内     → 调用栈 ✔（如重建通知）
     跨调用同进程   → 进程内队列 ✘（重启即丢，G3）→ 改成重算或耐久
     跨调用跨进程   → 耐久载体，或可重算（blocked / problem）
6 O5       没有 δ 时是否静默？（C3）
```

#### 2.2.6 四层归属（Clean Architecture）

| 层 | 内容 | 判据 |
|----|------|------|
| Entities | 不变式：`Belief` 不得与世界脱节 | §1 |
| Use Cases | **投递政策**：I1 + O1–O5 + 每类别一个出口 | 政策只依赖它自己定义的端口 |
| Interface Adapters | `SignalStore`（耐久暂存）、`ReplacedWorkspace`（一次性事实）、`UnhydratedWorkspace`（状态声明）、两个投影 | 端口必须声明**语义**，不只是能力位（02 §1.3） |
| Frameworks & Drivers | `configs_kv` 行、sandbox HTTP API、**模型 provider（无接收端 ⇒ P1）** | 依赖方向：adapter → port → policy |

```
Frameworks & Drivers ──implements──▶ Interface Adapters ──▶ Use Cases ──▶ Entities
（configs_kv / 模型 provider）        （SignalStore 等端口）  （投递政策）   （不变式）
```

一句话：**投递是变更侧的主动义务，感知是 agent 的被动机制；"主动"指变更侧不许沉默，不含打断。**

## 3. 三条推论


| # | 推论 | 设计后果 |
|---|------|---------|
| C1 | **信号要进 agent 真正读的通道** | 工具结果 / 下一轮上下文；slog 不算（运维可读 ≠ agent 可读） |
| C2 | **没有信号 ≠ 没有变化** | 只要 harness 可能让世界偏离 agent 的认知，就必须说出来；沉默等于宣称"世界如你所想" |
| C3 | **信号必须是异常通道** | 没有变化就不出现。每次调用都附一行，模型会学会跳过它——那等于没有信号 |

## 4. 哪些变更需要信号

| 变更来源 | 天然可感知？ | 处置 |
|---------|------------|------|
| agent 自己的工具调用 | ✅ 工具结果是回执 | 无需额外机制 |
| harness 改了 workspace（同步回写、驱逐 flush） | ❌ | **必须报告**（含被拒绝的动作） |
| harness 改了 agent 的身份/记忆/技能 | ❌ | **必须报告**（尤其"消失"类，见 C2） |
| harness 改了工具集/可用能力（MCP 装载、技能刷新） | ❌ | **必须报告**（缺省不可见最难注意） |
| harness 改了上下文（压缩、裁剪） | ⚠️ 看到结果，未必知道发生过 | 报告"发生了什么"，而不只是呈现新状态 |
| harness 改了执行环境（沙箱被替换、重建、未 hydrate） | ❌ | **必须报告**（已有先例：`[sandbox replaced]`、`workspaceUnhydratedSignal`） |

## 5. 当前 harness 的审计

> 本节是按"变更"列的清单（哪些变更需要有信号）。按**组件**逐个体检的版本——包括
> "被 agent 触发"与"触发 agent"两个方向、以及 G5–G13 号缺口（其中 G5 是一条**恒为假**的 σ，
> 本地已复现）——见 [10](./10-harness-state-audit.md)。两节互补：本节回答"这类变更有没有信号"，
> 10 回答"这个组件有没有漏"。

| 状态变更 | agent 侧信号 | 判定 |
|---------|-------------|------|
| 工具写入 store | 工具结果 | ✅ |
| 同步把沙箱改动写入 store（post-exec） | `exec` 结果 `[workspace] the sandbox changed …` | ✅ 2026-09-18 |
| 同步被拒绝的路径 | `exec` 结果 `[workspace] NOT synced …` | ✅ 2026-09-18 |
| **沙箱删除了文件** | **无信号**：遍历的定义域是沙箱快照，被删的路径不在其中，且与“只在 store 里的上传”无法区分 | ❌ **G4**（[09](./09-sandbox-lifecycle-audit.md) §3：恢复检测需要 store 侧**持久**清单，进程内状态不行） |
| **空闲驱逐时才发生的同步** | 原先丢弃信号 → 现排队到下一个工具结果，只投递一次 | ✅ 2026-09-18 |
| 穿透替换了沙箱里的不同版本 | 写结果 `[workspace]`（含字节数，不含内容） | ✅ 2026-09-18 |
| 穿透看到沙箱里是另一个版本、且没有可比对的旧副本 | 写结果 `[workspace]`："沙箱里那份是另一个版本（N 字节），没有可比对的旧副本" | ✅ 2026-09-18（此前这一支恒说"超过 2 MiB"，见 [10](./10-harness-state-audit.md) §2.1 G5） |
| 沙箱不可达 / 被替换 | 写结果 `[workspace]`、`exec` 错误附注 | ✅ |
| **store 有、活沙箱没有的路径**（用户上传/删除造成） | `list_dir` 清单后附 `[workspace] N path(s) … NOT in this sandbox …` | ✅ 2026-09-18（此前完全无信号，见 [10](./10-harness-state-audit.md) G7a） |
| workspace 未 hydrate（store 列出失败） | `workspaceUnhydratedSignal` | ✅ 既有先例 |
| 工具结果被裁剪 | 结果内嵌裁剪标记 + "怎么看全"（`clipMarker`） | ✅ 既有先例 |
| goal 预算耗尽 | `BudgetLimitPrompt` 投递进会话 | ✅ 既有先例 |
| 上下文压缩 | 摘要与裁剪占位符都**明说发生了什么**（"earlier turns were compacted…it is lossy"、"dropped by context compaction…re-run it"） | ✅ 2026-09-18 |
| 后台记忆更新（heartbeat） | 统一的**环境变化信号**：`long-term memory was rewritten/created/CLEARED` | ✅ 2026-09-18 |
| 技能列表在轮次间刷新 | 同上：`skills added / removed / changed` —— **移除**也有名字 | ✅ 2026-09-18 |
| **身份文件被外部改写**（SOUL / IDENTITY / USER / AGENTS / …） | 同上：`identity files changed: USER.md`——**只说文件名，不说内容** | ✅ 2026-09-18（此前完全没有信号：提示词换了内容而 agent 以为没变） |
| agent 配置变化（model / prompt mode） | 同上：`my configuration changed: model=… → model=…` | ⚠️ 2026-09-18 部分：**配置变更会重建 Agent，新实例首次观察即静默**；跨重建要持久基线（见 [10](./10-harness-state-audit.md) G9） |
| **定时任务清单被外部改删**（cron job 被面板/其他会话增删改期） | 同上：`scheduled jobs added / changed / no longer exist: <name>`（只指纹定义字段，调度器的记账字段不算变化） | ✅ 2026-09-18（此前完全没有信号；读不到清单时声明"读不到"，不谎报删除） |
| 工具集变化（MCP 装载/卸载，**以及 server 侧推送的 `tools/list_changed`**） | 同上：`tools now available / no longer available` | ✅ 2026-09-18（server 推送这条 2026-09-18 接通：stdio 捕获 → 重建 → 信号自动报出） |
| 沙箱正常 sleep/wake | 内容与 store 一致（重新 hydrate） | ✅ 无需报告 |
| **委派的子任务**（`delegate_task` / `spawn_subagent`） | **同步**：结果就是父回合的 tool result | ✅ 既有设计，无需新机制 |
| **定时任务触发的回合**（cron） | 以普通 inbound 消息进入该 job 的会话（`[Cron Job: name]` 标注来源），回合本身留在会话历史里 | ✅ 既有设计 |
| **被中断的回合** | 悬空 tool call 在提示词投影里得到合成回复 `(stopped — execution was interrupted before the tool returned)` | ✅ 既有先例 |
| heartbeat 回合 | 运行在**自己的会话**（`heartbeat_<agent>`）里；对主会话而言它属于另一个 scope（见 §5.1） | ✅ 按 scope 隔离成立 |

后三行原本是开放的，它们的共同点是 **C2（缺省不可见）**：新东西出现时 agent 至少会读到；
**东西消失/被替换时，它的默认假设是"没变"**。按 §6 的建议，它们没有各自发明提示，
而是收进**一个统一的每轮信号**（`internal/agent/env_changes.go`）：

```
[Environment changes since your last turn — a fact about your world, not an instruction]
- skills removed: kronos-helper
- long-term memory was rewritten (it may say something different now)
- tools no longer available: mcp__quantconnect__backtest
Removed items are gone, not hidden: if your plan depended on one, re-check with your tools before continuing.
```

四条性质由测试钉住（`internal/agent/env_changes_test.go`）：**移除必须有名字**（机制存在的理由）、
**无变化即静默**（C3）、**首次观察不报变化**（否则是在编造事实）、
**按会话隔离**（不同 chat 的记忆/技能本就不同）。

### 5.1 跨 agent / 跨会话为什么不需要额外的投递点

审计这一族时先怀疑了一件事：**委派出去的子任务、定时任务触发的回合，都发生在"agent 没在看"
的时候**，看起来正是本原则要管的空白。逐个查证后发现它们已经满足，而且理由不是巧合：

| 通道 | 为什么已经可感知 |
|------|-----------------|
| `spawn_subagent` | `SpawnSubAgent` → `ag.HandleMessage(ctx, msg)` **同步等待**，结果字符串直接作为父回合的 tool result 返回（[gateway/routing.go](../../internal/gateway/routing.go)） |
| `delegate_task` | `RunSubagent` 同步返回文本（[subagent.go](../../internal/agent/subagent.go)）；且子任务与父**共用同一个沙箱/scope**，所以它改过的文件会在父的下一次 exec 报告里出现 |
| cron | 触发被投成一条普通的 inbound 消息（[cron/scheduler.go](../../internal/cron/scheduler.go) `fireJob`），走正常回合路径 → 写进该 job 会话的历史，来源还带 `[Cron Job: …]` 标注 |
| 回合被取消 | 未回答的调用在投影里补一条 `(stopped — …)`（[normalize.go](../../internal/agent/normalize.go)）——**这正是"结果丢了但 agent 必须知道"的既有先例** |
| heartbeat | 跑在自己的会话里；跨 scope 的变化不需要在另一个 scope 里报告（见下） |

**关键判据（补进 §6 清单）**：可观测性是**按上下文（scope/session）成立**的，
不是全局成立。一个变更只要在**它发生的那个上下文**里可感知，就不必广播到所有上下文——
否则会变成"每个会话都知道所有别的会话发生了什么"的噪音。heartbeat 回合属于
`heartbeat_<agent>` 这个 scope，它在那里有完整历史；主会话与它的联系是**记忆文件**
（而记忆变化已经由环境变化信号报出）。同理，子任务的结果属于发起它的那次调用。

于是这一族**没有新增任何机制**：审计的结论是它们已经满足原则，
再加一层投递点就是上一次被删掉的那种"重复保险"。

## 6. 审查清单（新机制必须过）

新增任何会改变 harness 状态的机制时，逐条回答：

- [ ] 这次变更是由 **agent 的动作** 引起的，还是 **harness 自己** 引起的？
- [ ] 若是后者：agent 通过 **哪个通道** 知道？（不许回答"slog"）
- [ ] **这次变更由谁 `place`？**（§2.2 O2：答案必须是"变更侧把它放到了 D₁ 或 D₂"；
      "等某个子系统来取"就是没有投递点）
- [ ] 这个信号**何时送达**？会不会晚于 agent 的下一次相关推理？
- [ ] **从 `place` 到 `take` 之间会不会丢？**（§2.2 O4：进程内状态 = 会丢；要么可重算，要么耐久）
- [ ] 信号**不出现时**是否恰好意味着"没有变化"？（C2：沉默不能有歧义）
- [ ] 信号是否只在异常时出现？（C3：常态即噪音）
- [ ] 是否有测试断言**信号存在**，以及**该静默时静默**？
- [ ] 若变更涉及"消失/替换"：agent 有没有办法确认"它还在不在"？
- [ ] **这次变更属于哪个上下文？** 它只需要在**发生的那个 scope/session** 里可感知。
      想把它广播到所有上下文之前先问：agent 在那个上下文里真的需要知道吗？
      （跨 scope 广播是 §5.1 明确排除的过度设计。）

参考实现（本目录内）：
`TestExecObservesSandboxChanges`、`TestExecIsQuietWhenNothingChanged`、
`TestExecObservesRefusedPaths`、`TestEvictionSignalReachesNextToolResult`（只投递一次）、
`TestWriteFileStaysQuietWhenMirrorIsUneventful`。

### 6.1 第 4 条为什么值得单列：信号"产生了"不等于"送达了"

**反例（本次修复中真实发生）**。同步通道有两条触发路径，第一条本来就带报告，
第二条被写成了这样：

```go
// post-exec：δ 变成 σ，挂在 exec 的工具结果上 —— 会送达 ✅
d := l.pool.syncSnapshot(ctx, l.scope, ex, "post-exec")
out += l.pool.takeSignals(ctx, l.scope) + signalsFor(d)

// evict（空闲驱逐）：δ 发生了，但此刻没有工具结果 ❌
func (p *LifecyclePool) flushIfSupported(sc sandboxScope) {
    ...
    p.syncSnapshot(context.Background(), sc, ex, "evict")   // 返回值没人接
}
```

后果：**只在空闲驱逐时发生的同步，agent 永远看不到**——而"agent 停下来的那段时间"
恰恰是最容易发生变化的窗口（后台脚本、heartbeat、另一个会话在跑）。这不是"没产生信号"，
而是**信号产生了却没有投递点**：第二条路径执行时，没有任何工具结果可以挂。

修法不是"再写一条日志"，而是**给信号找一个未来的投递点**：σ 按 scope 排队，
由该 scope 的下一个工具结果带走，且**只投递一次**
（`parkSignal` / `takeSignals`，落点是**耐久**的 `SignalStore`；测试 `TestEvictionSignalReachesNextToolResult` 与
`TestEvictSignalOutlivesThePoolThatProducedIt`——后者用**另一个 pool 实例**投递，正是进程内队列做不到的那一格）。

所以第 4 条真正要问的是：

> **这次变更发生时，"agent 正在读某个东西"这个前提成立吗？**
> 不成立（后台任务、驱逐、跨轮异步）就必须显式设计投递点，否则信号等于没有产生。

同类问题在其它子系统里同样存在：任何**发生在两次模型调用之间**的状态变更
（后台记忆更新、定时任务、外部 webhook 触发的改动）都落在这一条上——
它们的共同解法都是"排队 + 下一个投递点"，而不是"当下直接报告"。

## 7. 与其它原则的关系

| 原则 | 关系 |
|------|------|
| **F1** [Cordis 前置条件 / 零迁移](./07-formal-rootcause-and-fix.md)（07 第二部分） | 互补：前置条件决定"机制**能不能动**"，本原则（F2）决定"动了之后 agent **知不知道**"，F3 再决定"知道的话**能不能送到**"。三者缺一不可——只有前置条件会变成"拒绝但不说"，只有 F2 会变成"说了但送不到"（§6.1），只有 F3 会变成"送到了但内容有假"。三套形式化的总索引见 [00](./00-formal-systems.md) |
| [07 §3.11](./07-formal-rootcause-and-fix.md) 的四机制 | 是本原则在文件系统同步上的落地：穿透（两副本在写的那一刻一致）+ 无记忆的版本判定（δ 的判据）+ 拒绝 + 信号（事故防线）+ exec 变化信号（感知通道） |
| 早期版本被删掉的"保全副本 / 选边工具" | 违反本原则的 C3 精神：为已经宣布过的变更再配一层保险，属重复机制（07 §3.11.1 的三次形态对照） |

## 8. 一句话总结

> **机制的正确性可以靠前置条件保证；agent 的正确性只能靠可观测性保证。**
> 事故里丢掉的不只是三个文件，还有 agent 对世界的那份认知——而后者是它所有后续推理的前提。

## 9. 架构决策：要不要做"统一的状态观测机制"

按 Clean Architecture 的判据审一遍（问题：**是否该设计一个统一的观测出口**）：

| 判据 | 观察 | 结论 |
|------|------|------|
| **变化轴是否被证明**（CCP/SRP：同一变化原因 + 同一变化节奏 → 收在一起） | 到 2026-09-18 为止，同一个缝上已经改了 **6 次**：exec 变化信号、写结果信号、环境变化信号、未 hydrate 告警、裁剪标记、沙箱被替换告警 | **是**，边界被真实变化证明，可以投资 |
| **政策是否重复** | "何时附在工具结果上 / 何时排队 / 何时保持沉默"这条政策，每个机制各写了一遍——而且**漏掉过一次**（空闲驱逐的信号被丢弃，文档 §6.1） | **是**，重复的是政策，不是渲染 |
| **是否需要抽象**（YAGNI：只有一个实现就不要造接口） | 交付通道只有两种：**工具结果**（回答"我刚调用的东西发生了什么"）与**每轮提示词**（回答"我不在的时候发生了什么"） | 只需**一个事件类型 + 两个投递点**，不需要总线/订阅者框架 |

**建议的形态**（决策记录，未实施）：

1. **统一的是 δ 而不是通道**：各子系统停止自己写中文提示，改为产出一条结构化事实
   （就是 §2.1 的 δ：`{kind, paths, bytes, detail}`），由**一个** use case 决定 σ 的投递与措辞；
2. **保留两个投递点**（工具结果 / 每轮提示），因为二者回答的问题不同，合并会丢语义；
3. **政策集中后必须补上持久性**（**2026-09-18 已落地**）：当时这条写着"排队是进程内的，信号要么可重算、要么持久化"——
   现在两半都做了：可重算的（拒绝 / 同步失败）**不排队**，靠下一次同步重新推出；不可重算的（驱逐时写进 store 的路径、重建）
   分别走**耐久端口**与**当次调用栈**。进程内队列已删除（见 09 §3 G3）。

**明确不做的**（Musk 五步的前两步）：

- 不做通用事件总线 / 发布订阅 / 观察者框架——只有一个消费者（模型），
  多一层间接只会把"为什么这条通知没送到"变成更难查的问题；
- 不新增存储：通知的持久化若不可避免，应复用已有的 scope 级文档存储，而不是新表。

### 9.1 判据的最终形态：**按类别统一出口**，不做跨包框架

架构问题真正要回答的不是"有没有一个全局出口"，而是：

> **对 Agent 可观测；对每个类别（接口/包）而言，观测出口是唯一的。**

两类目标的层级不同：Agent 只看得到它的上下文（工具结果 / 提示词 / 消息），
这是**消费侧**；而"出口唯一"是**生产侧**的纪律——同一个包里的多个机制不能各自决定
怎么把话递给模型，否则政策会重复、会被漏掉（§6.1 就是这样丢过一次）。

判据里还有一条从 §2.2 继承来的定语：**出口 = 每类别唯一的投递点；投递义务在变更侧，取走时机在消费侧**
（"主动"指变更侧不许沉默，不含打断正在进行的推理——P1）。

按这条判据，本仓库已经收敛成三个类别、各一个出口：

| 类别（包/接口） | 唯一出口 | 送达方式 | 状态 |
|----------------|---------|---------|------|
| `internal/sandbox`（LifecyclePool） | `delta`（moved / blocked / problem）+ `signalsFor(delta)`；无工具结果可挂时 `SignalStore`（耐久） | 追加进该次 `exec` 的结果；跨进程/跨副本的下一条也能拿到 | ✅ 2026-09-18（无进程内队列） |
| `internal/agent/tools`（工作区类工具） | `Registry.workspaceSignalExit`：工具只 `addSignal(ctx, δ)`，出口决定放置（状态标记在前、本次事实在后） | 追加/前缀进该次工具结果 | ✅ 2026-09-18 收敛 |
| `internal/agent`（回合级） | `envTracker.signal` → `ContextBuilder.SetEnvironmentSignal`，追加在系统提示词末尾 | 每轮一次 | ✅ |

三者的共同纪律：**工具/机制只产出 δ，放置与措辞（σ）由出口决定**；
出口之外不允许自己拼字符串（`addSignal` 在拿不到出口时会 warn 而不是静默丢弃）。
这样"这句话为什么没送到 agent"永远是**一个包内一处**可查的问题。
