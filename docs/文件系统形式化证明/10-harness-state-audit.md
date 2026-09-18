# 10 · 全 harness 状态变更审计：被 agent 触发 & 触发 agent

> 状态：审计（2026-09-18）· 方法：[08](./08-state-observability-principle.md) §2 的形式化
> （δ = 世界变化的事实，σ = agent 可读的信号）+ §6 审查清单
> 范围：`fastagent` 里**所有**会改变 agent 世界、或会把 agent 拉起来跑一轮的组件
> 与 09 的关系：09 只审了沙箱生命周期（G1–G4）；本文是它的补集与总览，并给出 G5–G13
> 证据等级：**【码】**代码路径已核对（带 `file:line`）· **【测】**有测试钉住 ·
> **【探】**本地探针复现（§7 附录给了可重跑的探针）· **【产】**生产数据

> ## ⚠️ 部署状态（2026-09-18 记；读本文前先看这一段）
>
> **本文所有"已修（2026-09-18）"指的都是工作区（worktree）状态：未提交、未部署。**
> `HEAD` = `16a7532`（2026-09-17）；工作区相对它 +2163/−359、76 个文件。差别不是细节，而是
> **整批机制的有无**：
>
> | 机制 | HEAD（若线上就是这份） | 工作区 |
> |---|---|---|
> | 写入穿透 `WriteThrough` / `writeThroughSignal` | **不存在**（只有旧代码里 coding-only 的 `mirrorCodingWriteToSandbox`） | 存在 |
> | 同步的 `BLOCKED` / `CompareResult` / `StoreOnlyLine`（G4–G7 的信号） | **不存在** | 存在 |
> | 同步判据 | **`Stat().Size == len(data)` 就跳过，否则用沙箱那份 `Put` 覆盖 store**——事故的原始机制 | `size+mtime` → 再比字节 → 分歧则拒绝并报 |
> | 未水合声明的**持久化**（G19） | 不存在（`unhydrated` 只在进程内） | 存在（`sandbox_leases.unhydrated`） |
> | 环境信号的身份文件/配置/cron 采样（G8/G9/G10）、环境快照进收据（G20） | 不存在 | 存在 |
> | 面板删除的路径约定（G21）、同步回写折叠（A）、预览容器寻址（G） | **都是旧的、有缺陷的形态** | 已修 |
>
> **因此"线上现在是什么行为"不能从本文的"已修"推断**，要看 HEAD：
>
> * **事故根因仍在**：宿主写了新内容、而镜像没到（松散会话从不镜像），下一次同步会用沙箱的旧副本覆盖
>   store——**2026-09-17 的事故仍可复现**（HEAD 的判据是"size 相同就跳过"，所以它是间歇的、依赖字节数）。
> * **面板删除仍是静默无效应**（G21 在 HEAD 里原样存在）。
> * **项目会话仍在产生 `<pid>/<chat>/…` 重复副本**（A），而且**还在继续长**——清理脚本应当在 A 上线之后跑。
> * G22（盖章失效）**不属于线上**：HEAD 里根本没有穿透，它是未上线代码里的缺陷，本次已修。
>
> 若线上部署的**不是** HEAD（另一条分支 / 更早的镜像），这张表要用部署位置的实际代码重核——最快的判别法是
> 两条线上探针：① 面板删一个文件，刷新后那一行是否回来；② 面板上传一个文件，同一会话 `exec ls` 是否看得到。
> 两条的答案各自对应上表的一列。

## 0. 分类判据

原则只问一件事：**这次变更发生时，agent 的 `Belief` 会不会与现实脱节，而它无从察觉。**

按"谁触发"分两类，两类的判据不同：

| 类 | δ 的触发者 | 判据 | 天然满足吗 |
|---|-----------|------|-----------|
| **A：被 agent 触发** | agent 自己的工具调用 | 工具结果必须是**完整回执**：不只说"调用成功"，还要包含"世界因此变成了什么" | 08 §2 说天然满足，**但只在这个前提下**——回执包含了变化本身。缺这半句，A 类照样违反原则 |
| **B：触发 agent** | harness（cron / heartbeat / goal / subagent）、用户（web / IM / API / webhook）、外部系统（MCP server、provider、store 的其他写者） | 必须存在 σ，并且在**那个回合真正读的通道**里送达；不能只进 slog | ❌ 默认不满足 |

**第三类**最容易被漏掉：既不是 agent 触发、也不触发 agent 的变更（用户在面板里改配置、
另一个会话或另一个 pod 改了同一个 agent 的文件）。它们对原则而言和 B 类**完全一样**——
只要改变了 agent 的世界就必须可感知，触发者是谁不改变判据。

## 1. 全景表

σ 列写的是"agent 实际读到的那句话"；"出口"列是它从哪个投递点出去的（08 §9.1 的三个出口）。

### A 类：被 agent 触发

| 组件 | δ（世界变化） | σ（agent 看到什么） | 出口 | 判定 |
|------|--------------|-------------------|------|------|
| 只读工具（`read_file` / `list_dir` / `web_search` / `web_fetch` / `knowledge_search` / `memory_search` / `get_billing_usage` / `load_skill`） | 无（不改变世界） | — | — | ✅ 无需信号 |
| `write_file`（store 路由） | store 写入 + 沙箱穿透（docker 上只有一个副本） | **恒为假**的 `[workspace] … is too large to compare … (over 2 MiB)` | 工具结果 | ❌ **G5（P0，所有后端）** |
| `edit_file`（store 路由） | 同上 | 命中时 `[workspace] your write … replaced a different version (N bytes)`；无事则静默 | 工具结果 | ✅ 正确（唯一把 pre-image 传下去的调用点） |
| `apply_patch`（store 路由） | 同上，逐文件 | 同 G5 的假 σ；**永远不会**说"替换了不同版本" | 工具结果 | ❌ **G5 + G6** |
| `exec`（沙箱） | 沙箱 `/workspace` 被命令改写 | post-exec 同步的 `[workspace] the sandbox changed … / NOT synced …` | 工具结果 | ✅ 09/07 已收敛 |
| `exec`（宿主 `host_exec`） | 宿主文件系统的任意路径 | 只有命令的 stdout | 工具结果 | ✅（无副本分歧：宿主 FS 就是那唯一一份；能读回即能确认） |
| `spawn_subagent` / `delegate_task` | 子 agent 在同一 scope 里继续改文件/记忆 | 父回合拿到子任务文本；文件改动走下一次 `exec` 的同步信号 | 工具结果（同步等待） | ✅ 08 §5.1 |
| `message` | 向某个 channel 发出一条消息 | "已发送"的回执 | 工具结果 | ✅ |
| `image_gen` / `tts` | 生成文件并挂到回复上 | 回执含路径 | 工具结果 | ✅ |
| `create_cron_job` / `list_cron_jobs` / `delete_cron_job` | 未来的**自动回合**被登记/撤销 | 回执（job id、表达式） | 工具结果 | ✅ 回执；但见 **G10**（被外部删除时） |
| `update_goal` | goal 状态 / 预算 | 回执；预算耗尽时另有 `BudgetLimitPrompt` | 工具结果 + 回合提示 | ✅ |
| `set_preference` / `set_timezone` | scope 偏好 / `USER.md`（系统文件） | 回执 | 工具结果 | ✅（是它自己写的，C1） |
| MCP 配置工具（`mcp add/remove/…`） | 工具集变化 | 回执 + 下一轮 `[Environment changes …] tools now/ no longer available` | 工具结果 + 回合提示 | ✅ 2026-09-18 |
| 技能安装/生成（`skill install`、SkillsLearner 后台抽取） | 技能集变化 | 下一轮 `[Environment changes …] skills added / removed / changed` | 回合提示 | ✅ 2026-09-18 |
| 记忆写入（memory 工具 / heartbeat 复盘） | `MEMORY.md` 内容变化 | 下一轮 `[Environment changes …] long-term memory was rewritten/created/CLEARED` | 回合提示 | ✅ 2026-09-18 |
| 身份文件写入（`write_file('SOUL.md', …)` 等） | 系统提示词的一部分被改写 | 回执（是它自己写的） | 工具结果 | ✅ 自己写时；**被外部改写时见 G8** |
| `exec run_in_background` / 沙箱后台 job | 进程在工具结果之后继续写文件；它退出也是一次 δ | 文件改动被下一次同步报出；**退出本身不推送**，但 `bash_output` 每次调用**从世界重算**并给出 `[status] exited (code=N)` / `killed` / `lost — the sandbox was replaced` | 工具结果（消费侧下一次读取） | ✅ **pull 形态的 σ**（判据可重算 ⇒ 落点 1，不需要载体；见 §4 G13） |

### B 类：触发 agent

