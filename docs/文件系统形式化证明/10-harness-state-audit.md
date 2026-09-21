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
>
> **更正（2026-09-19 实测）**：T1–T6 **已提交**（分支 `fastagent`，HEAD `8984c99`；`a0080a5` 是 HEAD 的祖先；
> **私有仓库 `tokenaissance/fastagent` 已含该提交**），**dev 已部署** `8984c99`，
> **production 落后 34 个提交**（`a24c0a8`，镜像 `…:20260917035926-fastagent-a24c0a8`）。
> 本仓有两个远端：`fastagent`（私有，当前所在）与 `origin` = `tokenaissance/fastclaw`（**公开镜像，落后 34 个提交，按决定暂不推送**）。
> 所以下面那句"未提交"已经过期；"**未上 production**"仍然成立。下文表格里的 `HEAD` 指 2026-09-17 的 `16a7532`，
> 读时以本行为准。
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
>
> **「控制动作形态」列（2026-09-20 加，取值就是 STPA 的四个引导词）**：它记的是**这一格上出问题的那种动作形态**——
> **不提供**（该说没说）／**提供**（说了，但那句话说出去本身导致危险：假话、空动作、覆盖）／
> **顺序**（说了，但时机或次序错）／**时长**（该停没停、该退役没退役）。
> **「—」表示这一格根本不是形态问题**——它属于别的族（单一来源、作用域不变式、资源上界、死代码、协议合规）。
> **为什么加它**：原来的表只有「义务」列（O1–O5），它能判**违规**，判不了**遗漏**——
> 「在这个条件下我们什么都不提供」在表里是隐形的。加上这一列之后，**遗漏本身变成一行可见的记录**。
> **下一步（未落地，候选）**：给每类 δ 规定一个**允许的动作形态集**（提供／不提供／顺序／时长），
> 并要求**「拒绝」具名**（拒的是哪一类 δ、依据哪一条判据）——那就是把这张「带义务登记的会计」
> 升级成「带控制动作清单的会计」。现在只做到**可见**，没有做到**控制**；
> 且这一层管不到「agent 收到之后仍然做错」（那半边在《认知哲学的数学原理》19.5.1 的第③行）。
> 完整表述见同书 19.4.6.2 的「从会计升级成控制」。
> **可核查的读数**：本表 23 行 = 不提供 **13** · 提供 **2** · 顺序 **1** · 时长 **0** · 非形态 **7**。
> 「时长」为空不是漏填，而是一条**可被推翻的读数**——找到一行「该停没停」即可推翻它。
> **它已经被找到了一半**：表外的块引用条目里有一行是「时长」（**G27**，已退役的资源被永久保留）。
> 表内 0 行、全表 1 行——两个数字都写出来，才叫读数。
>
> **这张表为什么能判「遗漏」**（2026-09-20 补）：因为这套系统的分析单位是**规定的**，不是观察的——
> 三个角色（I1）、两个落点（`D₁` / `D₂`）、一条通道责任（∀δ ⇒ ∃σ）都写在文档里，可以照着念。
> 于是"该发生的事没发生"能**对着规格**判，不必等事故：**G3、G14 这类行，读代码就能判**
> （载体在进程内存里、同一份事实有两个来源）——它们是**结构性质，不是事故性质**。
> **代价**：裁决只在**这套系统自己**的范围内有效——规格没写到的地方，这里同样看不见。
> 完整形态见 07 §3.11.1 的方法论备注与《认知哲学的数学原理》19.4.9。

