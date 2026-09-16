# 容器里出现别人的文件：排查记录（2026-09-16）

**现象**：`exec` 里的 `/workspace` 是另一个项目的 kronos 文件 + 两个旧脚本，时间停在约 9/14；你自己的脚本、累积库、文档在文件工具通道可见，`exec` 里不存在。数据没丢，只有"在容器里执行"这一步失败。

**来源**：用户在 prod 观察到，由 cron job 触发的轮次。

---

## 1. 两个假说，以及它们如何被证据杀掉

| 假说 | 结论 | 证据 |
|---|---|---|
| cron 的 `chat_id` 为空 → `poolKey` 退化 → 不同 session 共用一个容器 | **证伪** | `cron_jobs` 全表 3 行，`empty_chat = 0`；三个 `chat_id` 都非空且与日志里的 scopeKey 一一对应 |
| 「续租机制让不同 session 共用容器」 | **证伪（该窗口内）** | 72h 内两个 pod 上**全部** scopeKey 都带 `:s:`，互不相同，没有退化成 `agent` / `agent:p:*` 的 |

DB 证据（只读查询）：

```
 total | empty_chat
-------+------------
     3 |          0

 id                  | agent_id                 | chat_id              | channel
 c9845fad-…          | agt_cda27bbfbf4a84e2dfa6 | YhHDu15XElTDGxW1tz6Vj3 | web
 b48176eb-…          | agt_cda27bbfbf4a84e2dfa6 | hJKMWwtOp3mJOtqN8Uz2mW | web
 9e432939-…          | agt_f8e9acf8088825b4acbf | ac978dfb-a4d5-…        | web
```

这三个 `chat_id` 就是日志里三条 scopeKey 的 `:s:` 片段。**cron 侧不需要"补 session id"** —— 那条修复从计划里划掉。

## 2. 探测方法（可复用）

网关镜像里没有 psql（`NO_PSQL`），DSN 在 secret `fastagent-secrets#STORAGE_DSN`（env 名是 `FASTAGENT_STORAGE_DSN`）。一次性跳板：

```bash
kubectl --context do-nyc2-tokenaissance-nyc2 -n production run cron-check --rm -i --restart=Never \
  --image=postgres:16-alpine \
  --overrides='{"spec":{"containers":[{"name":"c","image":"postgres:16-alpine","command":["sh","-c","D2=$(printf %s \"$DSN\" | sed -E \"s/([?&])default_query_exec_mode=[^&]*//g; s/\\\\?&/?/; s/[?&]$//\"); psql \"$D2\" -c \"select … from cron_jobs\""],"env":[{"name":"DSN","valueFrom":{"secretKeyRef":{"name":"fastagent-secrets","key":"STORAGE_DSN"}}}],"restartPolicy":"Never"}]}}'
```

两点坑：`envFrom` 注入的是**原始键名**（`STORAGE_DSN`，不是 `FASTAGENT_STORAGE_DSN`）；DSN 带 pgx 专有参数 `default_query_exec_mode`，psql 不认，必须剥掉。查询是只读 SELECT。

## 3. sandbox 时间线（72h，两 pod 合并）

```
03:54:37 created  i58cyeltdzm65ic0slraj   → hydrated workspaceFiles=5
04:00:07 created  iru7gjxb7fu6vtwlml3ti   → hydrated workspaceFiles=1228  ← 那份完整工作区
04:05:19 paused   i58cy…
04:46:59 paused   iru7…   |  07:42:00 paused iru7…
07:44:12 ADOPTED  iru7…  scopeKey=agt_cda27…:s:hJKMWwtOp3mJOtqN8Uz2mW  owner=…6pzbl:1
09:14:35 created  iasoct5ccvmg55ceqsqeu   → hydrated workspaceFiles=5
09:23:20 paused   iru7…
09:23:31 exec 失败: the connection to sandbox iru7… ended before the stream completed
09:27:50 / 09:28:41 paused iasoct5c…
09:30:04 created  igh1bjnj3y8q8m1qgpiy4   → hydrated skills=6 skillFiles=71 workspaceFiles=0（另一个 agent）
09:40:42 paused   igh1bjnj…
09:45 → 10:26   iru7… 被反复 pause
```

## 4. 证据支持的两个机制（都与 key 无关）