| 触发源 | δ | σ | 出口 | 判定 |
|-------|----|---|------|------|
| 用户消息（web / IM / API / webhook） | 会话出现一条新输入 | 消息本身就是上下文 | 回合输入 | ✅ |
| **cron** 触发回合 | 一次自动回合 | `[Cron Job: name]` 标注来源，留在会话历史 | 回合输入 | ✅ 【码】`internal/cron/scheduler.go:289,463` |
| **heartbeat** 触发回合 | 同上 | `[Heartbeat — ts]` + HEARTBEAT.md 正文 | 回合输入 | ✅ 【码】`internal/agent/heartbeat.go:66-90` |
| **goal continuation** | 目标未完成，自动续跑 | 合成的 goal_context 消息留在历史 | 回合输入 | ✅ 【码】`internal/agent/goal/continue.go:50-68` |
| **子 agent 完成任务** | 父回合等它 | 结果即父回合的 tool result | 工具结果 | ✅ 08 §5.1 |
| 沙箱被替换/超时（provider 侧） | `/workspace` 重建 | `[workspace] the sandbox was REPLACED …`（投递一次） | 工具结果 | ✅ 09 G1/G2 |
| 用户上传文件（`POST /api/n`） | store 多了文件；**活沙箱没有** | **无** | — | ❌ **G7** |
| 用户删除文件（`DELETE /api/n/{path}`） | store 少了文件；**活沙箱还留着** | **无** | — | ❌ **G7** |
| 外部改写身份/系统文件（面板、另一个会话、另一个 pod） | 下一轮系统提示词不同 | **无**（回合级采样只覆盖 skills/tools/memory） | — | ❌ **G8** |
| 外部改写 agent 配置（模型、prompt mode、技能开关…） | 运行期外观不同 | **无**（除非恰好改变了工具名/技能名） | — | ❌ **G9** |
| 外部删除/改写 cron job、`HEARTBEAT.md` | "我将在 X 被唤醒"变成假 | **无** | — | ❌ **G10** |
| MCP server 侧工具列表变化（`notifications/tools/list_changed` 等） | server 能力变了 | **无**（没有任何 notification 处理） | — | ❌ **G11** |
| 自动回合因会话忙被推迟 / 超时丢弃 | 该跑的一轮没跑（调度器**不会补投**：它在触发时就推进了下一次） | 逐条 slog（含 agent/会话/来源/等了多久/文本首行）+ **cron 会在该会话发一条注记** | 该会话的 channel | ✅ 2026-09-18（原 ❌ G12） |
| 上下文压缩 / 工具结果裁剪 / 回合被中断 | 历史被替换 | 摘要与裁剪占位符都明说发生了什么 | 回合输入 | ✅ 08 §5 |
| provider 账号回退（`providerForAgent` 的三条路径落到 shared provider，【码】`provider_fallback_log_test.go:3-10`） | 请求改由另一个上游账号发出（模型名不变） | 只有 slog warn | — | ⚠️ 运营侧事实：不属于 agent 的世界模型，但它印证同一课——"悄悄换了一个东西，只有日志知道" |

## 2. A 类详述

### 2.1 工作区写入：三个工具、一个出口、一个假信号（G5，P0；**2026-09-18 已修**）

**现象（【探】本地已复现）**：在一个真实 `LifecyclePool`（RemoteWorkspace + workspace.Store）
上调用 `write_file('notes.md', 'hello')`，工具结果里带的是：

```
Written 5 bytes to notes.md

[workspace] notes.md is too large to compare automatically, so the sandbox copy was
replaced without checking it (over 2 MiB). If a script edits a file this large, verify
the result inside the sandbox with exec before moving on.
```

一个 **5 字节**的文件被告知"超过 2 MiB"。`apply_patch` 同样。`edit_file` 正常（它静默）。

**并且 docker 后端也一样**（【探】第二组探针：把 executor 换成不实现 `sandbox.RemoteWorkspace`
的"docker 版"，输出逐字相同）。docker 上 `/workspace` 就是宿主目录、**只有一份副本**，
没有任何东西可被"替换后未核对"——这条 σ 在那里连语义都不成立，却照样附在每一次 `write_file` 上。

**成因（两处已被各自测试钉住的行为，组合出来的）**：

1. `sandbox.WriteThrough` 只在调用方给出"沙箱那份应该是什么"（`previous`）时才做比较；
   没给就返回 `Compared=false`（【测】`TestSyncContract_WriteThroughWithoutExpectationClaimsNothing`）。
2. tools 层把 `Compared=false` **一律**解释成"超过指纹上限"：
   `file.go:920` 的 `case !outcome.Compared:` 直接吐那句 "(over 2 MiB)"。
3. 而 `write_file`（`file.go:1080`）与 `apply_patch`（`apply_patch.go:690`）**永远传 `""`**，
   只有 `edit_file`（`file.go:1269`）把自己读到的字节传了下去。

所以 `Compared=false` 被两个完全不同的原因共用（"没给期望值" / "太大读不了"），
出口把其中一个原因当成了另一个。

**为什么这是 P0 而不是"文案问题"**：σ 的价值在于它说的**是真话**。一条恒为假的 σ 比沉默更糟：

- 它教模型一个假事实（"这个大文件我没核对"），而模型的后续行为会依据它做决策（去 `exec` 复核）；
- 它出现在**每一次** `write_file` / `apply_patch` 上，正是 08 §3 的 C3 要避免的"每次都有的噪音"——
  一旦成为背景音，真正的信号（真的替换了不同版本）也会被一起忽略。

**修法（2026-09-18 已落地）**：把那个 bool 换成一组**有名字的事实**（`sandbox.CompareResult`），
每个事实各对应一句真话，出口不再翻译：

| 事实 | 什么时候出现 | 出口（σ） |
|------|-------------|----------|
| `CompareNoSecondCopy` | docker：`/workspace` 就是宿主目录，没有第二份可比 | **静默**（此前发"超过 2 MiB"） |
| `CompareNothingReplaced` | 沙箱那份就是交给它的版本 / 就是正要写的内容 / 读不到 | **静默** |
| `CompareReplacedVerified` | 沙箱那份既不是调用方的期望、也不是新内容 | "替换了一个不同的版本（N 字节）" |
| `CompareReplacedUnchecked` | 沙箱那份与新内容不同，但调用方没给期望值可比 | "沙箱里那份是另一个版本（N 字节），没有可比对的旧副本"——**不再声称"超过 2 MiB"** |

配套 G6：**把 pre-image 真的传下去**——`write_file` 在写之前读一次 store 的旧对象
（`previousStoreVersion`，上限 2 MiB，读不到就不带期望值），`apply_patch` 把 `runApplyPatch`
已经读到的 `old` 随 `plannedWrite` 传下去。于是 `write_file` 第一次**能够**说出
"替换了一个不同的版本"——在此之前只有 `edit_file` 说得出。

**同仪器复验（2026-09-18）**：用 §7 那支探针重跑，同一段代码给出：

```
remote(e2b) write_file : "Written 5 bytes to notes.md"          ← 假信号消失
shared(docker) write_file: "Written 5 bytes to notes.md"        ← docker 上不再自称"超过 2 MiB"
沙箱里先被脚本改过、再 write_file：
  "Written 2 bytes to notes.md
   [workspace] your write to notes.md replaced a different version in the sandbox (12 bytes). …"
                                                                ← G6：write_file 第一次报得出这件事
```

真后端同样复跑过：`FASTAGENT_E2B_LIVE=1 go test ./internal/sandbox/ -run TestE2BLive -count=1`
5/5 通过（含 `TestE2BLiveRepro` 与 `TestE2BLiveWriteThroughReachesSandbox`）。

**第五态（2026-09-18 加，G17/H）**：**部分成功**也要说。项目里一个写入只镜像到"本轮这个容器"、
另一个容器（兄弟 chat 的，或控制台那个预览容器）没更新到——症状是"我改了，预览不动"，而 agent
从工具结果里看不到任何线索。现在 `WriteThroughOutcome.BroadcastFailures` 计数非零时，工具结果会说
`[workspace] your write … landed in this sandbox, but N other sandbox(es) of this project could not be updated — a preview running there may keep showing an older version. Writing the file again retries them.`
（C3：全部成功时依旧静默；【测】`TestWriteFileStatesAPartialProjectMirror`）。

### 2.2 写自己未来的世界

这一族的特点是：**回执只覆盖"调用成功"，真正的后果在下一轮才出现**（工具集多了一个工具、
技能少了一个、记忆被改写、系统提示词换了内容）。它们由**回合级出口**（每轮环境信号）兜底，
因此判定是 ✅，但有一个共同的时序性质值得记住：

| 变更 | 本轮回执 | 下一轮可见性 |
|------|---------|-------------|
| MCP 工具增删 | 有 | 回合信号 `tools now / no longer available` |
| 技能增删改 | 有 | 回合信号 `skills added / removed / changed` |
| 记忆改写 | 有 | 回合信号 `long-term memory was …` |
| 身份文件（SOUL/IDENTITY/USER/AGENTS） | 有 | **没有任何信号**——但它是自己写的，所以 C1 满足；被外部写时见 G8 |
| 偏好 / 时区 | 有 | 无信号（同样是自己写的；影响的是下一轮提示词的日期行/偏好段） |
| goal | 有 | 预算耗尽有 `BudgetLimitPrompt` |
| cron job | 有 | 无信号（见 G10） |