| # | 义务（08 §2.2） | 缺口 | 触发者 | 代价 | 修法方向 | **控制动作形态** |
|---|------|------|--------|------|---------|------|
| ~~G5~~ | O1 | ~~`write_file` / `apply_patch` 的 σ 恒为假（over 2 MiB），docker 上也照发~~ | agent 自己 | **P0 已修（2026-09-18）**：`CompareResult` 四态 + docker 静默，同仪器复验见 §2.1 | — | 提供 |
| ~~G6~~ | O1 | ~~`apply_patch` / `write_file` 永远不会报"替换了不同版本"~~ | agent 自己 | **P1 已修（2026-09-18）**：`previousStoreVersion` + `plannedWrite.previous`；`write_file` 现在报得出 | — | 不提供 |
| ~~**G7a**~~ | O1 | ~~分歧不可见~~ | 用户 | **P1 已修（2026-09-18）**：`list_dir` 现在把"store 有、沙箱没有"的路径点出来（含原因与出路），docker 不问、问不到不说 | — | 不提供 |
| ~~**G7b**~~ | —（写路径对称性，属 Cordis 前置条件） | 上传/删除不写进活沙箱 | 用户 | **上传半边已决策（2026-09-18）：不做穿透 = 选项 a**。产品语义 = **面板是"文件库"**，"上传后下一次 `exec` 立刻 `ls` 得到"**不是**要求；代价与补救都已由既有信号说清（`list_dir` 与每次 exec 后的同步都会点名"store 有、沙箱没有"，并给出 `read_file`+`write_file` 的搬法）。澄清：在 LocalFS+docker 上它天然立刻可见（同一棵树），**不做**为把它藏起来而改架构——准确表述是"不承诺立刻可见，也不阻止（由后端决定）"。**删除半边仍未修，且必须修**：见下 | 删除半边 = 待决策（d1 穿透删除 vs d5 删除墓碑），见 [05 §8](./05-remediation-plan.md) | 不提供（已决策） |
| ~~**G21**~~（2026-09-18 新发现；与 G7b 同族） | —（路径/作用域约定不一致；属 F1 的"同一路径一个键"） | ~~面板删除静默无效：列表返回带作用域前缀的 agent 相对路径，面板原样 DELETE 又附带 `?sessionId=`，handler 再把作用域拼一次 ⇒ 键多一层前缀 ⇒ 目标不存在；两个后端对"删不存在的目标"都返回成功 ⇒ 200 + UI 以为删掉了 + 刷新后那行又回来~~ | 用户 | **已修（2026-09-18）**：① **Fix 0** —— 删除改用与下载同一个约定（路径自带前缀时按 agent 相对解释，`Delete(agent,"","",path)`；旧的无前缀+query 形状继续可用，`sandbox.StorePathScope` 是唯一判定点）；② **d1** —— 同一次删除也把**活沙箱**里那份删掉（`Gateway.RemoveWorkspaceFile` → `sandbox.LiveWorkspaceFileRemover`，经 `LiveExecutorPool` **只找活实例、绝不建实例**），否则下一次同步会把它写回（真机两半都钉住）。失败时返回 `sandboxRemoved:false` + warning，不假装干净 | — | 提供 |
| ~~G8~~ | O1 | ~~身份/系统文件被外部改写无信号~~ | 用户 / 其他会话 / 其他 pod | **P1 已修（2026-09-18）**：回合级采样加入 7 个身份文件的指纹；只报文件名、不报内容 | 见 §3.3 的落点（`identitySampleFiles`） | 不提供 |
| ~~**G9**~~ | O4 | ~~agent 配置被外部改写无信号；基线随实例消失，而配置变更恰恰靠重建实例生效~~ | 用户 | **已修（2026-09-18）**：采样加入 `model` + `prompt_mode`；before 不再自造，而是**读对话自己的回合收据**——零新增存储、零新增写路径、天然跨实例与跨副本。重建后的**新实例首回合**即可说出 `my configuration changed: <旧> → <新>`；取不到收据即沉默。收据后来承载整张快照（见 G20） | —（G20 已把同一机制推广到其余五族） | 不提供 |
| ~~**G20**~~ | O4（G9 的推广） | ~~技能 / 工具名 / 记忆 / 身份文件 / cron 五族的基线仍在进程内 `envTracker.last`：pod 重启或热重载后首回合沉默；且 `handlePlanMode`、API 的 `HandleMessageStream` 两条回合路径既不采样也不盖戳~~ | 用户 / harness | **已修（2026-09-18）**：收据改为承载**整张 `envSnapshot`**（metadata `run_receipt`），`envTracker` 类型连同 `last` map / mutex / `configWas` 分支一起删除，判定变成纯函数 `renderEnvDelta(prev, seen, cur)`；三条回合入口统一采样 + 盖戳 | 见 §3.3：这是"判据复用既有耐久记录"这条落点的第一次完整落地；仍存的边界只有"取不到收据 ⇒ 沉默"（首回合／压缩后） | 不提供 |
| ~~**G19**~~（2026-09-18 新发现） | O1 | 未水合声明（Policy C）的判据 `workspaceUnhydrated` 只在**创建它的那个进程**里：`adoptFromLease` 明确不重放 hydrate（"the creating pod hydrated the same scope"），于是换手后的副本 flag 恒为 false ⇒ **声明消失**，agent 把空 `/workspace` 读成"文件没了" | 沙箱生命周期族 | P1 **已修（2026-09-18）**：该位随**实例**落进 `sandbox_leases.unhydrated`（`SetSandboxLeaseUnhydrated`，owner+sandbox_id CAS，Acquire/Replace 时归零 ⇒ 不会钉到继任实例上）；采纳时 `ex.setWorkspaceUnhydrated(rec.Unhydrated)` 读回，创建/替换发布实例时 `publishUnhydrated` 写入 | 真机 E2E：pod A 用坏 store 建实例（列表失败）→ pod B 采纳同一实例 → 仍报未水合；反证（去掉采纳读取）该 E2E 变红 | 不提供 |
| ~~**G10**~~ | O1 | ~~cron job / HEARTBEAT.md 被外部改删无信号~~ | 用户 | **P2 已修（2026-09-18）**：定时任务清单进回合级采样（`scheduled jobs added / changed / no longer exist: <name>`）；`HEARTBEAT.md` 的内容变化本就被 G8 的身份文件指纹覆盖；读不到清单时声明"读不到"，**不谎报删除** | — | 不提供 |
| ~~G14~~ | —（单一来源，不是投递义务） | ~~`HEARTBEAT.md` 有**两个来源**：提示词读 store（`loadFileForUser`），heartbeat 触发却只读 `<home>/HEARTBEAT.md`~~（`heartbeat.go`） | 用户 / 运维 | **P2 已修（2026-09-18）**：`loadHeartbeatTasks` 改走与提示词**同一个解析器**（`ctxBuilder.loadFileForUser("HEARTBEAT.md", ownerUserID)`，store 优先、磁盘回落）；owner 正是该回合 `chatterUserID` 对 `SourceHeartbeat` 的解析结果，所以"看到的"与"触发的"必然同一份。没有 ctxBuilder 的形态（嵌入式/CLI）保持原有磁盘读法 | — | — |
| ~~**G11**~~ | O1 | ~~MCP server 侧通知被丢弃~~ | 外部 server | **stdio 侧 P2 已修（2026-09-18）**：捕获 → 闸门（每 server 30s）→ 复用 `mcpConfigNotify` 重建 → 回合级工具集信号自动报出；**HTTP 侧仍无通知通道**（见 §3.4 的两个边界，需要 SSE 或定期 re-list 才能补） | — | 不提供 |
| **G15** | —（协议合规，不是投递义务） | `notifications/initialized` 从未发送（stdio 与 HTTP 都没有） | — | P3：规范要求 initialize 之后发；现有 server 不要求，可能是某些 server 开始推送通知的前提 | 补发这条通知，但必须先在真机（QC / Quandora）上验证握手不受影响 | 不提供 |
| ~~G16~~ | —（setup API 的遮罩写回，非投递义务） | ~~技能密钥被自己的遮罩覆盖~~（2026-09-18 死码扫描发现） | 运维面板 | **P1 已修（2026-09-18）**：规则收成**一个家** —— `mergeSkillEntry`（条目级）+ `mergeSkillEntries`（补丁级），被**两条**写入路径共用：全局 `skills.entries`（namespace 扫描前先与库中现值合并）与 per-agent 覆盖行（`scope.SettingInto` 取现值再合并）；providers/channels 既有的内联守卫**保持不动**（请求形状不同，等第三个变体证明同一缝再抽）。实现中撞到一个真坑：JSON 解码器**复用**（不替换）map，所以"覆盖前快照"必须**深拷贝**（`cloneSkillEntries`），否则比值比的是被就地改写的自己——第一次接线正是这样悄悄保留了遮罩 | — | — |
| ~~**G12**~~ | O2 | ~~自动回合被推迟/丢弃只有 slog~~ | harness | **P2 已修（2026-09-18）**：丢弃逐条带全信息（原来只有 `count=N`）；用户创建的 cron 额外在该会话发一条注记（有界发送，不启动回合）；harness 自己的来源只记录，不打扰用户 | — | 顺序 |
| ~~**G13**~~ | O3（不是 O2） | ~~后台 shell / 沙箱 job 结束不推送~~ | agent 自己 | **改判：不是缺口（2026-09-18 形式化复核）**。δ = 进程退出；σ **存在且每次读取从世界重算**：`bash_output` 返回 `[status] exited (code=N)`（`killed` / `lost — the sandbox was replaced` 同样由现场推出，[sandbox_background.go](../../internal/agent/tools/sandbox_background.go) 第 350–375 行）。按 08 §2.2.2，**D₁ 只在调用期间存在**，所以"空闲时不推送"不等于"没有投递点"——投递点是消费侧那次读取本身（O3）。判据可重算 ⇒ 三落点里的**落点 1**，不需要任何载体 | ⛔ **不要实现"下一个工具结果附'后台 X 已退出'"**：那要引入一张进程内的"已退出但还没报告"集合，正好是 O4 形状（实例一换就丢），而这条事实本来就能重算 —— 用一次新的进程内状态换一个已经可达的 σ，是净亏 | —（已改判） |
| ~~**G18**（= 01 §8）~~ | —（实体不变式：一条路径一个键，非投递义务） | ~~`apply_patch` 用 `r.sessionID` + 原样路径写 store，而镜像与另两个工具用 `scopeSessionID()` + `wsPath()` ⇒ 一次写入自相矛盾：store 落 A 键、镜像落 B 路径，一份文件两个键~~ | agent 自己 | **P1 已修（2026-09-18）**：6 个触点（host 3 + 沙箱 3）统一到同一解析；沙箱模式的 `apply_patch` 同时补上逐文件穿透（此前完全不调用镜像）。单测 3 + 1 条、真机 E2E 1 条，三条都做过反证（改回旧写法即变红），详见 01 §8.1 | — | — |
| ~~**G17**~~ | —（作用域不变式：同一族，比 G18 低一层） | 项目里"一个文件树、多个容器"的可见性：项目会话的容器是**每 chat 一个**（有意为之：并发 chat 不共享 shell），而预览的 dev server 只跑在其中一个里 ⇒ ① 控制台起的预览用的容器（`agent:p:<pid>`）**agent 的回合永远不用** ⇒ 写入永远到不了它；② 兄弟 chat 改了文件，dev server 那个容器收不到（docker 靠 bind mount 天然没有这个问题） | agent / 用户 | **已决策 + 已修（2026-09-18，方案 G+H）**：**G** = 预览容器统一按项目寻址（`previewSandboxSession`：有项目就 `session=""`，两个入口从此同一个容器，一个项目一个预览）；**H** = 写入与删除**广播到项目内所有活容器**（`LiveProjectExecutors` + `mirrorToProjectPeers`/删除扇出）——把 docker 的挂载语义在云后端显式做出来，**保留"每 chat 独立 shell"**。部分失败会给出 σ（见 §2.1）。真机：同一项目两个容器，A 写 → B 读得到；A 删 → B 也没了且不会被 B 的同步复活 | **A 也已修（同日决策）**：`syncStoreScope` 让回写在项目会话里折叠到项目根（与 hydrate、与文件工具同一个键）⇒ 不再产生 `<项目>/<会话>/…` 副本，`exec` 新建的文件立刻可被 `read_file`/`list_dir` 看见。**未做迁移**：折叠前已产生的副本仍在库里（不再刷新、也无人清理）——一次性清理见 [05 §6](./05-remediation-plan.md) 的 `scripts/workspace_project_chat_duplicate_cleanup.py`（只在「同样字节在项目根另有存活」时才列入删除）。：无副本、exec 产物落项目根且工具可见、沙箱改既有路径仍被拒（且拒绝指向工具读的那个键） | — |
| ~~**G22**~~（2026-09-18 实现中发现） | —（作用域不变式，同一族的**第三处**） | 写入穿透的 **mtime 盖章**用**沙箱作用域**查 store（`Stat(sc.agentID, sc.projectID, sc.sessionID, storeKey)`），而工具在 coding-root 项目会话里写的是**项目根**（`session=""`）⇒ 项目会话里这次 Stat 永远 miss ⇒ **盖章静默不发生**。 | 对账拿不到"size+mtime 相同"的廉价判据，每次同步对这种路径回落到字节比较（`equalToStore`），**功能不受损** | agent / 沙箱 | **已修（2026-09-18，同一轮）**：写入方把 store 作用域一起交下来 —— `WriteThroughScope(storeScope, storeKey, sandboxPath, content, previous)`（`sandbox.StoreScope`），盖章与 H 的广播份都用**调用方声明的那个作用域**，不再从容器推断。**实测**（真机 E2B，`TestE2BLiveSyncReadsNoBodiesForStampablePaths`）：一次同步里这条路径的**整对象读取 1 → 0**（stats 仍 2 次）。单测 `TestWriteThroughStampsWithTheStoreScopeItWasGiven` 钉住“盖章用的是被声明的作用域”；**反证**：把 pool 改回用沙箱作用域查 store → 立刻红。**为什么只有性能影响也做**：这条缝（store 作用域跨层传给沙箱层）在同一轮已产出三处缺陷（G21 删除、G17/A 同步、本条），满足“投资边界要有 3+ 次历史变更”的判据；而修法是把“猜”换成“传参”（端口修正），不新增机制 | — |
| ~~**G23**~~（2026-09-18 评审发现；**同日已修**） | —（作用域不变式，同一族的**第四/第五处**：同一条规则被写成多种表达式） | ~~「项目会话 ⇒ 键落项目根」被写在**三处、用两种判据**：agent 侧 `Registry.scopeSessionID()` 用 `codingRootScope`（= `a.projectRuntime != nil && projectID != ""`），沙箱侧 `syncStoreScope()` 用 `projectID != ""`，面板侧 `StorePathScope()` 用"路径带不带作用域前缀"~~ 顺着查还发现**第五处**：**布局表**（`pid/sid` → 目录）在 `LocalFS.scopeDir` 与 `S3.key`/`S3.scopePrefix` **各写了一遍** | agent / 面板 / 沙箱 / 两个 store 后端 | ~~今天不可达，所以不是缺陷；但这条等式是"靠接线成立"的~~ **P3 已修（2026-09-18）**：规则收进 [`internal/workspace/scope.go`](../../internal/workspace/scope.go) 的两个纯函数 —— `ScopeSegments`（布局表，LocalFS + S3 共用）与 `WriteScope`（写者折叠，文件工具 + 沙箱回写共用）；`Registry.codingRootScope` / `SetCodingRootScope` 整个删除，`sandbox.StoreScope` 改成 `workspace.Scope` 的**类型别名**（端口不再自带第二份事实）。**行为差（唯一一处）**：折叠判据从"有运行时且在有项目中"变成"在有项目中"，两者只在"有项目、但没有 runtime manager"的部署里不同 —— 而 `cmd/fastclaw/main.go` 无条件构造并接线 runtime manager，那种部署里项目本身也建不出来 | 单测：`go test ./internal/workspace/ -run 'TestScopeSegments\|TestWriteScope\|TestAWriterScope'`（布局表、写者规则、两者描述同一文件系统）、`go test ./internal/sandbox/ -run 'TestLayoutWriteScopeAndParserAgree\|TestProjectWritersAndTheSyncShareOneScope\|TestAProjectChatSubdirKeyIsItsOwnPath'`（布局/写者/面板解析三者一致；以及"项目 chat 子目录的键是另一个对象"这条被钉住）、`go test ./internal/agent/tools/ -run TestScopeSessionIDCollapsesInsideAProject`。**反证**：把 `syncStoreScope` 改回"不折叠" ⇒ `TestProjectWritersAndTheSyncShareOneScope`、`TestSyncWritesBackToTheProjectRootNotTheChatSubdir`、`TestSyncScopeEqualsHydrateScopeForProjects` 三条变红；把 `scopeSessionID()` 改回 `r.sessionID` ⇒ `TestScopeSessionIDCollapsesInsideAProject`、`TestApplyPatchUsesTheSameStoreKeyAsWriteFile` 变红（两条都实测过） | — |
| G1–G4 | G1/G2 = **O2**（原无投递点，已修）；G3 = **O4**（进程内队列，已修）；**G4 = O1（2026-09-18 已修到"事实已声明"为止）**：同步里加一次 store List，把"store 有、沙箱没有"的路径报出来（与 G7a 共用同一句话 `sandbox.StoreOnlyLine`）；**归属（"沙箱删的"还是"后来上传的"）＝ 已决策不做**（2026-09-18，选项 a）：后果已送达，清单只买因果，且"交付清单"要在每次 hydrate 落上千行、"写者清单"要动 6 条写路径 —— 见 [05 §8](./05-remediation-plan.md) 决策记录 | 沙箱生命周期族（重建、删除） | provider / harness | 见 09 | 见 09 §6 | 不提供 |
| **G31**（2026-09-21 新增；缝登记 #2 的判决） | O1（该产生而没产生） | **租约存储不可用时，准入互斥被静默放行**：`GetSandboxLease` / `AcquireSandboxLease` 一旦报错，代码保留本地沙箱或本地 executor 继续跑（`e2b_executor.go:2182` / `:2434-2435` / `:2445` / `:2489`），只留一行 `slog.Warn`——**agent 看不到**；且这是被测试钉住的契约（`lease_pool_test.go:415` `TestE2BPoolFreshGetLeaseErrorsFailOpen`，注释原话 "Registry errors fail open"）。在 DB 抖动窗口内，两个 pod 可以各自跑同一 scope——这正是 09-18 跨副本双轮次事故的形态 | harness | **未修**：见证不可求值 ⇒ 消缝 / 移址都不通（本系统唯一的强制点是 DB）⇒ 按 12 §3.2 应**收手 + 申报**，或保留放行但**必须产生 σ**（"本次未取得跨副本准入"）落在 `D₁`/`D₂`；现状 = 19.4.6.1 反面清单里的 ✗「沉默地降级」 | 见 [12 §3.2](./12-lease-formal-design.md)（两条路任选其一，都必须具名） | **不提供** |
| **G32**（2026-09-21 新增；缝登记 #5 的 P-WAD 第 1 行**实测**） | O1（该产生而没产生） | **释放时"载体里没有 epoch" ⇒ 释放被静默丢弃**：`p.leaseEpochs`（`e2b_executor.go:1868`）是 fencing epoch 的**进程内镜像**（epoch 本身由共享表 `sandbox_leases` 发放）。发放那一刻注册表报错时，executor 照常登记而 epoch **不**登记（`:2215-2216` 注释原话 `fail-open; release will not destroy the sandbox`）⇒ 此后 `Release` 把 **epoch=0** 交给 `DELETE ... AND epoch = ?`（`internal/store/sandbox_leases.go:237`）⇒ 一行也匹配不到 ⇒ `deleted=false` ⇒ `:2615` 直接 `return nil`。**受控实验（`internal/sandbox/lease_epoch_gap_test.go`，2026-09-21）**：`Release` 返回 `nil`、销毁 **0** 次、日志 **0** 行，且**那一行仍记着释放者自己**（owner=pod-a）——而它与"被兄弟副本围栏挡下"（另一种**正确**情形）**观测上逐项相同**：两个不同的世界，一个观测 | harness（沙箱池释放路径） | **未修**：现状 = 19.4.6.1 反面清单里的 ✗「沉默地降级」，即 12 §3.2 判过的"放行"那一半**少了申报**。后果：一次驱逐 / 一次面板删除**静默地什么都没释放**（实例与行都活着，下一位使用者会**采纳**它而不是另建）。**边界**：这不是 §3.2 判过的"见证不可求值"（那里 `:2609` 的方向被记为"方向相反，是对的"），而是**见证可求值、而调用方手里的键是空的** | ① **申报**（最小）：`epoch == 0` 时产生 σ（"本次释放未作用于任何行"）；② **换判据**：无 epoch 时改按 (owner, sandbox_id) 做 CAS 删除——那条判据是调用方**确实持有**的事实，但要在 [12 §5](./12-lease-formal-design.md) 的 L4(c)（G25 陈旧 epoch 实测）语境里过一遍。两者都必须具名 | **不提供** |