**M2 · pause 与在飞的 exec 抢时序（直接可见）**
`09:23:20 paused iru7…` → 11 秒后 `09:23:31 exec` 报 `connection ended before the stream completed`。空闲 TTL 到点把实例**在使用中或刚要用时**冻结，流就断在半路。同一 agent 在 09:40 / 11:01 / 12:01 / 12:02 反复 `did not exit cleanly … response stream truncated`，形状一致。

**M1 · hydrate 出来的内容与该 scope 的预期不符（待闭合）**
同一个 agent 下，`iru7…` 一次性 hydrate 了 **1228 个**工作区文件；而 `i58cy…` 与 `iasoct5c…` 两个实例都只 hydrate 出 **5 个**（42MB 的 tar 主要是 skills，说明几乎没有真实工作区文件）。若这两个 5 文件实例属于**本该是 1228 那个 scope**，那就是 hydrate 取的 scope/内容不对 —— 这正好能解释"脚本、累积库不在容器里"。

还缺的一步：`e2b sandbox created` 日志**不带 scopeKey**（只有 `adopted` 那行带），所以要闭合 M1，需要给 `created`/`hydrated` 两行补上 scopeKey（一行日志的改动），或者用同一时刻的 `chat/history`/轮次日志反查当时是哪个 scope。

## 5. 建议（按证据强度排序）

| 优先 | 动作 | 依据 |
|---|---|---|
| P0 | `created` / `hydrated` 日志补 `scopeKey` 与 `projectID`（一行） | M1 无法闭合的唯一原因就是这条日志缺字段 |
| P0 | 空闲 TTL 与在飞 exec 互斥：pause 前检查该实例的 in-use 计数（`lifecycle.go` 已有 `inUse`），或 pause 失败/中断时把实例标记为不可用并重建 | M2 直接可见 |
| P1 | hydrate 幂等校验：命中实例后比对文件数（或写 scope 戳记），不匹配则强制重推 | 把"attempted"变成"verified" |
| P2 | 容器不做任何产物的唯一副本：脚本/累积库/文档走持久层 | 让此类故障的损失上限从"文件消失"降到"这一轮读不到" |

## 6. 明确**不再**成立的说法

* 「`poolKey` 退化导致跨 session 共用容器」——该窗口内无一条退化 scopeKey，DB 也无空 `chat_id`。
* 「cron 侧需要补 session id」——已有。

---

## 7. 修复设计：把"流被切断"变成一次可解释的重试（③，**已设计、未实现**）

### 7.1 为什么是 ③，而不是 ①②

排查确认（见 §3、§4）：**pause 由我们自己发起且已受保护** —— `evictIdle` 跳过 `inUse[k] > 0`（`lifecycle.go:265`），`beginUse` 在 `ex.Exec` 之前、`endUse` 用 defer 覆盖到后置同步结束（`:523/530`），长操作还有 `extendBudget` 主动延期 TTL（`:355`）。缺的不是仲裁（①②），而是**唤醒/损坏实例上开流失败之后没有任何恢复**：代码对它的处理只有注释里那句 *"the truncation that follows is classified like any other cut stream"* —— **被归类，没有被恢复**。

### 7.2 两个挂点（都沿用仓库既有模式）

**挂点 1：分类留在适配器**（沿用 `ScopeSleeper` / `ScopeExtender` / `workspaceAware` 的接口风格，以及 `SleepScope` 注释里的原则 *"The classification lives here, not in the lifecycle layer"*）

```go
// e2b_executor.go，紧挨 sandboxGone(:881)
func sandboxUnusable(err error) bool   // 现有 sandboxGone(502/404) ∪ unavailable ∪ "ended before the stream" ∪ "did not exit cleanly"

func (p *E2BExecutorPool) Unusable(err error) bool { return sandboxUnusable(err) }

// lifecycle.go
type UnusableClassifier interface{ Unusable(err error) bool }
func (p *LifecyclePool) unusable(err error) bool { c, ok := p.inner.(UnusableClassifier); return ok && c.Unusable(err) }
```

判据要**窄**：只认传输层信号，**不认"命令返回非零"** —— 否则用户脚本的真实失败会被重复执行。

**挂点 2：重试放在 in-use 之外**（避免 `Release` 与 defer 的 `endUse` 互相踩）

把 `lazyExecutor.Exec` 现有函数体抽成 `execOnce`（getInner → beginUse → extendBudget → defer endUse → ex.Exec → 后置同步），外层只做一次重试：