## 3. B 类详述

### 3.1 四类自动回合：都已经有"来源标注"

cron / heartbeat / goal continuation / subagent 的共同点是：**它们让 agent 在一个它没有请求的
时刻醒来**。可感知性由两条保证：①回合本身写进该会话历史（下一次读历史时能看到）；
②输入带来源标注（`[Cron Job: …]`、`[Heartbeat — …]`）。
【码】`internal/agent/admission.go:45` 列出这四个 source，`loop.go:1292` 的
`bus.SourceGoalContext` 决定它是否被当作"合成审计提示"而不进 FTS 索引。

### 3.2 用户/API 直接写 store：唯一一条**不穿透**的写路径（G7；**信号半边 2026-09-18 已落地**）

agent 自己的写路径都会穿透进沙箱（§2.1）；但用户从面板上传/删除文件走的是另一条：

| 动作 | 代码 | 结果 |
|------|------|------|
| `POST /api/n`（上传） | `handleAgentFileUpload`，`internal/setup/handlers_agents.go:1445`（`Put` 在 :1503） | 只 `workspaceStore.Put`，**不碰活沙箱** |
| `DELETE /api/n/{path}` | `handleAgentFileDelete`，`internal/setup/handlers_agents.go:1512`（`Delete` 在 :1534） | 只 `workspaceStore.Delete`，**沙箱那份还在**——**但见下方 2026-09-18 的更正：它当前其实什么都没删掉** |

于是产生一种当前同步规则永远修不回来的分歧：同步的定义域是**沙箱快照**（07 §3.11.3），
所以"只在 store 里"的文件既不会被推回沙箱，也不会被删除，而 agent 对这一切**零信号**。
分歧在两条读路径上会**显形为矛盾**：

- **上传**：`read_file` 命中 store，读得到；`exec` 里的脚本看不到（沙箱里没有）。
  同一路径，两条通道给出两个世界。
- **删除**（**2026-09-18 更正**）：上面这条原先写的是"store 那份删掉后 `read_file` 回落到沙箱"——
  实测**不成立**，因为它假设了 store 的删除成功。实际上**面板删除当前是静默无效的**：
  列表返回的是 **agent 相对、带作用域前缀**的路径（`sessions/<sid>/f`、`projects/<pid>/x`，
  `handleAgentFileList` 的注释明说这是给下载端点用的形状），面板把这条路径原样 DELETE 并附带
  `?sessionId=<chat>`，而 handler 又把这两个作用域参数拼了一次（`Delete(id, "", <chat>, "sessions/<sid>/f")`）
  ⇒ 键多了一层前缀（S3：`<agent>/sessions/<chat>/sessions/<chat>/f`）⇒ 目标不存在；而两个后端
  对"删不存在的目标"都返回成功（S3 幂等、LocalFS 显式吞掉 `os.ErrNotExist`）⇒ 接口 200、
  UI 以为删掉了、刷新后那一行又出现（列表读的是 store，store 里那份从未动过）。
  **对照**：下载端点传的是空作用域（`Get(agent, "", "", path)`，`handlers_agents.go:1413`），
  所以下载一直是正常的——两个端点对"这条路径怎么解释"的约定不一致，这才是缺陷本身。
  于是"回落沙箱"那条只在 store 那份**真的**被删掉时才会发生（例如脚本/工具删了它，或 Fix 0 落地之后）。

这条与事故同族但方向相反：事故是"沙箱的旧副本盖掉了 store 的新版本"，这里是
"store 的新文件永远进不了沙箱"。它不造成数据丢失（store 是权威），但它让两副本不一致，
而 agent 无法察觉——正是原则要禁止的状态。

> **范围修正（2026-09-18 重盘）**：上面两条**只适用于面板上传/删除**（`POST`/`DELETE /api/n`，
> `handlers_agents.go:1503/1534`）。**附件是另一条路**：`WriteSessionAttachments`
> （`agent/attachments.go:118-130`）把文件**三处齐写**——宿主目录、workspace store、以及**活沙箱**
> （`ex.WriteFile("/workspace/"+name)`）。所以"所有用户文件都不进沙箱"是错的；准确说法是
> **"面板上传/删除是唯一一条不穿透的写路径"**。全部 store 写者见 01 §3.1.1。
>
> **补充（2026-09-18）**：这条分歧现在有**两个**发声时刻，且共用同一句话（`sandbox.StoreOnlyLine`）：
> `list_dir`（agent 主动列举时，G7a）与**每次 exec 之后的同步**（G4）——同步那侧靠"沙箱快照之外再 List 一次 store"补上遍历定义域的盲区。两者都**不声称归属**。

**落点（2026-09-18，选了信号这一半）**：G7 有两个选项——给这条路也加穿透，或把分歧本身变成信号。
这里实现的是后者，因为前者要改"用户上传的文件到底落在哪"这一产品语义，而且上传时沙箱可能根本
不存在（不该为一个不存在的沙箱承担新失败模式），需要你单独决策；信号则不动任何写路径。

具体：`list_dir` 读的是 **store**，而 `exec` 读的是**沙箱**，两者打架的地方就是 agent 最容易被误导
的地方。所以 store 分支在给完清单后，问一次沙箱（`find /workspace -type f -printf '%P\n'`，仅当
executor 声明 `RemoteWorkspace` 时；docker 上只有一份副本，不提问），把"store 有、沙箱没有"的路径
附在清单后面：

```
f shared.csv (4 bytes)
f uploaded.csv (4 bytes)

[workspace] 1 path(s) listed above are in the workspace store but NOT in this sandbox, so anything run
with exec will not find them: uploaded.csv. A sandbox that is already running is not updated by uploads —
if a script needs one, read it and write it again (read_file + write_file copies it in).
```

四条性质各有一条测试：**命名差异、不误报共有路径、双方一致即静默（C3）、沙箱问不到就不说话**
（不能落地的 σ 比沉默更糟）——`store_only_signal_test.go`。代价是每次 `list_dir` 多一次 exec
（agent 主动列举时才发生，不是每次 exec）。**剩下的一半（让上传真的进沙箱）仍开放**，见 §4。

### 3.3 外部改写 agent 的世界（G8 / G9 / G10 已修）

回合级采样（`env_changes.go` 的 `envSnapshot`）原先只采集**技能、工具名、记忆 hash**。
不在这三样里的、由外部引起的变化都没有 σ：