> **G32 的三条出路与代价对账（2026-09-21 新增）**：
> ① **申报**：`epoch == 0` ⇒ 产生 σ（"本次释放未作用于任何行"）——验收标准是**一个数**，不是一行日志；
> ② **换判据**：无 epoch 时改按 `(owner, sandbox_id)` 做 CAS 删除——**这条路在 G25 上过不去**（12 §5 的陈旧令牌实测），
> 除非先把署名换成逐次唯一（`owner = <pod>/<uuid>`，12 §6），而那本身又是一张进程内载体；
> ③ **消缝**：发放失败 ⇒ 不登记（当场销毁 + `Get` 失败）。
> 三者的代价表（治哪一段 / 买到什么 / 代价 / 风险 / 前置条件）见《认知哲学的数学原理》19-5 §19.5.8.9 **§九**。
> **选哪条待裁定**——本表只登记，不裁定。

> **G22 的边界（2026-09-18 实测）**：把穿透盖章**整个移除**后，`TestE2BLiveSyncReadsNoBodiesForStampablePaths`
> 仍然读到 **0** 个整对象 —— 判据的 ±1 秒容差（`sameVersion`）把"store 写与镜像写落在同一秒"这件事盖住了，
> 所以那条读数是**测量**而非反证。盖章真正的钉法是单测 `TestWriteThroughStampsTheSandboxCopyWithTheStoreTime`
> （钉住命令里的那个时刻）与真机 `TestE2BLiveHydrateKeepsStoreStamp`（[11 §10](./11-change-register.md) 的 10-4/10-5/10-8）。