```go
func (l *lazyExecutor) Exec(ctx, command, timeout) (string, error) {
    out, err := l.execOnce(ctx, command, timeout)
    if err != nil && l.pool.unusable(err) {
        // 坏实例要**销毁**，不能走 sleepOrRelease 的 pause 路径 ——
        // 否则重试会唤醒同一个坏实例。
        if relErr := l.pool.Release(sc.agentID, sc.projectID, sc.sessionID); relErr == nil {
            out, err = l.execOnce(ctx, command, timeout)
        }
    }
    return out, err
}
```

`Release` → `inner.Release` 是销毁路径；`evictIdle` → `sleepOrRelease` → `SleepScope` 才是 pause 路径。两者**必须区分**，这是本设计里最容易写错的一处。

### 7.3 用例（红→绿；沿用现有替身与用例风格）

1. 假 E2B 客户端：第一次 exec 返回 `unavailable: … ended before the stream completed`，第二次成功；
2. 断言：①该错误被判为 unusable；②走的是**销毁**而非 pause（对照 `TestE2BExecutorFailedRebuildRestoresIdentityAndDestroysReplacement` 的写法）；③命令**只**重跑一次；④模型只收到第二次的结果；
3. 反向用例：命令返回非零（`exit code 1`）**不得**触发重试。

### 7.4 还可以顺带做的（低优先）

给 `e2b sandbox created` / `e2b sandbox hydrated` 两行补 `scopeKey`：本轮靠"轮次活动时刻 vs create 时刻"才把四个实例与四个 scope 对齐，补上字段后下次一眼可见。注意 `e2b sandbox hydrated` 在 `E2BExecutor.Hydrate`（`:730`），而 `E2BExecutor` 目前**没有** scope 字段 —— 所以这是"给执行器加一个字段"的小改动，不是一行日志。

---

## 8. 2026-09-16 补记：现场钉在"空工作区"，新根因候选是 **store 的 List 超时**

### 8.1 现象被钉死（有数字）

报错发生在 **Quandora quant session**（用户点了"quant 启动"）。它在租约表里的那一行：

```
scope_key  = agt_f8e9acf8088825b4acbf:s:ac978dfb-a4d5-4c72-b7a7-715044991f70
sandbox_id = igh1bjnj3y8q8m1qgpiy4
09:30:04  created  igh1bjnj…                            ← 「quant 启动」那一刻
09:30:04  hydrated skills=6  skillFiles=71  **workspaceFiles=0**
```

**该 scope 的 hydrate 一个工作区文件都没灌** —— 这就是 `refresh_paper_review.py` / 累积库 / 文档"不在容器里"、脚本报 `No such file or directory` 的直接原因。不是"被换成了别人的快照"，是**空工作区**（只有 6 个技能、71 个技能文件）。

### 8.2 "别人的 kronos 文件"这段结论作废（不再是未归因，而是已归因）

kronos 文件属于**另一个 scope**：`agt_cda27bbfbf4a84e2dfa6:s:hJKMWwtOp3mJOtqN8Uz2mW` → `iru7gjxb7fu6vtwlml3ti`，**1228 个文件**，同一时段每 10 分钟在跑 `firing store-backed cron job id=e943aa72-… name="Kronos 宽横截面续跑"`，其 exec 就是 `cd /workspace && … kronos_crypto_wide_one …`。租约表四条 scope ↔ 四个不同 `sandbox_id`，**一对一**。

所以：**没有任何跨 scope 复制**。观感来自"同一台 gateway 上两个 scope 同时活动"（一个空工作区、一个 1228 文件），而不是文件被搬。

### 8.3 新根因候选：对象存储的 `List` 在超时（**当前首选**）

```
09:26:00  WARN "workspace list failed for media fallback" agent=agt_cda27… session=hJKMW… error="context deadline exceeded"
09:35:23  WARN 同上
```

E2B 的批量 hydrate **正是一次 `List` 之后打 tar**（`workspace_hydrate.go` 只服务 docker 那条逐文件路径）。若那次 `List` 超时，tar 里就是 0 个工作区文件 —— 与 `workspaceFiles=0` 吻合。旁证：同一时段另一条路径（渲染用的 media fallback）在**同一个 store**上超时，说明当时列举整体不可靠，不是 hydrate 独有。

**待判 (i)/(ii)**（下一轮一条命令即可分离）：

