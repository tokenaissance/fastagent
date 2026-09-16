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