> **G24（2026-09-19，跨副本轮次事件中发现）** —— 义务 `—`（**F1 前置条件**：工具写 store 这条路）：
> 工具的 `workspace.Store.Put` **没有任何前置条件**（last-writer-wins）。T1 把 `sandbox→store` 回写变成了
> 带前置条件的对账（不同字节 ⇒ `BLOCKED` 拒绝 + 报告），但 **`tool→store` 这条路径从来没被检查过**。
> 当同一会话存在两个写者（跨副本并发轮次）时，同一路径被直接覆盖：2026-09-18 实测同一份交付物被写两次
> （pod B 16:44:07 **15348 字节** → pod A 16:50:16 **11492 字节**），**前一版内容丢失**，而且**沉默**——
> 没有任何一方被告知。
>
> **修法**：主修**不在本层**——跨副本 turn 租约让"第二个写者"根本不存在
> （[docs/session-turn-integrity.md](../session-turn-integrity.md) A1）。本层两级保险：
> ① **检测并报告**（写入前后各一次 `Stat` 比 `size+mtime`，无接口变更，把静默丢失变成 σ）；
> ② **条件写**（`ObjectInfo` 加 `Version`；`PutIfVersion` + `ErrVersionConflict`；3 个实现 + **11** 个
> `.Put(` 调用点），冲突 ⇒ 拒绝 + 报告——与 T1 的 `BLOCKED` **同一条政策**。**2026-09-19 决定：走 ②（B 族），清单 B1–B11**（`Move` 已是同姿态：
> 拒绝覆盖非空目标 `ErrMoveDestinationExists`）。
>
> **备注（上游）**：这条缺口的上游是"**一次写在并发下意味着什么**"从未被声明（同一端口上 `LocalFS` 是
> `O_TRUNC` 原地写、`S3.Move` 自称 "Not atomic"）。它是形式化意义上的第四套候选，分析与重开条件见
> [00 §7](./00-formal-systems.md) 的备注与 §7.1「原始设计」；**当前决定：不落地**——A1（去掉第二个写者）
> + A3（检测/条件写）已足够。
>
> **控制动作形态：提供**（写动作被提供了，但它是覆盖式的——前一版丢失且沉默）

> **G25（2026-09-19，设计跨副本轮次租约时逐条核对既有租约发现）** —— 义务 `—`（**F1 的机制层**：
> 租约本身作为前置条件的见证）：沙箱租约的围栏令牌 **每代归 1**——`AcquireSandboxLease` 的抢占与插入
> 两条语句都写死 `epoch = 1`（`internal/store/sandbox_leases.go:72` / `:81`），只有续租/替换才自增
> （`:118-124` / `:151-160`）。于是 `epoch` 表达的是"本次持有周期内续租了几次"，不是"这一行第几代"。
>
> **实测反例**（探针，跑完即删）：同一个 `owner`（`host:pid`，可重复）在上一代过期后重新取得同一 scope，
> 世代 2 的 `epoch` 回到 `1`；此时携带世代 1 令牌的迟到释放 `ReleaseSandboxLease(scope,"pod-a",1)`
> 返回 **`released=true`**，**新世代的活行被删除**（随后 `GetSandboxLease` 为 `nil`）。
> 这也解释了同一份文档为什么自相矛盾：`docs/sandbox-pool-leases.md:87-89` 承认"只在持有周期内单调"，
> 而 `:237-240` 说"任何迟到的销毁都会 fail closed"——后者为假。
>
> **归属与修法**：它不是 F2/F3 的缺口，而是 [12 §3](./12-lease-formal-design.md) 的 **L4(c)**
> （围栏令牌必须**逐次获取唯一**：要么持有者身份内嵌一次性 nonce，要么令牌在行的整个生命期内严格单调）。
> 修法一 clause：抢占分支改 `epoch = epoch + 1`（插入分支保持 `1`，首行没有前驱），
> 反证测试 = "两代接管后令牌严格递增"，把该 clause 改回去即红。
> `session_turns` 不继承这个弱点：它用 `<pod>/<uuid>` 作持有者（逐次唯一），并要求令牌永不复位——见
> [12 §6](./12-lease-formal-design.md)。
>
> **已修（2026-09-19，工作区）**：抢占分支改为 `epoch = epoch + 1`（`internal/store/sandbox_leases.go:69-80`），
> 插入分支保持 `1`；`TestSandboxLeaseEpochNeverResetsAcrossTakeover` 钉住"两代接管后严格递增 +
> 老令牌释放被拒 + 活行仍在"，**反证已实跑**（把 `epoch = 1` 改回去即红）。文档同步：
> [../sandbox-pool-leases.md](../sandbox-pool-leases.md) 的 U 条款与 "Hardening" 段已从"未覆盖"改为"已修"。
>
> **控制动作形态：提供**（围栏令牌被提供了，但它不围栏）