| 候选 | 判据 |
|---|---|
| **(ii) List 超时**（首选） | 09:25–09:35 窗口内出现 `workspace hydrate: list failed` 或同类 `context deadline exceeded`；且对象存储里**确实有**那些文件 |
| **(i) 键漂移**（写侧与读侧的 `projectID` 不一致） | 对象存储里那些文件落在**别的分区前缀**下（如 `…:p:<proj>:s:…`），而 hydrate 用的是 `…:s:…` |

### 8.4 工具性事实（下次别再走弯路）

* **`agent_files` 不是工作区索引**：列是 `agent_id, user_id, filename, content, updated_at`（agent 身份文件表），Quandora 那个 agent **0 行**。工作区文件只在**对象存储**里 —— psql 看不到它，只能走 S3 或 API。
* 对象存储配置（只列键名）：`FASTAGENT_OBJECT_STORE_{TYPE,BUCKET=fastagent-nyc3,ENDPOINT=nyc3.digitaloceanspaces.com,PREFIX=prod,REGION,USESSL}`。
* 列举跳板的两个坑：**endpoint 必须带 `https://`**（aws-cli 否则报 scheme missing）；**不要 `--recursive` 全列举**（大前缀会超时——那正是我们要查的现象本身），改成按前缀逐层 `ls` 或先 `--summarize` 拿计数。

### 8.5 对修复顺序的影响

* **③（流中断重建+重试）优先级下调**：它治的是"实例不可用"，而本次故障是"容器里本来就没有文件"，③ 帮不上。
* **新 P0：排 store 的 `List` 超时**（对象存储侧：列举耗时 / 对象数 / 客户端超时配置）。这比容器层更根本。
* **P1：hydrate 失败要可判**：`List` 失败时 hydrate 必须留下明确的失败痕迹（现在 docker 路径有 warn、E2B 批量路径只有一个 `workspaceFiles=N` 的数字），否则"空工作区"和"这个 scope 真的没有文件"无法区分。
* **治本不变，且理由更强**：文件工具通道**绕开 hydrate**，所以文档读写不该依赖容器里有文件。

---

## 9. P0 已定方案（**策略 C**）与落地清单 —— 待实现

### 9.1 三种"空"必须分开（判据的地基）

| 情形 | 含义 | 处置 |
|---|---|---|
| **真·空** | `List` **成功**、返回 0 个对象 | **照常放行、不标记、不失败**（否则打断所有新会话） |
| **列不出来** | `List` **报错**（prod 实测 `context deadline exceeded`） | 这才是故障，今天被吞成 `workspaceFiles=0` |
| **以为空、实际有** | store 里有 N 个文件、容器却空 | **最危险**：模型把基础设施故障当成"文件不存在"的世界事实 |

### 9.2 选定策略：**C（放行 + 标注 + 重建）** —— ✅ 已实现

`List` 失败 → **有界重试** → 仍失败：

1. **不返回 error**（那是策略 A：`inner.Get` 报错就没有 executor 可交）；
2. **把 executor 标记为 `workspaceUnhydrated`**，并打一条响亮日志（含 agent/project/session + 原因 + 重试次数）；
3. **scope 不再记为已水合**（`p.hydrated[k] = false`），并在池上记下"这个 scope 当前的实例是空的"，供工具层读取；
4. **把"工作区未水合"声明进这一轮**（③ → ⓑ，已落 `1b0b77e`，契约见 §9.5）；
5. **销毁重建**：把未水合的实例拆掉、重建一个新的，让 hydrate 真正再跑一次。

理由：本次故障的伤害不是"容器是空的"，而是**"空得没人知道"**；C 消灭这个伤害，同时不为瞬时抖动付冷启动代价（销毁 = 丢热实例 + 重传 42–95MB 的 tar）—— 所以重建本身是**受控**的，见下。

#### 9.2.1 与原计划的唯一偏离：第 5 条从"只在矛盾时"改成"一律重建"

原计划第 5 条写的是「**只有与已知基线矛盾时**（曾成功列举过非空、如今列不出来）才升级为销毁重建」，它建立在一个假设上：

> `p.hydrated[k] = false` → 下一次 `Get` **自动重试 hydrate**。