> **2026-09-18 落点（G8、G10 已修；G9 也已修，见下一段）**：采样面加了三项——
> `envSnapshot.identity`（`SOUL/IDENTITY/USER/BOOTSTRAP/AGENTS/HEARTBEAT/TOOLS` 七个身份文件的
> 内容指纹，与 `memoryHash` 同构；**只说改了哪个文件，不说内容**）与
> `envSnapshot.config`（`model` + `prompt_mode`）。
> 以及**定时任务清单**（`envSnapshot.cron`，只指纹 id/name/schedule/type/enabled——
> `LastRun`/`NextRun` 是调度器每 tick 都会改的记账字段，纳入它会让信号每轮都响，正好违反 C3）。
> G8、G10 因此关上。
>
> **G9 的补法（同日第三轮起）：凭空造一份基线，不如用已经存在的那一份。**
>
> 这条 duty 的形状是：配置变更**靠重建 Agent 生效**，所以"应该报告这次变更的那个 tracker"恰好
> 被它要报告的那次变更销毁了——在 08 §2.2 的 O4 意义上，σ 不是送丢了，而是**根本产生不出来**。
> 第一版补法给它加了一份专用基线（`configs_kv` 的 `cfg_seen` 行）；那能工作，但它是**同一个事实的第二份拷贝**：
> "这个对话上一轮以什么配置在跑"这件事，回合收据里本来就有——`session.Session.Append` 在每个
> assistant 消息上盖 `provider`/`model`（`session_messages` 列，早已存在且双向映射）。
>
> 第二版把 before 改成"读同一条收据上的一个指纹字段"（`run_config`）——但同一轮紧接着发现，
> 同一个位置可以装**整张世界快照**，而其余五族正卡在同样的进程内基线上（见下面 G20 那段）。
> **最终形态（第三版，也就是现在的代码）**：`provider.Message.Metadata` 的 `run_receipt`
> （`session.RunReceiptMetadataKey`）装整张 `envSnapshot` 的 JSON；agent loop 每轮从
> `signalEnvironmentChanges(...)` 拿回文档并 `SetRunReceipt(...)`（写在 `Append` 这唯一入口上），
> 下一轮由 `envBaselineFromReceipt(history)` + 纯函数 `renderEnvDelta(prev, seen, cur)` 取 before。
> 于是：
>
> | 关注点 | 结果 |
> |---|---|
> | 新增存储 | **零**（没有新 kind、新表、新列；收据本来每轮都在写） |
> | 新增写路径 | 零（复用 `Append` 这一个 assistant 消息的唯一入口） |
> | 跨实例/跨副本/重启 | ✅ 收据在 `session_messages` / `sessions.messages` 里，与实例无关 |
> | 清理 | 零（会话删了行就没了；不再有"删会话留下 `seen:` 残行"这件事） |
> | 语义 | 更准：before 是"**这个对话确实以什么在跑**"，不是"某个进程某轮采样看到什么" |
> | 取不到时 | 沉默：首次回合、历史被压缩重写、盖戳之前写下的旧行，一律"未采样"（不猜） |
> | 覆盖面 | 五族 + 配置一次到位（G20），不再有"只搬了一个字段"的中间态 |
>
> 顺带修掉一个真 bug：`StoreAdapter.GetSession`（blob 路径）在映射时漏掉了 `Provider`/`Model`，
> 而 archive 路径（`providerMessageFromStored`）有——同一行两种读法给出不同结果，正是"收据读不完整"
> 的来源。已补齐，并由 `internal/session/run_receipt_test.go` 的**重新加载后仍然读得到**钉住。
>
> **G20（同轮推广）：整张快照进收据，`envTracker.last` 删除。** 配置那一族之所以能用"落点 2"，
> 是因为收据已经在写；其余五族（技能 / 工具名 / 记忆 / 身份文件 / cron）的基线本来留在
> `envTracker.last` 这张**进程内**表里，所以 **pod 重启或热重载**后首回合依旧沉默——同一个 O4
> 形状，只是触发条件换成"需要一次实例更替"。既然收据每轮都在写，就没有理由只搬一个字段：
>
> | 项 | 改动 |
> |---|---|
> | 载什么 | `provider.Message.Metadata["run_receipt"]` = 这一轮**开始时**的整张 `envSnapshot`（JSON：skills / tools / memory / identity / cron / config） |
> | 谁写 | agent loop 每轮调 `signalEnvironmentChanges(...)` 拿回文档，随 `SetRunReceipt` 盖在该轮的 assistant 消息上（`Append` 是唯一入口） |
> | 谁读 | 下一轮 `envBaselineFromReceipt(history)` → `renderEnvDelta(prev, seen, cur)`（**纯函数**） |
> | 删掉 | `envTracker` 类型、它的 `last` map 与 mutex、`Agent.envTracker` 字段、以及为配置单开的那套 `configWas` 分支——**一个基线源，五个族受益** |
> | 取不到 | 全程沉默（首回合 / 历史被压缩重写 / 旧行）——与"首次观察不算变化"同一条规则 |
>
> 顺带修掉一个**交付缺口**：此前三个回合入口里只有 `HandleMessage` 调用了采样
> （`HandleWebChatStream` 是它的委托者，所以 Web 聊天没事），`handlePlanMode` 与 API 的
> `HandleMessageStream` **既不采样也不盖戳**——环境信号在那两条路径上根本不存在，
> 而且它们的回合也不会更新基线。现在三条路径都采样、都盖戳（[10 §4](#4-缺口清单按代价排序) G20）。

- **身份/系统文件**（SOUL / IDENTITY / USER / AGENTS / HEARTBEAT 的内容）：每轮现读现拼进提示词，
  内容变了、agent 不会被告知（G8）；
- **agent 配置**：模型、prompt mode、技能开关等；`reload_epoch.go` 只让**运行期**重建
  UserSpace（用户级的跨副本失效广播）——它不自己发声，但**重建这件事恰好是**"持久基线还要不要"
  的分界线：现在基线与实例无关，所以重建反而成了这句 σ 能被说出来的前提（G9，上面那段）；
- **cron job / HEARTBEAT.md 被外部删除或改写**：agent 的"我将在 X 被唤醒"变成假事实（G10）。

注意 G8/G9 与"工具集变化"的区别：工具集变化恰好被采样到了，所以它是 ✅——
这不是设计得更好，而是**采样面碰巧覆盖了它**。采样面之外的同类变化全都没有信号，
这正是 08 §6 清单第 4 条（"信号何时送达"）之外的第二类漏洞：**采样面本身要有判据**。

### 3.4 MCP server 侧的变化（G11；**stdio 侧 2026-09-18 已修**）

MCP 工具在**构造 agent 时一次性注册**（【码】`loop.go:472-484`：`mcp.NewManager` → `ToolDefs()`
→ 注册闭包），所以 server 侧的变化只有两条路能让它变成真的：重建 agent，或让 registry 支持运行时增删工具。

**调研结论（先把"通知到底能不能到"钉死）**：

| 传输 | 通知能到吗 | 证据 |
|------|-----------|------|
| **stdio** | **能，但被丢掉** | `stdio.go` 的读循环逐行扫 stdout，只认 `resp.ID == 请求id` 就返回，其余一律 `continue`——server 发来的 `notifications/tools/list_changed`（有 method、无 id）**被解析出来后直接丢弃**。请求 id 从 1 起（`NewStdioClient` 里 `nextID: 1`），所以通知的 id=0 不会与任何请求撞号 |
| **HTTP** | **不能，压根没有线** | `HTTPClient.sendRequest` 是"一请求一 POST"，只把响应体当单个 JSON-RPC 响应解析；既不消费 Streamable HTTP 的 SSE 流，也不发 `Accept: text/event-stream`。所以 server 在 HTTP 上没有推送的地方——这不是"我们丢了它"，是"它没地方来" |

**落点（2026-09-18，stdio 侧）**：捕获 → 交给 manager → 闸门 → **复用 `mcp add` 已经在用的重建路径**：

```
stdio 读循环识别 method 消息   →  StdioClient.SetNotificationHandler
    →  Manager.wireNotificationSink（HTTP 不接线，不假装有）
        →  notificationGate：每 server 30s 内最多处理一次
            →  agent 层：method == notifications/tools/list_changed
                 →  ag.mcpConfigNotify(owner, agent)  ← mcp add/remove 用的同一条路
                     →  下一次回合重建 userspace ⇒ 重新 tools/list ⇒ 工具集变化
                          ⇒ **回合级环境信号自动说出 `tools now available / no longer available`**
```

注意最后两步：**信号不需要新机制**——08 §9.1 的"每类别一个出口"在这里兑现了第二次，
工具集差异本来就被采样，缺的只是"让差异真的发生"。

闸门是必要的：闸门后面是"重建一个用户的整个 userspace"。一个反复宣告变化（或有 bug）的 server
不该能把这件事变成循环——被限流时记一条 warn，而不是静默。

**如实记下的两个边界**：
1. **HTTP 那半边没修**：要让 HTTP 侧也能收到，得实现 SSE 流（真功能），或者定期 re-list（每回合一次网络往返）。
   现在 HTTP 侧的行为是"变化要等下一次 reload"，与调研前一致，没有变坏；
2. **`notifications/initialized` 两个传输都没发**（全仓搜 `initialized` 无命中）：MCP 规范要求 client 在
   initialize 之后发这条通知。现有 server（QC/Quandora）不要求它，所以今天不致命——但它可能是某些 server
   开始推送通知的前提。记为 **G15（开放）**：先不动，因为它会改变与真实 server 的握手行为，需要真机验证。

### 3.5 自动回合被推迟/丢弃（G12；**2026-09-18 已修**）

自动来源（cron / heartbeat / goal / subagent）撞上正在跑的回合时，会被 park 到会话空闲
（【码】`internal/gateway/deferred_turns.go:14-40`），`maxWait` 之后**丢弃**，只留一条 slog。
被丢弃的一轮里，agent 永远不会知道"本来有一次我应该醒来"。
对 cron 与 goal 有天然补偿（下个周期还会再来），对 heartbeat 则是一次静默的漏拍。

**原来的缺口其实更基础**：丢弃时只打了一行 `count=2` 的 slog——**连是哪一次、哪个会话、什么来源都没有**，
事后无法定位（这正是 08 §6 第 4 条说的"信号产生了但没有投递点"的运维版）。

**落点（2026-09-18）**：先分清"谁的期望被打破"——

| 来源 | 谁的期望 | 处置 |
|------|---------|------|
| **cron** | **用户**（"9 点提醒我"是用户提的） | 逐条 slog 带全信息（agent / channel / chat / source / 等了多久 / 触发文本首行），**并且在该会话里发一条注记**：`[scheduled task did not run] The scheduled task "morning-brief" was due, but this conversation stayed busy for over 5m0s, so its turn was dropped. Set it up again if you still need it.` 它走用户已经在的那个 channel（IM 就发到 IM），**不启动回合** |
| heartbeat / goal continuation | harness 自己（下个 tick / 下次 PostTurn 会再来） | 只逐条 slog；对用户发声属于 C3 的噪音 |
| subagent | 父回合（`spawn_subagent` 是同步等待，任务由 task queue 派发） | 只逐条 slog；同步等待让"被 park 到超时"极少发生 |

写这条注记的通道是**有界的**（250 ms），出站路由卡住时记一条 warn 而不是拖住 drain 循环——
可观测性的补救不该自己变成新的阻塞点。

## 4. 缺口清单（按代价排序）

> 三套形式化的分工与符号总表见 [00-formal-systems.md](./00-formal-systems.md)；
> 「义务」列按 08 §2.2 的 O1–O5 归一：**O1** 该产生而没产生 / 说了假话；**O2** 产生了却没落在 D₁ 或 D₂；
> **O3** 取走时机错（要求消费方订阅）；**O4** 送达前丢了（进程内状态）；**O5** 无变化也说或打断推理。
> 「—」表示这一条不是投递义务的违反（是别族问题，另注原因）。

| # | 义务（08 §2.2） | 缺口 | 触发者 | 代价 | 修法方向 |
|---|------|------|--------|------|---------|
| ~~G5~~ | O1 | ~~`write_file` / `apply_patch` 的 σ 恒为假（over 2 MiB），docker 上也照发~~ | agent 自己 | **P0 已修（2026-09-18）**：`CompareResult` 四态 + docker 静默，同仪器复验见 §2.1 | — |
| ~~G6~~ | O1 | ~~`apply_patch` / `write_file` 永远不会报"替换了不同版本"~~ | agent 自己 | **P1 已修（2026-09-18）**：`previousStoreVersion` + `plannedWrite.previous`；`write_file` 现在报得出 | — |
| ~~**G7a**~~ | O1 | ~~分歧不可见~~ | 用户 | **P1 已修（2026-09-18）**：`list_dir` 现在把"store 有、沙箱没有"的路径点出来（含原因与出路），docker 不问、问不到不说 | — |
| ~~**G7b**~~ | —（写路径对称性，属 Cordis 前置条件） | 上传/删除不写进活沙箱 | 用户 | **上传半边已决策（2026-09-18）：不做穿透 = 选项 a**。产品语义 = **面板是"文件库"**，"上传后下一次 `exec` 立刻 `ls` 得到"**不是**要求；代价与补救都已由既有信号说清（`list_dir` 与每次 exec 后的同步都会点名"store 有、沙箱没有"，并给出 `read_file`+`write_file` 的搬法）。澄清：在 LocalFS+docker 上它天然立刻可见（同一棵树），**不做**为把它藏起来而改架构——准确表述是"不承诺立刻可见，也不阻止（由后端决定）"。**删除半边仍未修，且必须修**：见下 | 删除半边 = 待决策（d1 穿透删除 vs d5 删除墓碑），见 [05 §8](./05-remediation-plan.md) |
| ~~**G21**~~（2026-09-18 新发现；与 G7b 同族） | —（路径/作用域约定不一致；属 F1 的"同一路径一个键"） | ~~面板删除静默无效：列表返回带作用域前缀的 agent 相对路径，面板原样 DELETE 又附带 `?sessionId=`，handler 再把作用域拼一次 ⇒ 键多一层前缀 ⇒ 目标不存在；两个后端对"删不存在的目标"都返回成功 ⇒ 200 + UI 以为删掉了 + 刷新后那行又回来~~ | 用户 | **已修（2026-09-18）**：① **Fix 0** —— 删除改用与下载同一个约定（路径自带前缀时按 agent 相对解释，`Delete(agent,"","",path)`；旧的无前缀+query 形状继续可用，`sandbox.StorePathScope` 是唯一判定点）；② **d1** —— 同一次删除也把**活沙箱**里那份删掉（`Gateway.RemoveWorkspaceFile` → `sandbox.LiveWorkspaceFileRemover`，经 `LiveExecutorPool` **只找活实例、绝不建实例**），否则下一次同步会把它写回（真机两半都钉住）。失败时返回 `sandboxRemoved:false` + warning，不假装干净 | — |
| ~~G8~~ | O1 | ~~身份/系统文件被外部改写无信号~~ | 用户 / 其他会话 / 其他 pod | **P1 已修（2026-09-18）**：回合级采样加入 7 个身份文件的指纹；只报文件名、不报内容 | 见 §3.3 的落点（`identitySampleFiles`） |
| ~~**G9**~~ | O4 | ~~agent 配置被外部改写无信号；基线随实例消失，而配置变更恰恰靠重建实例生效~~ | 用户 | **已修（2026-09-18）**：采样加入 `model` + `prompt_mode`；before 不再自造，而是**读对话自己的回合收据**——零新增存储、零新增写路径、天然跨实例与跨副本。重建后的**新实例首回合**即可说出 `my configuration changed: <旧> → <新>`；取不到收据即沉默。收据后来承载整张快照（见 G20） | —（G20 已把同一机制推广到其余五族） |
| ~~**G20**~~ | O4（G9 的推广） | ~~技能 / 工具名 / 记忆 / 身份文件 / cron 五族的基线仍在进程内 `envTracker.last`：pod 重启或热重载后首回合沉默；且 `handlePlanMode`、API 的 `HandleMessageStream` 两条回合路径既不采样也不盖戳~~ | 用户 / harness | **已修（2026-09-18）**：收据改为承载**整张 `envSnapshot`**（metadata `run_receipt`），`envTracker` 类型连同 `last` map / mutex / `configWas` 分支一起删除，判定变成纯函数 `renderEnvDelta(prev, seen, cur)`；三条回合入口统一采样 + 盖戳 | 见 §3.3：这是"判据复用既有耐久记录"这条落点的第一次完整落地；仍存的边界只有"取不到收据 ⇒ 沉默"（首回合／压缩后） |
| ~~**G19**~~（2026-09-18 新发现） | O1 | 未水合声明（Policy C）的判据 `workspaceUnhydrated` 只在**创建它的那个进程**里：`adoptFromLease` 明确不重放 hydrate（"the creating pod hydrated the same scope"），于是换手后的副本 flag 恒为 false ⇒ **声明消失**，agent 把空 `/workspace` 读成"文件没了" | 沙箱生命周期族 | P1 **已修（2026-09-18）**：该位随**实例**落进 `sandbox_leases.unhydrated`（`SetSandboxLeaseUnhydrated`，owner+sandbox_id CAS，Acquire/Replace 时归零 ⇒ 不会钉到继任实例上）；采纳时 `ex.setWorkspaceUnhydrated(rec.Unhydrated)` 读回，创建/替换发布实例时 `publishUnhydrated` 写入 | 真机 E2E：pod A 用坏 store 建实例（列表失败）→ pod B 采纳同一实例 → 仍报未水合；反证（去掉采纳读取）该 E2E 变红 |
| ~~**G10**~~ | O1 | ~~cron job / HEARTBEAT.md 被外部改删无信号~~ | 用户 | **P2 已修（2026-09-18）**：定时任务清单进回合级采样（`scheduled jobs added / changed / no longer exist: <name>`）；`HEARTBEAT.md` 的内容变化本就被 G8 的身份文件指纹覆盖；读不到清单时声明"读不到"，**不谎报删除** | — |
| ~~G14~~ | —（单一来源，不是投递义务） | ~~`HEARTBEAT.md` 有**两个来源**：提示词读 store（`loadFileForUser`），heartbeat 触发却只读 `<home>/HEARTBEAT.md`~~（`heartbeat.go`） | 用户 / 运维 | **P2 已修（2026-09-18）**：`loadHeartbeatTasks` 改走与提示词**同一个解析器**（`ctxBuilder.loadFileForUser("HEARTBEAT.md", ownerUserID)`，store 优先、磁盘回落）；owner 正是该回合 `chatterUserID` 对 `SourceHeartbeat` 的解析结果，所以"看到的"与"触发的"必然同一份。没有 ctxBuilder 的形态（嵌入式/CLI）保持原有磁盘读法 | — |
| ~~**G11**~~ | O1 | ~~MCP server 侧通知被丢弃~~ | 外部 server | **stdio 侧 P2 已修（2026-09-18）**：捕获 → 闸门（每 server 30s）→ 复用 `mcpConfigNotify` 重建 → 回合级工具集信号自动报出；**HTTP 侧仍无通知通道**（见 §3.4 的两个边界，需要 SSE 或定期 re-list 才能补） | — |
| **G15** | —（协议合规，不是投递义务） | `notifications/initialized` 从未发送（stdio 与 HTTP 都没有） | — | P3：规范要求 initialize 之后发；现有 server 不要求，可能是某些 server 开始推送通知的前提 | 补发这条通知，但必须先在真机（QC / Quandora）上验证握手不受影响 |
| ~~G16~~ | —（setup API 的遮罩写回，非投递义务） | ~~技能密钥被自己的遮罩覆盖~~（2026-09-18 死码扫描发现） | 运维面板 | **P1 已修（2026-09-18）**：规则收成**一个家** —— `mergeSkillEntry`（条目级）+ `mergeSkillEntries`（补丁级），被**两条**写入路径共用：全局 `skills.entries`（namespace 扫描前先与库中现值合并）与 per-agent 覆盖行（`scope.SettingInto` 取现值再合并）；providers/channels 既有的内联守卫**保持不动**（请求形状不同，等第三个变体证明同一缝再抽）。实现中撞到一个真坑：JSON 解码器**复用**（不替换）map，所以"覆盖前快照"必须**深拷贝**（`cloneSkillEntries`），否则比值比的是被就地改写的自己——第一次接线正是这样悄悄保留了遮罩 | — |
| ~~**G12**~~ | O2 | ~~自动回合被推迟/丢弃只有 slog~~ | harness | **P2 已修（2026-09-18）**：丢弃逐条带全信息（原来只有 `count=N`）；用户创建的 cron 额外在该会话发一条注记（有界发送，不启动回合）；harness 自己的来源只记录，不打扰用户 | — |
| ~~**G13**~~ | O3（不是 O2） | ~~后台 shell / 沙箱 job 结束不推送~~ | agent 自己 | **改判：不是缺口（2026-09-18 形式化复核）**。δ = 进程退出；σ **存在且每次读取从世界重算**：`bash_output` 返回 `[status] exited (code=N)`（`killed` / `lost — the sandbox was replaced` 同样由现场推出，[sandbox_background.go](../../internal/agent/tools/sandbox_background.go) 第 350–375 行）。按 08 §2.2.2，**D₁ 只在调用期间存在**，所以"空闲时不推送"不等于"没有投递点"——投递点是消费侧那次读取本身（O3）。判据可重算 ⇒ 三落点里的**落点 1**，不需要任何载体 | ⛔ **不要实现"下一个工具结果附'后台 X 已退出'"**：那要引入一张进程内的"已退出但还没报告"集合，正好是 O4 形状（实例一换就丢），而这条事实本来就能重算 —— 用一次新的进程内状态换一个已经可达的 σ，是净亏 |
| ~~**G18**（= 01 §8）~~ | —（实体不变式：一条路径一个键，非投递义务） | ~~`apply_patch` 用 `r.sessionID` + 原样路径写 store，而镜像与另两个工具用 `scopeSessionID()` + `wsPath()` ⇒ 一次写入自相矛盾：store 落 A 键、镜像落 B 路径，一份文件两个键~~ | agent 自己 | **P1 已修（2026-09-18）**：6 个触点（host 3 + 沙箱 3）统一到同一解析；沙箱模式的 `apply_patch` 同时补上逐文件穿透（此前完全不调用镜像）。单测 3 + 1 条、真机 E2E 1 条，三条都做过反证（改回旧写法即变红），详见 01 §8.1 | — |
| ~~**G17**~~ | —（作用域不变式：同一族，比 G18 低一层） | 项目里"一个文件树、多个容器"的可见性：项目会话的容器是**每 chat 一个**（有意为之：并发 chat 不共享 shell），而预览的 dev server 只跑在其中一个里 ⇒ ① 控制台起的预览用的容器（`agent:p:<pid>`）**agent 的回合永远不用** ⇒ 写入永远到不了它；② 兄弟 chat 改了文件，dev server 那个容器收不到（docker 靠 bind mount 天然没有这个问题） | agent / 用户 | **已决策 + 已修（2026-09-18，方案 G+H）**：**G** = 预览容器统一按项目寻址（`previewSandboxSession`：有项目就 `session=""`，两个入口从此同一个容器，一个项目一个预览）；**H** = 写入与删除**广播到项目内所有活容器**（`LiveProjectExecutors` + `mirrorToProjectPeers`/删除扇出）——把 docker 的挂载语义在云后端显式做出来，**保留"每 chat 独立 shell"**。部分失败会给出 σ（见 §2.1）。真机：同一项目两个容器，A 写 → B 读得到；A 删 → B 也没了且不会被 B 的同步复活 | **A 也已修（同日决策）**：`syncStoreScope` 让回写在项目会话里折叠到项目根（与 hydrate、与文件工具同一个键）⇒ 不再产生 `<项目>/<会话>/…` 副本，`exec` 新建的文件立刻可被 `read_file`/`list_dir` 看见。**未做迁移**：折叠前已产生的副本仍在库里（不再刷新、也无人清理）——一次性清理见 [05 §6](./05-remediation-plan.md) 的 `scripts/workspace_project_chat_duplicate_cleanup.py`（只在「同样字节在项目根另有存活」时才列入删除）。：无副本、exec 产物落项目根且工具可见、沙箱改既有路径仍被拒（且拒绝指向工具读的那个键） |
| ~~**G22**~~（2026-09-18 实现中发现） | —（作用域不变式，同一族的**第三处**） | 写入穿透的 **mtime 盖章**用**沙箱作用域**查 store（`Stat(sc.agentID, sc.projectID, sc.sessionID, storeKey)`），而工具在 coding-root 项目会话里写的是**项目根**（`session=""`）⇒ 项目会话里这次 Stat 永远 miss ⇒ **盖章静默不发生**。代价：对账拿不到"size+mtime 相同"的廉价判据，每次同步对这种路径回落到字节比较（`equalToStore`），**功能不受损** | agent / 沙箱 | **已修（2026-09-18，同一轮）**：写入方把 store 作用域一起交下来 —— `WriteThroughScope(storeScope, storeKey, sandboxPath, content, previous)`（`sandbox.StoreScope`），盖章与 H 的广播份都用**调用方声明的那个作用域**，不再从容器推断。**实测**（真机 E2B，`TestE2BLiveSyncReadsNoBodiesForStampablePaths`）：一次同步里这条路径的**整对象读取 1 → 0**（stats 仍 2 次）。单测 `TestWriteThroughStampsWithTheStoreScopeItWasGiven` 钉住“盖章用的是被声明的作用域”；**反证**：把 pool 改回用沙箱作用域查 store → 立刻红。**为什么只有性能影响也做**：这条缝（store 作用域跨层传给沙箱层）在同一轮已产出三处缺陷（G21 删除、G17/A 同步、本条），满足“投资边界要有 3+ 次历史变更”的判据；而修法是把“猜”换成“传参”（端口修正），不新增机制 |
| ~~**G23**~~（2026-09-18 评审发现；**同日已修**） | —（作用域不变式，同一族的**第四/第五处**：同一条规则被写成多种表达式） | ~~「项目会话 ⇒ 键落项目根」被写在**三处、用两种判据**：agent 侧 `Registry.scopeSessionID()` 用 `codingRootScope`（= `a.projectRuntime != nil && projectID != ""`），沙箱侧 `syncStoreScope()` 用 `projectID != ""`，面板侧 `StorePathScope()` 用"路径带不带作用域前缀"~~ 顺着查还发现**第五处**：**布局表**（`pid/sid` → 目录）在 `LocalFS.scopeDir` 与 `S3.key`/`S3.scopePrefix` **各写了一遍** | agent / 面板 / 沙箱 / 两个 store 后端 | ~~今天不可达，所以不是缺陷；但这条等式是"靠接线成立"的~~ **P3 已修（2026-09-18）**：规则收进 [`internal/workspace/scope.go`](../../internal/workspace/scope.go) 的两个纯函数 —— `ScopeSegments`（布局表，LocalFS + S3 共用）与 `WriteScope`（写者折叠，文件工具 + 沙箱回写共用）；`Registry.codingRootScope` / `SetCodingRootScope` 整个删除，`sandbox.StoreScope` 改成 `workspace.Scope` 的**类型别名**（端口不再自带第二份事实）。**行为差（唯一一处）**：折叠判据从"有运行时且在有项目中"变成"在有项目中"，两者只在"有项目、但没有 runtime manager"的部署里不同 —— 而 `cmd/fastclaw/main.go` 无条件构造并接线 runtime manager，那种部署里项目本身也建不出来 | 单测：`go test ./internal/workspace/ -run 'TestScopeSegments\|TestWriteScope\|TestAWriterScope'`（布局表、写者规则、两者描述同一文件系统）、`go test ./internal/sandbox/ -run 'TestLayoutWriteScopeAndParserAgree\|TestProjectWritersAndTheSyncShareOneScope\|TestAProjectChatSubdirKeyIsItsOwnPath'`（布局/写者/面板解析三者一致；以及"项目 chat 子目录的键是另一个对象"这条被钉住）、`go test ./internal/agent/tools/ -run TestScopeSessionIDCollapsesInsideAProject`。**反证**：把 `syncStoreScope` 改回"不折叠" ⇒ `TestProjectWritersAndTheSyncShareOneScope`、`TestSyncWritesBackToTheProjectRootNotTheChatSubdir`、`TestSyncScopeEqualsHydrateScopeForProjects` 三条变红；把 `scopeSessionID()` 改回 `r.sessionID` ⇒ `TestScopeSessionIDCollapsesInsideAProject`、`TestApplyPatchUsesTheSameStoreKeyAsWriteFile` 变红（两条都实测过） |
| G1–G4 | G1/G2 = **O2**（原无投递点，已修）；G3 = **O4**（进程内队列，已修）；**G4 = O1（2026-09-18 已修到"事实已声明"为止）**：同步里加一次 store List，把"store 有、沙箱没有"的路径报出来（与 G7a 共用同一句话 `sandbox.StoreOnlyLine`）；**归属（"沙箱删的"还是"后来上传的"）＝ 已决策不做**（2026-09-18，选项 a）：后果已送达，清单只买因果，且"交付清单"要在每次 hydrate 落上千行、"写者清单"要动 6 条写路径 —— 见 [05 §8](./05-remediation-plan.md) 决策记录 | 沙箱生命周期族（重建、删除） | provider / harness | 见 09 | 见 09 §6 |

> **G22 的边界（2026-09-18 实测）**：把穿透盖章**整个移除**后，`TestE2BLiveSyncReadsNoBodiesForStampablePaths`
> 仍然读到 **0** 个整对象 —— 判据的 ±1 秒容差（`sameVersion`）把"store 写与镜像写落在同一秒"这件事盖住了，
> 所以那条读数是**测量**而非反证。盖章真正的钉法是单测 `TestWriteThroughStampsTheSandboxCopyWithTheStoreTime`
> （钉住命令里的那个时刻）与真机 `TestE2BLiveHydrateKeepsStoreStamp`（[11 §10](./11-change-register.md) 的 10-4/10-5/10-8）。

## 5. 结论

1. **A 类的问题不是"缺机制"，而是"σ 说假话"。** G5/G6 都在既有出口内部，
   不需要新通道，只需要让出口的两句话各自成立——`Compared` 一个字段扛了两种含义，
   是这次审计发现的最具体的缺陷。
2. **B 类的缺口全部指向同一个出口，而扩采样确实够用（已兑现两条）。** G8、G10 都按这个方向修完：
   身份文件指纹进回合级采样，出口数仍是三个（08 §9.1）。G9 只兑现了一半，且**不是**因为少写了两行，
   而是因为配置变更会重建 Agent——in-process 的 tracker 随实例消失，跨重建的变更需要持久基线或
   reload 时的显式声明。这条要如实留在开放项里，而不是记成"采样面问题"。
3. **G7 被拆成两半做，且这个拆法本身是结论。** 可观测性那一半（G7a，让分歧被说出来）已经落地，
   没动任何写路径；剩下的 G7b（让上传真的进到活沙箱）是**写路径对称性**的产品决策，
   不是本原则能替产品定的。把"必须修"拆成"能修的"与"要决策的"，比把两者混在一行里更有用。
4. **原则的适用范围比"文件同步"大得多**：本次审计里违反原则的行，一半与文件无关
   （配置、cron、MCP 通知、回合调度）。这印证了 08 开头那句：
   文件同步只是这条原则的第一个应用场景。

## 6. 复核方式

| 结论 | 怎么独立复核 |
|------|-------------|
| G5（已修） | §7 的探针重跑：e2b 与 docker 两种形状都不再产生任何 `[workspace]` 行；`go test ./internal/agent/tools/ -run TestWriteFile` |
| G6（已修） | 同探针第三段：沙箱先被脚本改过时，`write_file` 的结果含 "replaced a different version (12 bytes)" |
| G7a（已修） | `go test ./internal/agent/tools/ -run TestListDir` 四条；或在真环境上传后再 `list_dir`，看清单后的 `[workspace]` 行 |
| G7b | —（写路径对称性，属 Cordis 前置条件） | 对活沙箱 `POST /api/n` 上传一个文件，然后 `exec ls /workspace` 与 `read_file` 对比——分歧仍在（现在只是被说出来了） |
| G8（已修） | `go test ./internal/agent/ -run TestEnvSignalCarriesIdentityFileChanges`（改名、不泄内容、无变化即静默、删除也算变化） |
| G9 + G20（已修） | 写端（收据）：`go test ./internal/session/ -run TestRunReceipt` —— 用**真 sqlite** 走"实例 1 写入 → 另一个 manager 重新加载 → 仍然读得到"，未绑定配置的会话不盖章。读端：`go test ./internal/agent/ -run 'TestEnvBaselineComesFromTheTurnReceipt|TestRebuiltAgentStatesTheChangeFromTheReceipt|TestReceiptCarriesEverySampledFamily|TestEnvSignal'` —— 取最后一条收据、只有 model 列不猜、无收据/坏收据即沉默，以及**五个族全部能穿过收据**（skills/tools/memory/identity/cron/config 各一条断言）。**反证**：① 注释掉 `Append` 的盖章 → `TestRunReceiptStampSurvivesAReload` 红；② 让 `envBaselineFromReceipt` 永远返回"未采样"（等价于删掉收据基线）→ 三条读端测试全红 |
| G19（已修） | `go test ./internal/store/ -run TestSandboxLeaseUnhydrated`（真 sqlite：标记可读、外部/错实例写无效、Replace 归零、迟到的旧实例写不会钉到继任者）+ `go test ./internal/sandbox/ -run 'TestE2BPoolAdoptionCarriesTheUnhydratedFact|TestE2BPoolPublishesTheUnhydratedFactOnCreate'`；真机 `FASTAGENT_E2B_LIVE=1 E2B_API_KEY=… go test ./internal/sandbox/ -run TestE2BLiveUnhydratedFactSurvivesPodHandoff -v`（两个池 = 两个副本，共享 sqlite 租约表；pod B 采纳 pod A 的实例后仍报未水合）。**反证**：去掉采纳处的读取 → 真机测试红 |
| G13（改判为"非缺口"） | `go test ./internal/agent/tools/ -run 'TestBashOutputTool|TestSandboxJobOutput'`（退出码、killed、lost 三种状态的渲染）与 `TestBashOutputSchemaCarriesTheWholeContract` / `TestExecDescriptionsStayInSync`（工具契约里写明"退出后仍可读、以退出行为准"）；真机 `go test ./internal/agent/tools/ -run TestSandboxBackgroundE2BLive -v`（后台 job 在**后续调用**里仍然存活并能被拉到状态）。**判据本身在代码里**：状态行由每次读取现场重算，不来自任何"上次看到"的记录 |
| G21 + G7b 删除半边（已修） | `go test ./internal/setup/ -run TestHandleAgentFileDelete`（面板形状的路径：`sessions/<sid>/f` + `?sessionId=` 真的把 store 里那份删掉；项目根路径把 chat 从请求里取；旧的无前缀形状仍可用；沙箱删除失败会回报 `sandboxRemoved:false`）+ `go test ./internal/sandbox/ -run 'TestSandboxPathForStorePath|TestStorePathScope|TestE2BPoolLiveExecutorDoesNotCreate|TestDockerPoolLiveExecutorDoesNotCreate'`（映射与"只找活实例"）+ `go test ./internal/gateway/ -run TestRemoveWorkspaceFile`（没池/单副本后端是 no-op，有副本时交给执行器）。真机：`FASTAGENT_E2B_LIVE=1 E2B_API_KEY=… go test ./internal/sandbox/ -run TestE2BLivePanelDeleteSticks -v` —— **两半**：不加 d1 时"删除被下一次同步复活"（可复现），加了 d1 后删除钉住。**反证**：把 handler 的路径约定改回旧行为 → `TestHandleAgentFileDelete_UsesThePathThePanelClicked` 立刻红（"the panel's delete left the file in the store"） |
| G10（已修） | `go test ./internal/agent/ -run 'TestEnvSignalCarriesScheduledJobChanges|TestCronFingerprintIgnoresRunBookkeeping|TestEnvSignalStatesUnreadableJobList'`（删除/改期/新增都点名；记账字段不触发；读不到就说读不到） |
| G14（已修） | `go test ./internal/agent/ -run 'TestHeartbeatReadsWhatThePromptShows|TestHeartbeatFallsBackToTheDiskCopy|TestHeartbeatWithNoFileSendsNothing'`（store 与磁盘内容故意不同 → tick 与提示词必须同源；无 store 回落磁盘；都没有则不发回合） |
| G11（stdio 已修） | `go test ./internal/mcp/ -run 'TestStdioClientHandsNotificationsToTheHandler|TestManagerWiresNotificationsThroughTheGate|TestManagerDoesNotWireATransportWithoutNotifications'`；HTTP 侧可读 `internal/mcp/http.go` 确认没有 SSE 流 |
| G15 | —（协议合规，不是投递义务） | `rg 'initialized' internal/mcp/` 无命中（两个传输都没发这条通知） |
| G12（已修） | `go test ./internal/gateway/ -run 'TestDeferredTurnsAnnouncesADroppedScheduledTask|TestDeferredTurnsDropsMessagesPastBudget|TestDroppedCronNoteWithoutAJobName'`（cron 才发声、点名任务、没有任务名也不留悬空引号） |
| G18（已修） | `go test ./internal/agent/tools/ -run 'TestApplyPatchUsesTheSameStoreKeyAsWriteFile|TestApplyPatchDeleteUsesTheSameStoreKey|TestApplyPatchKeyInANonCodingSession|TestWriteThroughMirrorsOneKeyAndOnePath'`；真机 `FASTAGENT_E2B_LIVE=1 E2B_API_KEY=… go test ./internal/agent/tools/ -run TestE2BLiveOnePathIsOneKey -v`（命令在文件头）。**反证**：把 `writeForPatch` / `writeForPatchSandbox` 里的键改回 `r.sessionID, path`，前三条立刻变红（`keys = [app/notes.md sessions/<sid>/notes.md]`） |
| ~~G17~~（已决策 G+H+A，全部已修） | 单测：`go test ./internal/sandbox/ -run 'TestWriteThroughReachesEveryContainer|TestWriteThroughCountsAContainer|TestRemoveLiveWorkspaceFileReaches|TestSyncWritesBackToTheProjectRoot|TestSyncScopeEqualsHydrateScope'`（广播到项目内每个活容器、别的项目不受影响、失败计数；回写落项目根而不是 chat 子目录；折叠规则本身）+ `go test ./internal/runtime/ -run TestPreviewSandboxSession`（预览容器按项目寻址）+ `go test ./internal/agent/tools/ -run TestWriteFileStatesAPartialProjectMirror`（部分失败有 σ）。真机：`go test ./internal/sandbox/ -run TestE2BLiveProjectWriteReachesSiblingContainer -v`（同一项目两个容器：A 写 → B 读得到；A 删 → B 也没了，B 的同步不复活）与 `go test ./internal/agent/tools/ -run TestE2BLiveProjectSessionKeepsOneTree -v`（无副本；exec 产物落项目根且工具可见；沙箱改既有路径仍被拒）。**反证**：去掉广播 → 前者在"A 写 → B 读得到"红；把 `ws := syncStoreScope(sc)` 改回 `sc` → `TestSyncWritesBackToTheProjectRoot` 红（键变成 `chat-1/artifact.txt`） |
| ~~G22~~（已修） | `go test ./internal/sandbox/ -run TestWriteThroughStampsWithTheStoreScopeItWasGiven`（盖章用**被声明**的 store 作用域；作用域里没有这个对象时不盖章）。真机：`FASTAGENT_E2B_LIVE=1 E2B_API_KEY=… go test ./internal/sandbox/ -run TestE2BLiveSyncReadsNoBodiesForStampablePaths -v` —— 断言一次同步里**整对象读取数为 0**（修复前实测 1）。**反证**：把 `lifecycle.go` 的盖章改回 `Stat(sc.agentID, sc.projectID, sc.sessionID, …)` → 单测立刻红（`primary container was not stamped`） |

## 7. 附录：G5/G6 的本地探针（可重跑）

探针要点：用**真** `LifecyclePool` 而不是测试替身。原因是 G5 恰好是"两个各自被测试钉住"
的行为组合出来的，任何一端用假件都会把它盖住。

```go
// 临时放进 internal/agent/tools/，go test ./internal/agent/tools/ -run TestProbe -v
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

// pool 需要实现 sandbox.ExecutorPool（Get/Release/CloseAll/Backend）。
lp := sandbox.NewLifecyclePool(probePool, time.Minute, time.Minute)
lp.SetWorkspace(workspace.NewLocalFS(t.TempDir()))
r  := NewRegistry(t.TempDir(), t.TempDir()); r.SetWorkspaceStore(workspace.NewLocalFS(t.TempDir()), "a")
r.SetSessionID("s1")
ex, _ := lp.Get(ctx, "a", "", ""); r.SetExecutor(ex)
out, _ := r.Execute(ctx, "write_file", `{"path":"notes.md","content":"hello"}`)
// 2026-09-18 实测输出：Written 5 bytes to notes.md + "[workspace] notes.md is too large to compare
// automatically … (over 2 MiB)"  ← 5 字节的文件说它超过 2 MiB
```

第二组探针把 `IsRemoteWorkspace()` 去掉（docker 形状：只有一个副本），输出**逐字相同**——
这正是 G5 的判定："出口不知道'有没有第二份副本'，只知道'我这次没比较'"。

## 8. 未复核项（老实记下）

- G10 的两个方向（cron job 被 UI 删除、`HEARTBEAT.md` 被改写）没有找到测试，
  代码路径是推断（`cron` 表 → 调度器；`heartbeat.go:94` 每 tick 现读文件），未见反例；
- ~~G12 只核对了 `deferred_turns.go` 的丢弃分支~~（**已补核**：cron 在 fire 的同一段里就 `UpdateCronJobRun` 推进下一次，**不补投**——所以那次注记是漏跑的唯一痕迹；harness 自己的来源由下个 tick / 下次 PostTurn 补上）；
- provider 账号回退要不要给 agent 信号，取决于产品是否把"我的请求由哪个账号发出"算作
  agent 世界的一部分——本文只记录，不下判断（它已经被一条 slog 钉住，属于运营可见性）。

## 9. 顺带清掉的死代码与遗留配置（2026-09-18）

审计"这个 feature 下线后还剩什么"时（标识符、字段、常量、配置、存储五类扫描），代码里的
baseline 一族是**零残留**——没有 `baselineDigestMax`/`staleWrites`/`decideReconcile` 之类的标识符，
没有未读字段，没有无人引用的常量，也没有 `ws_baseline` 键/列或对应 env。清掉的是下面这些：

| 项 | 位置 | 为什么要清 | 兼容性 |
|----|------|-----------|--------|
| `makeExecTool` | `internal/agent/tools/exec.go` | 全仓无引用的死函数，且它调 `makeExecToolFull(nil, …)`——真被调用会构造一个拿不到 Registry 的 exec 工具 | 无（unexported、无引用） |
| `BoxliteClientID` 整条链 | config 字段 / env `FASTAGENT_SANDBOX_BOXLITE_CLIENT_ID` / 管理端请求字段 / web TS 类型 / 池与执行器构造参数 / `defaultBoxliteClientID` | 三点：① 它是一个**设了没有任何效果**的旋钮——配置面在告诉运维"这里填 OAuth client_id"，而代码从不读它、也不报错（silent no-op）；② 它被一路传递（config → pool → executor），`grep` 看起来是活的，下一个人读构造函数会以为它有意义；③ 它所属的 OAuth 交换已在上游移除，留着就要永久维护一个配置字段 + env + 管理端字段 + 前端类型。删掉之后，"client_id 不再有意义"从注释变成了编译期事实 | 旧配置行/旧客户端里带 `boxliteClientId` 会被忽略（`json.Decode` 默认不拒绝未知字段）；env 变量不再被读取；管理端响应不再出现该键（web 端已无引用） |
| ~~`WorkspaceSync`~~（`internal/sandbox/workspace_sync.go`，121 行，**已删除**） | 2026-09-18 重盘发现 | **死码**：`NewWorkspaceSync`/`WorkspaceSync` 全仓零引用——它生于 `87c50ee`（2026-04-12 多用户重构）、此后从未被修改，也没有任何 commit 引用过构造器；且其自带的 `WorkspaceStore` 接口（userID-only 键）与现行 `workspace.Store`（agent/project/session 作用域）签名不兼容，现行 `LocalFS`/S3 不满足它。它实现的那套"整体 hydrate + 按需 flush"已被 `LifecyclePool` + `hydrateWorkspace` + `syncSnapshot` + `WriteThrough` 取代 | 无（unexported 包内类型 + 零引用） |
| 三处注释化石 | `internal/sandbox/lifecycle.go`（"…record it here, once per hydrate. Skipped for a scope the sandbox" 半句话；`(over the digest cap)`）与一支测试里的未读字段 | 它们在描述已经不存在的机制，读者会以为基线还在 | 纯注释，零行为变化 |
| 3 个零引用函数：`generateRandomToken`（`handlers.go`，被同文件的 `newRandID` 取代）、`filterAccounts`（`handlers_agent_channels.go:169`，`flattenChannelRows` 的调用方都传空 filter）、`defaultIfEmpty`（`handlers_agents.go`） | 2026-09-18 全库引用计数（593 文件 / 2703 个函数定义） | 逐个人工复核过：无接口实现角色、无测试引用、无函数值传递 | 无 |
| `Registry.sandboxSessionID`（字段 + `SetSessionID` 里的赋值 + 注释） | `internal/agent/tools/registry.go` | 本轮 G18 修复中引入又未接线：全仓零**读**引用（只有赋值），且注释把读者指向一个不存在的符号 `sandboxScopeSession`——比没有注释更糟，因为它断言了一条没人执行的设计。**它描述的那个事实本身是真的**（沙箱池按 chat 建实例、store 按项目根折叠），所以事实搬进了 01 §8.2 / 本条 G17，字段删掉 | 无（unexported 字段、零读引用） |

> **同一次扫描的第四个候选当时没有删**：`mergeSkillEntry`（`handlers.go`）同样零引用，但追下去发现它是**没接线的守卫**——删掉它就等于删掉"遮罩写回"这个缺失行为的唯一实现。它当时留在原地并加了说明注释，缺口记为上表的 **G16**；**随后已按该缺口接线修好**（两条写入路径共用它，见 G16 行）。这条是这轮扫描最值钱的产出：**"零引用"只是线索，不是判决**——同一个信号，可能是死码，也可能是"本该被调用却没人调用"，两者的处置相反。

**故意保留的**：05/06/07 里那几段"已下线"的历史（每处都有显式标注，属于演进记录）；
以及一条**反向断言**（测试断言工具结果里不含 `resolve_workspace_conflict`）——它是回归护栏，
防止"让 agent 选边"那套设计被重新引入，不是残留。