> **G26（2026-09-19 发现，为回答"我们说好的是 serverless fastagent——有没有设计违背了它"）** — 义务 `—`
> （**E 桶：保留策略**；这是 harness 自身的内存驻留，不是 σ）：在**生产真正在跑的那个构建**里
> （`a24c0a8`，镜像 tag `20260917035926-fastagent-a24c0a8`，pod 启动于 2026-09-17T04:02Z），
> `session.Manager.sessions` 是一个**无上限**的进程内 map —— `internal/session/manager.go:389` 与 `:446`
> 各有一处 `m.sessions[key] = s`，而**全仓没有任何淘汰路径**
> （`git show a24c0a8:internal/session/manager.go | grep -n 'sessionCacheMaxSize\|evictIdleLocked'` 无输出）。
> 进程服务过的每一个 (agent, session) 组合都会一直驻留到进程结束，并各自带着那一整个 LLM 可见工作集
> （`Session.Messages []provider.Message`）。也就是说它的规模由**"这个 pod 到现在服务过多少历史"**决定，
> 而这恰恰是 serverless 进程绝不能有的性质。两个生产副本同一分钟启动、**0 次重启**，footprint 却不同：
> 2026-09-19 `kubectl -n production top pod` 读到 **96Mi**（62mx9）与 **88Mi**（vxrcx），均已运行 2d9h。
>
> **修法（已落在工作区，尚未部署）**：LRU 上限 —— `agentSessionCacheMaxSessions = 10`（**每个 agent** 的预算；名字里带 agent 是因为作用域就是它，见 §10.7），调用方正在用的那个会话与
> 任何有在途工作的会话永不被淘汰 —— 外加每 100 次穿过缓存的 `Get` 打一行 footprint 日志。
> `TestSessionCacheEvictsIdleEntriesAndRebuildsThem` 钉住"被淘汰的条目会从权威 store 重建（所以淘汰不可观测）"，
> `TestSessionCacheKeepsSessionsWithWorkInFlight` 钉住"在途状态永不被丢"。
> **第一版实现是纯 idle TTL，被测试当场抓住**：当每个条目都刚被碰过时没有一个是"空闲"的，map 照样涨到
> 18 > 10。idle TTL 只是**偏好**，只有淘汰才**碰到上限**。
>
> **预算的单位是「会话数」—— 2026-09-19 在两种备选都实测过之后定下。** 字节预算与行数预算都实现过、
> 又撤掉了：字节预算需要在每一条变更路径上维护一个估算值（哪条路径忘了，估算就漂），而行数同样不是固定
> 大小。会话数是这个缓存能**精确且廉价**执行下去的单位，而它要约束的东西 —— 这个 pod 会攒下多少条目 ——
> 本来就是个数。`TestSessionCacheBudgetIsCountedInSessionsNotSize` 把这个单位钉住：即使每个会话都是
> 64 KiB 的重会话，缓存也会恰好停在预算数上（**反证已实跑**：把闸门改成按行数，缓存停在 51，测试当场变红）。
>
> **单位与作用域，2026-09-19 决定** —— 预算是**每个 agent 10 个会话**（`agentSessionCacheMaxSessions`；
> Manager 本来就是每个 agent 一个，所以"按 agent"是一个真实的作用域，常量名现在也把它写出来了 ——
> pod 级的备选与它的代价记在 §10.7）。10 这个数是有意取小的：缓存唯一省下的是分配开销，因为 `Get`
> 在**每一次**调用都从 store 重读工作集，所以"热着 10 个会话"和"热着 100 个"买到的是同一样东西。
> 残留是量出来的、不是藏起来的 —— 会话之间大小不等（同样 10 个会话的代价可以是 2.3 MiB 到 77.1 MiB，
> 见 §10.3）—— 而淘汰现在是常态而非例外，这正是 §10.6 那份逐字段证明存在的原因：丢掉一个条目不可观测，
> 只有一个字段例外，`snapshot`（§10.5），那是这个上限被接受的代价。
>
> **控制动作形态：—**（资源上界问题，不是形态问题）

> **G27（同一轮审计发现；当天即修 —— 见下方补记）** — 义务 `—`（**E 桶：已退役的资源**）：两张按 agent 建的表会永久保留**已经结束**的
> 工作的条目，且各自**一个 `delete` 都没有**：`tools.shellManager.shells`
> （`internal/agent/tools/bash_session.go:157`）与 `tools.sandboxJobs.live`
> （`internal/agent/tools/sandbox_background.go:104`）。两者只在 `Start` / `start` 里**加**，
> 全仓 `rg 'delete\(m\.shells|delete\(s\.live'` **零命中**。代码自己就写着这件事：
> *"we deliberately do NOT remove the session from the map here. bash_output remains useful after exit …
> Registry.Close handles cleanup, or a future TTL eviction can be layered on top."*
> 但 `Registry.Close()` 在**生产里零调用点** —— 全仓仅有的两处在测试里
> （`internal/agent/workspace_signal_e2e_test.go:91,124`）。Registry 在 `newAgentWithActor`
> （`internal/agent/loop.go:338`）里**每个 agent 建一次**，所以在生产里这张表的生命周期就是 agent 的，
> 而 agent 的生命周期是 `UserSpace` 的 —— 30 分钟 idle TTL，且每次使用都会续期。
>
> **代价**：host 模式的后台 shell 各持一个上限 `bufferCap = 4 MiB` 的 `outputBuffer`（`bash_session.go:25`），
> 所以最坏情况是 **4 MiB × 这个 agent 至今启动过的每一个 host 后台任务**。sandbox 那张表的条目很小
> （一条路径、一个读游标、一个 runner）—— 同一形态，重量更低。要走到 host 这条路径还需要
> `run_in_background` **且** `useSandbox == false`（未绑定 executor 时 `internal/agent/tools/exec.go:292`
> 会拒绝在 sandbox 路径上跑后台），所以它**能涨多大**取决于部署；但它**无上限**这一点不取决于部署。
>
> **已修（2026-09-19，工作区）** —— 退役挂到**真正会发生**的那个转换上，因为原来指望的那个
> （`Registry.Close`）在生产里根本不可达：
>
> | 表 | 何时退役 | 留什么 | 上限 |
> |---|---|---|---|
> | `shellManager.shells` | 收尸 goroutine，在 `cmd.Wait` 返回后立刻 | 足以解释这次退出的尾部 | `shellExitedTailBytes` = 每 shell 64 KiB · `shellRetainedExited` = 每 agent 32 个已退出 shell |
> | `sandboxJobs.live` | 第一次观测到 `exited` / `missing` 的那次 poll（sandbox 里没有进程句柄可等，**poll 就是那次观测**） | 条目本身（一条路径 + 一个读游标） | `sandboxRetainedFinished` = 每 agent 64 个已结束任务 |
>
> 两张表都**永不遗忘正在跑的工作**。收缩发生在 `done` 发布**之前**，所以一个看到 "exited" 的读者不可能
> 拿到一段尾巴却不知道那是尾巴 —— 而且告知它的那句话说的是**真实原因**（*"is no longer buffered (the
> shell exited; only its last 64 KiB is kept)"*），没有沿用 4 MiB 运行期上限那套措辞：那会对一次真实的
> 丢失给出错误的解释（08 §2.2，O1）。
>
> **代价明写**：对一个已被遗忘的任务调用 `bash_output`，回答与"这个 id 从未存在"完全一样 —— 那句话本来
> 就写着 id 只在同一 agent 进程内有效，而诚实的契约是"最近 N 个已结束任务仍可寻址"。上限（32 个 shell /
> 64 个 sandbox 任务）高到正常工作会话根本碰不到；它存在的意义是让这张表不再由历史决定大小。
>
> 测试：`TestRetiredShellKeepsOnlyItsTail`（200 KB 输出 → 只留 ≤64 KiB，结尾仍在，读者被告知退出原因）、
> `TestRetiredShellsAreCappedAndRunningOnesSurvive`、`TestSandboxJobsForgetFinishedJobsBeyondTheRetention`、
> `TestSandboxJobsNeverForgetARunningJob`。**反证均已实跑**：关掉收缩 ⇒ 第一条在 *"retired shell holds
> 200022 bytes"* 变红；关掉 shell 上限 ⇒ 第二条超时；关掉任务表上限 ⇒ 第三条在 *"72 finished entries"* 变红。
>
> **控制动作形态：时长**（该退役而不退役：表会永久保留已经结束的记录）