**这个假设在 E2B 上不成立**：`E2BExecutorPool.Get` 命中缓存实例时直接 `return ex`（`e2b_executor.go:1951-1961`，`cachedExecutor` 分支），不会重跑 hydrate。也就是说，一个 listing 失败的实例会**一直被发出去**，直到空闲 TTL 偶然把它回收 —— `hydrated[k] = false` 在 E2B 上因此是个**不产生任何重试的空转**：唯一会重跑 hydrate 的路径就是「实例被重建」。

```go
// E2BExecutorPool.Get —— 有缓存就交出去，不重新 hydrate
if ex, ok := p.cachedExecutor(key); ok {
    if p.leaseStore != nil {
        return p.reconcileLocalLease(ctx, key, ex, agentID, projectID, sessionID)
    }
    return ex, nil          // ← 这里没有 hydrate
}
```

所以第 5 条改成**对所有未水合实例生效**：见到未水合实例就重建（而不是只在"曾非空"时）。代价用两道闸门限制，且两道都落在"不要伤害"这一侧：

| 闸门 | 防的是 |
|---|---|
| `inUse[k] > 0` 时**不重建** | 拆掉正在跑命令的实例 = 把命令的流截断（§4 的 M2）。宁可先发一个空实例（且已声明），等这次操作结束再重建 |
| 同一 scope 每 `rebuildCooldown`（默认 1 分钟）最多重建一次 | store 长时间故障时，每个 scope 每分钟付约 1 次重建，而不是**每次工具调用**一次。§8.3 实测的 10 分钟故障窗口 ≈ 每 scope 10 次重建；而 store 在一分钟内恢复时，下一次调用就能拿回完整工作区，不必等空闲回收 |

**这个取舍是有意的**：store 长时间不可用时，scope 会持续以"未水合 + 已声明"的形态服务。宁可给出一个空容器并说明原因，也不要让 agent 因为读不到 store 而完全不可用 —— 后者会把一次存储故障放大成一次服务中断。

### 9.3 机制侧（✅ 全部已实现）

| 位置 | 改动 | 提交 |
|---|---|---|
| `e2b_executor.go` | `listWorkspaceWithRetry`（复用水合的 attempts/interval）+ `workspaceUnhydrated` + `WorkspaceUnhydrated()` | `b4ffc7b` |
| `e2b_executor.go` | 导出 `UnhydratedWorkspace` 接口 —— lifecycle 层与工具层读同一个形状 | `da37174` |
| `lifecycle.go` | 未水合 → `hydrated[k] = false`（不再静默当作已水合） | `48d8e06` |
| `lifecycle.go` | `recoverUnhydrated`：未水合 → 销毁重建（受 `inUse` 与 `rebuildCooldown` 两道闸门） | `da37174` |
| `lifecycle.go` | scope 级状态 + `lazyExecutor.WorkspaceUnhydrated()`：工具层由此得知状态，**且不会因此触发建实例**（状态问题不该成为开机理由） | `da37174` |
| `registry.go` | ⓑ 声明挂到 6 个工作区工具的返回值上 | `1b0b77e` |

注意保持既有的语义分工：**`List` 失败 = 可检索性故障（致命到"要声明"）**；**单文件 `Get`/`Read`/`tar` 失败 = best-effort**（个别文件坏了不该让整次 hydrate 变成"未水合"）。

**一处仍未闭合的证明**：重建让 hydrate **再跑一次**，但没有任何一步能证明这次 hydrate **真的把 N 个文件放进了容器**（§5 P1「attempted → verified」）。今天能说清的是"listing 成功了"；说不到"容器里因此有了那些文件"。

### 9.4 用例（红→绿，全部可离线）

**进度（2026-09-16）**

| # | 用例 | 状态 |
|---|---|---|
| 1 | `List` 失败一次后成功 → 重试、拿到真实列表、**未**标记未水合 | ✅ `e85e6e8`（先红后绿；红时日志可见 `retrying … attempt=1`） |
| 3 | `List` 成功返回 0 个对象 → **不**标记、**不**失败（防误伤新会话） | ✅ `1b98cd2` |
| 2 | `List` 持续失败 → **拿到 executor** + `WorkspaceUnhydrated() == true` + `p.hydrated[k]` 回到 false | ✅ `da37174`：`TestLifecycle_UnhydratedWorkspaceIsNotRecordedHydrated`（红用变异验证：去掉 `hydrated[k] = false` 即红） |
| 4 | 曾成功列举非空、随后列不出来 → 走**销毁重建** | ✅ `da37174`：`TestLifecycle_UnhydratedWorkspaceIsRebuilt`（断言 release=1、实例被 Close、create=2、拿到的是**另一个**实例、scope 重新记为已水合） |
| 5 | store 持续故障 → 重建**受 `inUse` 限制**、且**受冷却限制**（不会一调用一实例） | ✅ `da37174`：`...RebuildWaitsForInFlightWork`、`...RebuildIsRateLimitedButRepeatable`（后者还钉住"store 恢复后必须能再重建一次"，否则 scope 会一直空到空闲回收） |

**替身的一个坑（用例①踩过，值得记下）**：`fakeWorkspace.put` 只按 **agent** 存，而它的 `List` 按 **scope** 过滤 —— 用例里调用 `listWorkspaceWithRetry` 时的 `(project, session)` 必须与存入时一致（用 `("", "")`），否则会得到 0 个对象、误判成"空工作区"。

**替身（用例②④⑤用的）**：`fakeExecutor` 带一个 `workspaceUnhydrated atomic.Bool`（零值 = 已水合，所以 Policy C 之前的用例照旧走普通路径）；`fakePool` 带一个 `unhydrated` 开关，打开后**每个新建实例都报未水合** —— 就是 store 故障时的形态。

### 9.5 ③ 的注入点：**已定 ⓑ（工具结果前缀）**

**决定**：声明**只挂在碰到工作区的工具**（`exec` / `read_file` / `list` 一类）的**返回值前缀**上；ⓒ（事件 + UI 徽标）作为**补充**让人也看得见，但**不作为唯一手段**（模型看不到就等于退回成这次事故）。ⓐ（每轮系统提示）不采用 —— 它会把一条"这次操作的事实"变成每轮都出现的指令，稀释提示词。

**声明的契约**（三条硬要求）：

1. **是事实，不是指令**：措辞要让模型能读成"这一轮的环境状态"，而不是"你要做什么"，例如：

   ```
   [workspace not hydrated: the store could not list files (timed out after N attempts).
    Files may be missing for this reason only — do not conclude they were deleted.]
   ```

2. **必须与"文件不存在"区分开**：这行字的目的就是拦住"把基础设施故障当成世界事实"（本次事故的伤害本体）。所以它要写明**原因**（列举超时/重试次数），而不是只说"空"。

3. **只在 `WorkspaceUnhydrated()` 为真时出现**，且**只在触碰工作区的工具上出现** —— 不碰工作区的调用（纯计算、纯网络）不该被这行字污染上下文。

#### 9.5.1 ⓑ 已落 `1b0b77e`：把声明接在哪、以什么形状

实现比"在每个工具里加一行"少一层手工：**在 `SetExecutor` 里把 6 个工作区工具的闭包包一层**（`registry.go` 的 `declareUnhydratedWorkspace` / `withWorkspaceNotice`）。这样 `exec` / `read_file` / `write_file` / `edit_file` / `list_dir` / `apply_patch` 的**每一条返回路径**（含错误路径——本次故障正是以 `No such file or directory` 的形状出现的）都被覆盖，规则也只写在一个地方。

```
[workspace not hydrated: the file store could not list this agent's files (the listing
 timed out after retries). Files may be missing for this reason only — do not conclude
 they were deleted.]
```

两个实现细节值得记住（都有用例钉住）：

* **标记位置**：声明放在 `MetaSandboxPrefix` **之后**，绝不放在它前面。agent loop 用 `strings.TrimPrefix` 剥这个标记，而**只剥第一行** —— 声明若占了第一行，前端会静默失去"跑在沙箱里"的徽标。模型那边仍然先读到声明，因为标记在结果送进 provider 之前已经剥掉。
* **幂等**：`SetExecutor` 每轮绑定都会跑一次（`loop.go` 的 bindSession），所以包裹会叠加 —— 靠结果前缀的字符串检查去重（`TestWorkspaceNoticeIsNotDuplicatedByRebinding`）。
* **只在这些工具上**：`web_search` 一类不碰工作区的工具带上这行字，只会让声明变成背景噪声（`TestWorkspaceNoticeStaysOffNonWorkspaceTools`）。

**仍未做的**：ⓒ（事件 + UI 徽标）—— 让人也看得见；它只是补充，模型侧的 ⓑ 已经落地。

### 9.6 命名修正：`Stale` → `Unhydrated`（✅ 已落 `629ccd1` + `86cc5f3`）