> **G28（同一轮审计发现，次要）** — `session.StoreAdapter.ownerCache`（`internal/session/store_adapter.go:55`）
> 是一个既无上限也无淘汰的 map：该 adapter 解析过的每个 `session_key` 一条。条目极小（一个键 → 一个用户 ID），
> 且 adapter 随它的 `UserSpace` 一起消亡，所以这是备注而不是缺陷 —— 记下来是因为这条审计判据必须**一视同仁**地
> 施加到每一处，而不是只在预期有问题的对方检查。
>
> **控制动作形态：—**（资源上界问题，不是形态问题）

> **G30（2026-09-19 发现，当天定性）** — 义务 `—`（**S1 的作用域**）：`sessionCacheMaxSessions` 读起来像
> 一份 pod 级配额，实际是**按 agent** —— `internal/gateway/userspace.go:1115` 每个 user space 建一个
> `agent.Manager`，`internal/agent/manager.go:246` 再为**每个 agent** 建一个 `session.Manager`，所以载入了
> K 个 agent 的 pod 最多持有 K × 预算。**按 agent 就是它应有的作用域**（值得热着的是这个 agent 自己的会话）；
> 错的只是名字与注释在邀请人们按 pod 级去读。字段现在叫 `agentSessionCacheMaxSessions`，pod 级备选的代价
> 记在 §10.7，于是这个选择是被文档化的，而不是靠默认值暗示的。
>
> **控制动作形态：—**（命名/作用域问题，不是形态问题）

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

## 10. harness 自身的内存驻留：回答"有没有设计违背 serverless"的判据（2026-09-19）

"我们说好的是 serverless fastagent，那为什么 pod 内存会随会话轮次一直涨？"既不是 σ 问题（F2/F3），
也不是前置条件问题（F1）。它是这套文档反复撞见的**第四类问题**：*一个进程允许在内存里留住什么，留多久？*
它属于 [00 §7](./00-formal-systems.md) 的 E 桶，落在"保留 / GC 策略"那一行。

### 10.1 判据

施加到 gateway 里**每一个进程内容器**上：

| # | 判据 | 什么情况下算不通过 |
|---|------|------------------|
| **S1** | 它的规模由**在途工作量**决定，而不是由"这个进程服务过多少历史"决定 | 每见过一个会话 / 一个回合 / 一个任务就多一条 |
| **S2** | 凡是**能从持久化 store 重建**的东西，都不该是内存里的唯一副本。判定按**字段**做，不按结构体做（见 §10.5 —— 这条就是本轮审计补进方法的） | 任意时刻丢掉一条会改变可观测结果 |
| **S3** | **已退役**的资源（跑完的任务、死掉的会话）必须可丢，且不因此丢掉别人还需要的事实 | 设计成"怕它以后还要问，就永远留着" |

下面要用到一个推论，也正因为它才有 §10.3：**条数上限只是字节上限的代用品，而代用的好坏取决于最大的那个元素。**
"最多 128 个会话"只有在"一个会话本身有界"时才真的约束了内存。

### 10.2 审计（每一个容器 · 判定 · 证据）

| 容器 | 被什么约束 | 判定 |
|------|-----------|------|
| `session.Manager.sessions` 及每个 `Session.Messages` | 工作区版本是**每个 agent LRU 10**；**生产在跑的构建里什么都没有** | **在 `a24c0a8` 里不通过 S1**（G26；工作区已修）。它的字节上限仍未落地 —— 见 §10.3 |
| `tools.shellManager.shells` | 在跑的构建里无约束；**现在**：退出即退役（收缩到尾部）并封顶 32 | **曾不通过 S3**（G27），**2026-09-19 已修** |
| `tools.sandboxJobs.live` | 在跑的构建里无约束；**现在**：观测到结束的那次 poll 即退役，封顶 64 | **曾不通过 S3**（G27），**2026-09-19 已修** |
| `session.StoreAdapter.ownerCache` | 无约束；随它的 `UserSpace` 一起消亡（30 分钟 idle TTL） | 次要（G28） |
| `gateway.userSpaceRegistry.spaces` | 30 分钟 idle TTL，使用即续期（`idleTTL`、`startEvictor`） | **通过 S1** —— 由*并发用户数*约束，而"持有一个 space"正是"这个用户正在工作"的意思 |
| `gateway.dedup`（`sync.Map`） | TTL 60 秒 + 每 30 秒扫一次（`cleanupDedup`，在 `gateway.go:853` 启动） | **通过** |
| `gateway.deferredTurns.items` | 每秒 drain 一次（`run`），`maxWait` 5 分钟，过期会发 σ | **通过** |
| `agent.EventHub.subs` | 退订即删除，且三处生产订阅点都 `defer unsubscribe()`（`internal/setup/handlers.go:1284`、`:1566`、`handlers_team_chat.go:187`） | **通过** |
| `gateway.modelCostCache.cache` | 键是配置里的 (provider, model) 对 | **通过** |
| `sandbox.E2BExecutorPool.executors` / `leaseEpochs` | 每个 scope 一条；release / sleep 时由 `takeExecutor` 删除 | **通过 S1** —— 由持有活租约的 scope 数约束 |

三处不通过共享同一个形态：**一张按历史增长的键表，没有退役路径。**

### 10.3 能测到什么，测不到什么

"去读线上 footprint"是个错误的计划，用户叫停得对。那行进程内 footprint 日志**不在已部署的构建里**
（`a24c0a8` 早于它），而且 `a24c0a8` 与工作区都**没有注册 `net/http/pprof`**
（`rg 'pprof|expvar|/metrics' cmd internal` 无命中），所以不额外发一个端点就**无法**从活着的 pod 里取堆剖面。
**不部署任何东西**就能拿到的：

| 手段 | 能给出什么 |
|------|-----------|
| `git show <生产tag>:<文件>` 再用 `rg` 找 `delete` / 淘汰 | **定性**答案：哪些容器随历史增长。这是源码的性质，不是运行中 pod 的性质 |
| `kubectl -n production top pod` + `.status.startTime`、重启次数、镜像 tag | 症状及其形状：同一分钟启动，footprint **不同** |
| 本地探针驱动真的 `Manager`，读 `runtime.ReadMemStats` 差值 | **每会话字节数** —— 把"N 个会话"换算成"N 字节"的系数 |
| 对生产 store 的一次只读查询（`sessions` 行数、消息字节数） | 这个 pod 实际被要求留住多少历史 |
| `kubectl exec … kill -QUIT 1` → `kubectl logs` | goroutine 转储。查 goroutine 泄漏有用，查堆没用 |

**实测**（本地探针，用真的 `session.Manager`，前后各 `runtime.GC()`；跑完探针即删）。缓存会吃满上限，
所以每一行都是"128 个会话常驻"：

| 每个会话的消息形状 | 常驻堆 | 每会话 |
|---|---|---|
| 20 条 × 512 B | **2.3 MiB** | 18.5 KiB |
| 40 条 × 2 KiB | **12.1 MiB** | 96.8 KiB |
| 30 条 × 10 KiB（≈300 KiB 文本 —— 大致就是压缩后留下的量） | **38.5 MiB** | 308.4 KiB |
| 60 条 × 8 KiB（工具输出很重） | **62.1 MiB** | 496.7 KiB |
| 60 条 × 10 KiB | **77.1 MiB** | 616.8 KiB |

**对着生产 store 实测**（只读；经由 `production` 命名空间里一个临时 `pgprobe` pod，查完自删 ——
数据库在 VPC 内网，外面 `psql` 连不进去）：

| 生产 `sessions` | 值 |
|---|---|
| 行数 | **108** |
| 最近 2 天更新过的行 | **5** |
| `messages` 的存储尺寸（`pg_column_size`，即 TOAST 压缩后） | 合计 **9.1 MiB** · 最大 449 KiB · 均值 87 KiB |
| `messages` 的文本尺寸（`octet_length(messages::text)`，即进程实际持有的量） | 合计 **31 MiB** · **最大 1.85 MiB** · 均值 297 KiB |

**要看的是第二行，只看第一行就会判断错**：JSONB 在磁盘上是 TOAST 压缩的，所以存储尺寸把常驻尺寸
低估了 3.4 倍。进程真正持有的是那份*文本*：整个数据集 **31 MiB**。再加上每条消息的 Go 结构体开销，
并且在 `GOGC=100` 下（部署 env 里没有 `GOMEMLIMIT`）运行时的目标是**活堆的两倍**，结果就落在
**80–100 MiB** 这一带 —— 这正是四个在跑的进程所在的位置。而跨两个构建、两个差别很大的运行时长仍在
同一条窄带里（生产 `a24c0a8` 跑 2d9h：96Mi / 88Mi；开发 `8984c99` 跑 21h：87Mi / 82Mi），
原因只有一个：一个**有限**的数据集（约 31 MiB）被每个副本整份缓存。按服务历史线性增长的容器不会出现
平台期；这里的天花板*就是*数据集本身，正是这一点让"无上限缓存"成为主项，而不是一次缓慢泄漏。

> 两个环境**并不共用同一个库**（各自 `STORAGE_DSN` secret 的哈希不同），所以 31 MiB 是生产的数字；
> 窄带是两个环境共同呈现的*形状*，开发的库更小。窄带在两边含义相同：天花板由数据决定，不由运行时长决定。

> **"预期十几 M"对这个二进制从来就不可达。** 地板不是"一个空进程"，而是一到多个 `UserSpace` 常驻
> 的东西（agent，各自带着已加载的技能、提示词模块、记忆与完整工具目录）加上 Go 运行时与这份数据集。
> 有用的目标不是一个凭直觉选的数字，而是**一个带机制的预算**（见下面第 2 条与 G27）。

两条结论，外加第二条逼出来的那个决定：

1. **无上限那一版在数量上解释了症状。** 生产在跑的构建把它加载过的每一个会话永远留着；它能持有的
   数据集约 **31 MiB 文本**，由此得到的活堆在 RSS 上大体就是它的两倍。
   有一条源码事实解释了*一个会话*为什么能这么重：最大的单个会话是 **1.85 MiB 文本 ≈ 46 万 token（按
   `EstimateTokens`，`len(Content)/4`，`internal/agent/compaction.go:31`）**，即**远远超过 8 万 token
   的压缩触发线** —— 因为触发线只统计 `Content` 与工具调用参数，而存储（因而常驻）的东西还带着
   `Metadata`、`Thinking` 与 `RawAssistant`。所以"压缩跑过了"并不等于"这个会话很小"。
2. **条数上限不等于字节上限 —— 但预算仍然按「会话数」计（2026-09-19 决定）。** 字节预算与行数预算
   都实现过（`sessionCacheMaxBytes` / `sessionCacheMaxLines`），又都撤掉了：字节预算需要在每一条会变更
   历史的路径上维护估算，而行数并不比会话数更"固定大小"。这个决定买到的是一个缓存能**精确、廉价、可预期**
   执行的约束；它的代价则明写出来而不是暗示：**它限制的是记住多少个会话，不是每个有多重**，而生产今天只有
   108 个会话、而预算按**每个 agent 10** 计，所以它在生产上会真的触发（不再只是防未来增长）。残留仍是量出来的：会话之间大小不等；
   这个决策需要的数字就在上面，而每次淘汰的代价只是 `Get` 本来每次都要做的那一次重建（它无条件从 store
   重读）。

### 10.4 为什么这不是第四套形式系统

F2 说的是"世界发生变更必须让 agent 可观测"。S1/S3 是它施加到 harness 自身内存上的对偶：
**不是关于世界的事实的东西不得驻留；可推导的东西不得作为唯一副本。** 骨架就是 F2 到处都在用的那一个 ——
*从权威来源重算，不要记住* —— 所以这是既有形态的一个新实例（[00 §7](./00-formal-systems.md) 词汇里的 C），
不是"新问题 + 新判断形态"。它真正给清单加上的，是一条必须对每一张新表提出的问题：
**"什么东西会删掉一条记录，那条路径在生产里可达吗？"** G27 就是答案为一个没人调用的 `Close()` 时的样子。

### 10.5 本轮审计反过来补进方法的一条：可重建性是**按字段**的属性

淘汰规则建立在 S2 上：*凡是能从持久化 store 重建的东西，都不该是内存里的唯一副本。* 套到 `Session` 上
看起来显然成立 —— 工作集在每次 `Get` 都从 store 重读，所以条目随时可以丢。但把结构体**逐字段**走一遍
（而不是相信这个概括），就找到了一个字段并不成立：

| `Session` 字段 | 能从 store 重建吗 |
|---|---|
| `Messages` | **能** —— `getByKey` 每次调用都重读它，所以丢掉一个条目不可观测 |
| `channel` / `accountID` / `chatID` / `projectID` / `runReceipt` | 能 —— 都是会话行上的列 |
| `snapshot`（`/retry` 的还原点） | **不能** —— `Undo()` 只从进程内存恢复；没有 snapshot 列，丢掉之后 `HasSnapshot` 直接报 `false` |
| `turnActive` / `turnWaiters` / `steerBuf` / `turnFence` / `fenceLost` | 不能 —— 但它们只在有在途工作时存在，而清扫被禁止淘汰忙会话 |
| `lastTouched` | 不能，但不影响：它是缓存自己的记账 |