**问题**：`WorkspaceUnhydrated()` 里的 **stale 在技术语境里默认读作"陈旧"** —— 即"里面有旧数据"。而这里的真实语义恰恰相反：

> **这份 `/workspace` 从未成功从持久层填充（列举失败、重试耗尽）—— 可能一个文件都没有，而不是"有旧副本"。**

中文同理：**不要译成「陈旧/过期」**，应译作 **「未水合」**（文档 §9 全程用的就是这个词，与代码命名目前**不一致**）。这一族词的区分值得写在注释里当对照：

| 英文 | 中文 | 含义 |
|---|---|---|
| stale | 陈旧 | 曾经有效、现在可能不是最新 —— **数据还在** |
| expired | 过期 | 超过 TTL，明确失效 |
| invalid | 失效 | 被显式作废 |
| dirty | 脏 | 有未同步的本地修改 |
| **unhydrated / incomplete** | **未水合 / 不完整** | **从未成功从真源填充** ← 本处语义 |

**为什么值得改而不是只加注释**：本次事故的伤害本体就是"把基础设施故障读成了关于世界的事实"。一个读作"陈旧"的字段，会让人下意识认为"里面是旧副本"，从而继续在错误的假设上推理 —— **命名是第一道防误读的门**。

**改动清单（3 个文件，纯机械）**

1. `e2b_executor.go`：字段 `workspaceUnhydrated atomic.Bool` → `workspaceUnhydrated`；方法 `WorkspaceUnhydrated()` → `WorkspaceUnhydrated()`；doc 注释补一句对照 —— *"unhydrated here means the listing failed, never 'the scope is empty': there may be no files at all, not old ones."*
2. `lifecycle.go`：类型断言 `interface{ WorkspaceUnhydrated() bool }` → `WorkspaceUnhydrated()`；注释同步。
3. 文档：§9 各处 `WorkspaceUnhydrated()` 改为 `WorkspaceUnhydrated()`（本文件现有措辞「未水合」即可，不必改中文）。

**当时的约束**：与 §9.4 余下用例、§9.5 的 ⓑ 声明**同一刀**落地 —— 改名单开一个提交会让"半成品"多一处（类型断言与实现短暂不一致）。

**收尾（2026-09-16）**：改名本身先落（`629ccd1` + `86cc5f3`，全仓旧名 0 残留），随后 ⓑ 声明与机制侧重建分别在 `1b0b77e` / `da37174` 落地；`d1b020f` 清掉两处**注释里**残留的旧词（`e2b_executor.go` 的 hydrate 路径注释、以及测试名 `TestE2BHydrateEmptyScopeIsNotMarkedUnhydrated`）。

### 9.7 这一刀之后还剩什么（都不是本轮引入的）

| 项 | 状态 | 说明 |
|---|---|---|
| ⓒ 事件 + UI 徽标 | ⬜ | 让人也看得见"这一轮的容器是空的"；补充手段，模型侧 ⓑ 已落 |
| §5 P1「hydrate 幂等校验」 | ⬜ | **attempted → verified** 仍然没做：能证明 listing 成功，不能证明文件进了容器 |
| §5 P0「`created`/`hydrated` 日志补 `scopeKey`」 | ⬜ | 一行日志的**效果**，但 `E2BExecutor` 目前没有 scope 字段，实为"给执行器加字段" |
| §7「流被切断 → 有界重试」 | ⬜ | 与本次故障正交（那是实例不可用，这是容器本来就空），优先级仍低于本刀 |
| B 案（文件工具通道绕开 hydrate） | ⬜ | **不在本仓**（Quandora session 的持久层） |
| 削掉残留竞态 | ⬜ | `recoverUnhydrated` 是"先查 `inUse`、再 Release"两步；两次之间进来的并发调用可能拿到即将被销毁的实例。窗口在一次调用内，失败形态是那次 exec 报错（不是静默数据丢失）。要彻底关掉需要引入"重建中"状态并让 `beginUse` 等待——在有实测证据说明这条竞态真的咬到人之前，不值得为它加一个状态机 |

**一句话总结这一刀**：容器可以是空的（store 会故障），但**不能空得没人知道** —— 于是 scope 知道（`unhydrated` + `hydrated=false`）、模型知道（ⓑ 声明）、系统动手（受两道闸门限制的销毁重建），而"空"与"文件不存在"在证据层面被分开了。