所以 S2 的正确表述是**按字段，而不是按结构体**：*对每一个字段，要么它可重建，要么它只出现在清扫不得
淘汰的条目里，要么丢掉它是**写明了的**代价。* `snapshot` 属于第三种，而把它写下来正是要点：

- 淘汰一个会话就丢掉了它的还原点，于是 `/retry` 之后的 `/undo` 可能回答"没有可撤销的内容" —— 这与
  "下一回合被另一个副本服务"时给出的答案是同一个，因为 snapshot 本来就是 pod 局部的。这个约束只是
  多给了一种丢失方式；而这个丢失早已在契约里。方法要求的是把它**写下来**，而不是让人自己发现。

这是本轮审计唯一一处对方法提出异议的地方。它不足以改动某条原则，但把清单磨利了：
*这个结构体里哪些部分**不**可重建，代价由谁付？* —— 这一问现在已经并入 §10.1 的 S2。也正是这一问，
如果 `Snapshot()` 是每回合都调用（而不是只有 `/retry` 调用），就能拦住"整结构体淘汰悄悄丢掉撤销状态"。

### 10.6 当预算真的生效时：淘汰安全性审计

预算是**每个 agent 10 个会话**，而生产里一个 agent 的会话多于 10 个，所以淘汰是**常态**而不是例外 ——
正因为如此，"store 能重建"这句话不足以作数，必须换成核查。"丢掉一个条目不可观测" 是**逐字段**
对着读这些字段的代码验出来的：

| `Session` 的状态 | 条目被丢时是否丢失 | 为什么不可观测 |
|---|---|---|
| `Messages` | 丢失 | `getByKey` 在**每一次**调用（命中或不命中）都从 store 重读工作集（`internal/session/manager.go:625`）—— 所以重建出来的条目与热条目完全一致，而淘汰**不产生任何额外 store 流量** |
| `channel` / `accountID` / `chatID` | 丢失 | 路由经 `resolveOrMintKey` 解析，它问的是 **store**（`ResolveActiveSessionKey`），从不问内存 map —— 所以淘汰不可能给同一个会话铸出第二个 key |
| `projectID` | 丢失 | `SaveSession` 的 `ON CONFLICT … DO UPDATE` 有意**不含** `project_id`（`internal/store/database.go:2985`），所以带空 hint 重建的条目既不能抹掉行上的项目，也不能复活旧项目；面板与回合入口都经 `LookupSessionProject` 从 store 解析项目 |
| `runReceipt` | 丢失 | 环境基线是从**已存储的消息元数据**读出来的（`session.RunReceiptOf` → `envBaselineFromReceipt`，`internal/agent/env_changes.go:352`）—— G9/G20 当初把它搬进 receipt 正是为了这个 |
| `LastConsolidated` | 丢失 | 没有任何地方读它：`UnconsolidatedCount` 与 `MarkConsolidated` 全仓**零引用**（G29） |
| `snapshot`（`/retry` 的还原点） | 丢失 | **这一条是*可观测*的** —— 见 §10.5。它是这个上限被接受的代价 |
| `turnActive` / `turnDepth` / `turnWaiters` / `steerBuf` / `turnFence` / `fenceLost` | 不丢 —— 清扫跳过忙会话 | 而且残留的 steer 不可能活过一回合：`EndTurn` 把它交还，`flushLeftoverSteer` 把它落到历史里（`internal/agent/loop.go:2150`、`:2236`） |
| `lastTouched` | 丢失 | 缓存记账 —— 它评价的那个条目已经不存在了 |

在运行预算下钉住这件事的测试：`TestEvictionIsUnobservableAtTheOperatingBudget`（40 个会话 → 预算守住，
而且每一个重新读回来仍是它当初说过的内容；**反证已实跑**：关掉淘汰，它在
*"cache holds 40 sessions, want <= the budget 10"* 变红）、`TestSessionCacheEvictsIdleEntriesAndRebuildsThem`、
`TestSessionCacheBudgetIsCountedInSessionsNotSize`。

这轮审计留下了两样超出它自身的东西：

- **一条带理由的立场记录** —— 每个 agent 10 个：因为缓存的唯一产出是省下的分配（无论如何每次 `Get`
  都要读 store），小的热集与大的热集买到的是同一样东西；也因为**会触发的上限才是被检验过的上限**；
- **G29**：`LastConsolidated` 和它的两个访问器（`UnconsolidatedCount`、`MarkConsolidated`）是本包里最热的
  结构体上的死状态 —— 没人读、没人持久化，而那四处把它清零的代码是在为一个不存在的读者服务。
  这里只记录不删，因为"零引用"已经两次只是线索而不是判决（10 §9）。

### 10.7 作用域：按 agent，以及 pod 级预算要付什么

2026-09-19 决定：预算**按 agent**（`agentSessionCacheMaxSessions = 10`）。另一个形状被考虑过，记在这里，
因为这个常量过去读起来像是已经是 pod 级的：

| 形状 | 约束什么 | 代价 |
|---|---|---|
| **按 agent（已选）** | 单个 agent 的热会话；一个载入了 K 个 agent 的 pod 最多持有 K × 10 条 | 无额外代价：Manager 本来就是每个 agent 一个，淘汰是局部的 |
| pod 级 | 整个进程，一份共享预算 N 条 | 需要一个由组装根创建、每个 Manager 注册进去的共享预算对象，以及一次全局清扫（从所有 Manager 收集候选，丢掉全局最老的） |

两个隐患让 pod 级成为一次真正的改动而不是改一个常量，这也是它被记录而不是悄悄实现的原因：

1. **锁序。** `getByKey` 现在是持着 `m.mu` 插入并清扫的；全局清扫会先持缓存锁、再逐个取 `m.mu`。
   per-Manager 的清扫必须先停止在 `m.mu` 下运行，否则两种顺序在负载下会死锁。
2. **生命周期。** Manager 注册表本身就是个无界结构，除非有东西把它们注销 —— 而今天没有任何东西会
   拆掉一个 `session.Manager`（与 G27 里 `Registry.Close` 是同一种"没有拆卸路径"的形状）。为了修一个
   无界缓存而引入注册表、却没有拆卸路径去移除条目，等于用一个无界结构换另一个。

预算的 per-Manager **分摊**（预算 ÷ 在线 Manager 数）能同时避开这两个隐患，但会让上限随负载漂移 ——
那比一条写明的 per-agent 上限更糟：运维能对"每个 agent 10 个"做推理，没人能对"N ÷ 此刻载入了多少 agent"
做推理。

### 10.8 另一处缺失的拆卸，被决定而不是被默认

G27 点出了第二件"没有拆卸路径"的事实并把它留成了问题：`Registry.Close()` 至今没有生产调用点，所以当一个
`UserSpace` 因闲置被驱逐时，该 agent 的 host 后台 shell **不会被杀** —— 它们继续跑（现在每条最多留
64 KiB，见 G27）。

**决定（2026-09-19）：不接。** 因为用户安静了三十分钟就杀掉他的后台进程，是在改一条产品承诺；而唯一不会
造成"沉默的破坏"的版本，是**同时把它说出来**的那个版本（一条 σ："你的后台任务被回收了"）—— 那需要一个
写明了的闲置参数和一个投递点。在它存在之前，"不接"才是诚实的选择：资源代价有界且可见，而"接上"会让用户的
dev server 无声消失、没人解释。等"闲置回收"这条承诺被定义时再重开；参数就用现有的 30 分钟 idle TTL，
不要再引入新旋钮。
