# 12 · 租约的形式化设计：从既有实现归纳出的契约

> 状态：归纳（as-built 取证 + 设计规则）· 最后核对：2026-09-19
> 触发：2026-09-18 跨副本双轮次事故（[../session-turn-integrity.md](../session-turn-integrity.md)）——
> 设计 `session_turns` 时必须回答"本仓库里的**租约**到底是什么"。
> 答案不是新发明的：它从**已在树上的四个实现**（`channel_leases`、`rediscoord.Leaser`、
> `sandbox_leases`、`RedisRefreshLocker`）归纳而来，并用一次探针实验证伪了其中一条被写进注释的断言。
> 归属：**F1（前置条件 / 零迁移）的机制层** —— 租约是"制造前置条件"的手段，不是第四套形式系统
> （[00 §1](./00-formal-systems.md)）。
> 英文版：[`../fs-formal-proof/12-lease-formal-design.md`](../fs-formal-proof/12-lease-formal-design.md)

## 1. 为什么要把它形式化

本仓库现有**四个**互不认识的租约机制，各自发明了一遍同样的三件事：

| 机制 | 键 | 值 | 代码 |
|---|---|---|---|
| 渠道单例（关系库，默认） | `(channel, account_id)` | `holder_id` + `expires_at` | `internal/gateway/channels.go:16-30` → `internal/store/database.go:4975-5055` |
| 渠道单例（Redis，可选） | 同上 | holder 值 + TTL，Lua 校验 | `internal/rediscoord/lease.go:28-82` |
| 沙箱实例租约 | `scope_key` | `owner` + 实例身份 + `expires_at` + `epoch` | `internal/store/sandbox_leases.go` |
| MCP OAuth 刷新互斥（Redis，可选） | oauth store key | 常量 `"1"` + TTL | `internal/mcp/oauth/adapter/redis_locker.go` |

它们**都对**（在各自的场景里够用），但"对"的理由从来没有被写下来；于是要新增第五个
（`session_turns`）时，就没有判据说清"哪几条是必须的、哪几条是可以省的"。这份文档给出那套判据，
并把它与 F1/F2/F3 的既有形式化对齐。

## 2. 定义：租约 = 一条共享存储里的**前置条件见证**

固定一个**键** `κ`（被守护资源的身份）、一个**持有者** `h`、一个**围栏令牌** `e`（整数）、
一个**到期时刻** `τ`。共享存储里的一行就是一份租约：

```
L(κ) = (h, e, τ)          live(L(κ), now)  ≜  L(κ) ≠ ⊥ ∧ τ > now
```

三个操作（`Acquire` / `Renew` / `Release`）与一个读（`Get`）。被守护的动作记作 `A(κ)`——
它可以是"持续独占"（一个长轮询循环、一个回合）也可以是"离散效果"（一次销毁请求、一次历史追加）。

**它不是锁。** 锁的语义由持有者自己维护（"我拿着它"）；租约的语义由**存储**维护
（"这一行现在说谁拿着、到什么时候"）。判据永远在存储那一侧求值，这是下面 L4/L5 的根据，
也是三个既有实现都把真相放进行里的原因。

## 3. 义务 L1–L7

| # | 义务 | 陈述 | 违反的后果 |
|---|---|---|---|
| **L1** | 互斥 | 对每个 `κ`，任意时刻至多一行 `live`；`Acquire` 是 CAS（不是"读—判断—写"） | 两个持有者同时执行 `A` |
| **L2** | 署名 | 行里记下 `h`，且 `h` 可归因（进日志、进信号） | 事后无法回答"是谁在写"；无法区分"同一个持有者重入"与"换了人" |
| **L3** | 时效 | `τ` 是**唯一**的失效通道（自然过期或自愿释放）。推论：`TTL > sup(duration(A))` 是 **A 的安全前提，不是性能旋钮** | 活着的 `A` 被判死：第二个持有者进场，两个同时写 |
| **L4** | 围栏 | (a) `A` 的效果**携带**令牌，且由**资源侧在同一原子步骤内**校验，(b) 校验的是**对** `(h, e)` 与当前 live 行相等，(c) 该**对**必须逐次获取唯一——要么 `h` 内嵌一次性 nonce，要么 `e` 在**行的整个生命期内**严格单调 | 迟到的效果落在新世代的资源上（经典的"被暂停的写者醒来后覆盖"） |
| **L5** | 受保护的释放 | `Release`/夺取都必须带同一个 `(h, e)` 谓词；无谓词的释放是漏洞 | 过期持有者的迟到清理，删掉**当前**持有者的租约 |
| **L6** | 判决可观测 | `Acquire` 的结果、`Renew` 的失败、每一次失去，都是 σ（F2）且必须投递（F3） | 消费者把"沉默"当成"我独占"（[08 §2.2](./08-state-observability-principle.md) 的 P1′） |
| **L7** | 前置条件可求值 | 一次写的前置条件必须在效果的**线性化点**可求值，且**见证与效果的执行者同址**（前提：见证**可求值**——承载它的存储可达，见 §3.2 的条件 (c)）。满足 ⇒ 条件写（B 族）；不满足 ⇒ 只能客户端比较（A 族），并且必须把它**声明为检测而非阻止** | 判据在过期观测上求值：漏检时机制在 `pre` 为假处仍然迁移（**R2 静默失效**），且**不发出任何 σ**（O1 在沉默方向失效——最难察觉的一类） |

### 3.1 L7 的推论：八个文件变更 seam 该怎么选族（W1–W8；标题原写「七个」，2026-09-20 按表核正）

> L4(a) 说的是"**租约的**令牌必须由资源侧在同一原子步骤校验"；L7 是它的推广——**任何**写的前置条件都适用同一条：
> **见证必须在执行效果的那一方手里**。见证在手里 ⇒ B 族免费；见证只 in-band 存在副本里 ⇒ 此时 A 族免费。
>
> **为什么不能靠"再造一份一致的事实"补上**（别读成"造不出来"——**数据库里的租约就是一份跨副本一致的独立事实**，
> 一 scope 一行、CAS，很便宜）。能不能买到**阻止**，取决于两件事：
>
> | 条件 | 满足（买到阻止） | 不满足（只买到检测） |
> |---|---|---|
> | **(a) 资源侧能在同一次原子步骤里校验它**（= L4a） | 数据库行 CAS；对象存储 ETag 条件 PUT | 容器里的 POSIX 文件：没有条件写，检查与写入之间没有原子性 |
> | **(b) 该资源上有强制点，所有写者都必须经过它** | 存储 API 是唯一入口 | 容器不是：agent 的 `exec`、脚本、快照解包都能绕过中介直接改文件 |
>
> 两条都满足 ⇒ 一次往返买到阻止；缺任一条 ⇒ 无论造多少事实，买到的都是**检测**（按 L7 必须**声明为检测**）。
> W2/W3 落在 A 族是因为它们不满足这两条，不是因为那里"想不出办法"。
> 附带代价：即便造了那份事实，它也得**跟着容器生命周期一起生灭**（每次 hydrate 重建、每次驱逐重写），
> 而它描述的正是那个能被绕过的东西。

| seam | 见证此刻在谁手里 | 全 A 的后果 | 全 B 的后果 | 正解 |
|---|---|---|---|---|
| **W1** tool→store（write_file / edit_file / apply_patch） | **在手里**（工具刚读过对象） | 有窗口；且 `write_file` 是盲写 ⇒ 前置条件为**空集**（无围栏） | **免费且精确**（+1 HEAD，0 字节） | **B**（B1–B11） |
| **W2** store→沙箱镜像（穿透） | **in-band**：沙箱副本的 mtime 就是交付戳 | 戳 + 字节兜底，够用（同秒同大小会漏） | 需把 ETag 送进沙箱 ⇒ 持久清单或 sidecar | **A** |
| **W3** 沙箱→store 回写（T1 对账） | **只在 in-band 戳里** | 同上（窄窗口） | **必须重建"沙箱手里那版"的记忆** ⇒ 退回 09-18 之前；且拒绝面扩大会活锁（07:520） | **A**（+可选一行判据收紧） |
| **W4** 面板上传/删除 | **在手里**（刚列出过） | 有窗口 | **免费** | **B**（B11） |
| **W5** 附件写入 | 无（新文件） | 只能"必须不存在" | 同一语义，廉价 | **B**（`VersionAbsent`） |
| **W6** 技能发布 | 无（覆盖是**意图**） | 无谓据 | 无谓据 | **都不做**，只声明姿态 |
| **W7** hydrate | 它是**见证的生产者**（写下戳） | 戳即见证 ✅ | 需把见证外送进沙箱 | **A** 的戳就是正解 |
| **W8** Move | 目标必须为空 | 服务端已按 B 的形状做（S3 上非原子） | 同 | **B**，并把"非原子"写进强度表 |

**结论**：**"全 A"与"全 B"都严格劣于这个划分。** 全 A 把 W1/W4 从"闭窗"降级为"开窗"却买不到任何东西（B 在那两格是免费的）；全 B 为了让 W2/W3 有见证，必须引入一份自身需要跨副本一致的元数据，且会重现 07:520 的活锁形态（R3 不收敛）。

**L4(c) 是本轮唯一新增的、被实测的需求**：其余五条都能在既有实现里找到实例，
只有它是在设计 `session_turns` 时被沙箱租约的实测行为逼出来的（§5）。

### 3.2 退化路径（缝登记 #2 的判决）：租约存储不可用时，今天选的是**放行**

> 来源：《认知哲学的数学原理》19.5.8.7 缝登记表第 2 行（"数据库不可用时的降级路径**未逐格判定**"）。
> 本节就是那一格的判定。**取证日期：2026-09-21。**

**as-built 取证（现状）。** 沙箱池在**每一条**租约**准入**读路径上都是 fail-open：

| 位置 | 失败 | 代码做了什么 |
|---|---|---|
| `internal/sandbox/e2b_executor.go:2182` | `AcquireSandboxLease` 报错 | `slog.Warn("e2b lease acquire failed (keeping local sandbox)")` 并**继续用本地沙箱** |
| `:2434-2435` | `GetSandboxLease` 报错 | 注释原话：`Registry unavailable: fail open on the local executor.` |
| `:2445` | 过期后 reclaim 报错 | 保留本地 executor |
| `:2489` | 重建的沙箱没能上账 | 保留本地 executor |

而且这是**被测试钉住的契约**：`internal/sandbox/lease_pool_test.go:415`
`TestE2BPoolFreshGetLeaseErrorsFailOpen`，注释写着
`Fail-open contract (design doc: "Registry errors fail open: the sandbox is left alive")`。

**这一格属于哪一类。** 见证（租约行）**在 DB 上**，而效果（发放 / 复用沙箱）在 pod 上。
DB 可达时，这一格是 (a)(b) 都满足的 B 族——这正是 §3.1 里"数据库租约很便宜"那句的兑现。
DB **不可达**时，坏掉的不是"见证不在手里"，而是**见证根本求值不出来**：
两个世界（"没人持有" vs "别人持有但我们查不到"）产生**同一个观测**（一个错误）。
所以这一格不是③（做不到），而是**②的兄弟：判不出**。

**于是引出 L7 的第三条隐含条件。** L7 的前两条（原子校验 + 强制点）都在讲**见证在哪**；
本节补上第三条，它讲**见证能不能被求值**：

> **(c) 见证必须可求值**（承载它的存储可达）。
> (a)(b) 满足而 (c) 不满足时，判据**不可求值**——此时"照常执行"与"拒绝"都不是由见证决定的，
> 而是一个**可用性选择**，因此必须**声明**。

**按 19.4.6.1 的三个动作走一遍。**
① 见证在谁手里？——在 DB 里，此刻**取不到**。② 能换地方吗？——本系统的强制点只有 DB
（沙箱实例在对象存储上没有条件写语义），**换不了**。③ 都不能 ⇒ **收手 + 标注**。
**当前实现选的是第四种：放行，且不说**——那正是 19.4.6.1 反面清单里的 ✗「沉默地降级」。

**两条可落地的路（任选其一，都必须具名）。**

- **路 A（收手）**：租约查不到 ⇒ **不发放、不复用**可写沙箱，本次回合拒绝。
  代价：DB 抖动 = 回合失败（可用性）；收益：窗口内不可能出现两个写者。
- **路 B（放行 + 申报）**：保持 fail-open，但**必须产生 σ**——一句真话，例如
  "本次没有取得跨副本准入：租约存储不可达，可能有两个写者"，并**落在 `D₁` 或 `D₂`**
  （有执行中的调用落 `D₁` 工具结果，否则落 `D₂` 回合入口；取不走就按 08 §6.1 显式设计未来的投递点）。
  同时在强度表里把这一格的准入**声明为放行**（既不是阻止，也不是检测）。

**为什么路 B 是本节的推荐。** 它**零成本**（只加一句真话），却把 A1 的纪律从"拒绝方向"补齐到**放行方向**——
**拒绝 ≠ 沉默，放行也不 ≠ 沉默**。而且它的产物正好落进已有机制：不需要新表、不需要新往返。

**边界（这四条必须一起写）。**

1. **不是所有 fail-open 都是同一件事。** 同一份文件里的 `:2609`（`ReleaseSandboxLease` 报错 ⇒
   **保留沙箱存活**）方向相反：那是**避免不可逆破坏**的保守选择，**没有违反 L7**，也不该被一起改掉。
   于是这里要给"fail-open"**分义**：**准入方向的 fail-open（放行）** 与**生命周期方向的 fail-open（不销毁）**——
   同一个词在同一份文件里承担两个相反的意思，这是"单一来源 / 表达唯一"族的新成员（与 G23 / G16 同形）。
2. **DB 完全不可用时**，回合本身起不来，σ 的落点可能一起没有 ⇒ 那一格退化成"② 没有落点"，
   按 R4 / P1′ 必须**设计未来的投递点**（下次 `D₂` 补报），而不是假装已经说过。
3. **本节只判定，不施工。** 代码未改；`10 §4` 的 **G31** 行是登记，不是修复。
4. **`releaseExecutor` 里还有第三条分支，它落不进上面任何一种（2026-09-21 新增，见 G32）。**
   `:2615` 的 `!deleted` 分支**不是**"存储报错"（那是 `:2609`），而是**存储成功地回答"没有行匹配你给的键"**。
   当池子的**进程内 epoch 镜像**（`internal/sandbox/e2b_executor.go:1868` `leaseEpochs`）里没有这个 scope 的 epoch 时，
   释放会呈现 `epoch=0`，`DELETE ... AND epoch = ?` 一行也匹配不到，于是代码把这个 `false` 读成
   "另一个持有者挡住了我"——**而在另一个世界（真相）里，那一行还写着释放者自己的名字**。
   受控实验见 G32（`internal/sandbox/lease_epoch_gap_test.go`：返回 `nil`、销毁 0 次、日志 0 行，
   与"被围栏挡下"那一格**观测上无法区分**）。⇒ 这条分支**不是**保守选择，而是**沉默地丢弃一次释放**；
  按本条的纪律（放行必须申报），它至少要产生 σ。**本节不裁定走哪条路**——登记在 `10 §4` 的 **G32**。

## 4. 既有实现 vs 六条义务（as-built）

| | `channel_leases` | `redis` 渠道租约 | `sandbox_leases` | `session_turns`（设计） |
|---|---|---|---|---|
| L1 互斥 | ✅ `ON CONFLICT … WHERE expires_at < now OR holder_id = me`（`database.go:4986-4996`） | ✅ `SETNX` + 续租 Lua | ✅ 单条 `UPDATE` 抢占 + `INSERT … DO NOTHING` + 回读（`sandbox_leases.go:52-101`） | ✅ 同 `channel_leases` 形态 + `epoch = epoch + 1` |
| L2 署名 | ✅ `holder_id` | ✅ 值 = holderID | ⚠️ `owner = host:pid`（可重复，见 §5） | ✅ `holder = <pod>/<uuid>`（逐次唯一） |
| L3 时效 | ✅ TTL 30s / 续租 10s（`internal/channels/lease.go:32-34`） | ✅ 同上 | ✅ TTL 15m > 单次工具调用（`internal/sandbox/lease.go:134`） | ✅ TTL = 回合预算 45m + 60s 宽限，定时器按 TTL/3 续租 |
| L4 围栏 | — （被守护的动作是**连续**的：续租失败即取消 ctx，没有"迟到的离散效果"） | — 同上 | ✅ **已修（2026-09-19，G25）**：抢占分支改为 `epoch = epoch + 1`，令牌逐次唯一（`sandbox_leases.go:69-80`；UT `TestSandboxLeaseEpochNeverResetsAcrossTakeover`） | ✅ 严格单调 + `(holder, epoch)` 唯一 |
| L5 受保护的释放 | ✅ `WHERE … AND holder_id = ?`（`:5044-5051`） | ✅ Lua 内比对值 | ✅ `WHERE scope_key = ? AND owner = ? AND epoch = ?`（`:224-235`），但受 L4 复位影响 | ✅ 同形 |
| L6 判决可观测 | ⚠️ 输家只是重试（渠道是静默基础设施，可接受） | ⚠️ 同上 | ⚠️ 只有部分事实上了行（`unhydrated`、`state`） | ✅ `turnActive{holder, epoch, expiresAt}` + `queued{holder, ETA}`（A4.1） |

第四行之外还有一处**同族不一致**，与本目录的"单一来源"家族同形：`RedisRefreshLocker` 的
注释说 "Release drops the lock (guarded by the token value)"，但代码是
`l.Client.SetNX(ctx, key, "1", ttl)` + `l.Client.Del(ctx, key)` —— 值恒为 `"1"`，
释放是**无条件 DEL**（`redis_locker.go:29-36`）。它今天不生效（Redis 在两个环境都没开，
[session-turn-integrity A1.1](../session-turn-integrity.md)），但它说明：**L5 不是自然长出来的，
是必须被写下来的**。

## 5. 反例（G25）：沙箱租约的围栏令牌每代归 1 —— **已修（2026-09-19）**

`AcquireSandboxLease` 的两条写语句把 `epoch` 写死为 `1`：

```sql
-- 抢占过期行（sandbox_leases.go:69-76，epoch = 1 在 :72）
UPDATE sandbox_leases SET owner = ?, sandbox_id = ?, …, epoch = 1, …
WHERE scope_key = ? AND expires_at <= ?
-- 首次插入（:80-87）
INSERT INTO sandbox_leases (…, epoch, …) VALUES (…, 1, …)
-- 续租 / 替换才自增（:120-124 / :153-160）
UPDATE sandbox_leases SET …, epoch = epoch + 1, … WHERE …
```

于是 `epoch` 只是"**本次持有周期内**收到了几次续租"，不是"这一行历史第几个世代"。
两条已有文档在这里**互相矛盾**：`docs/sandbox-pool-leases.md:87-89` 诚实地说
"`epoch` is monotonic within a lease cycle only … a delayed destroy from an earlier cycle by
the same owner is not covered"，而同一文件的 `:237-240` 又说 "The version column makes any stale
destroy request fail closed"。**探针实验判定后者为假**（临时测试，跑完即删）：

```
gen1 acquire(insert): sandbox=sb-1 epoch=1
gen1 renew: epoch=2 then epoch=3
gen2 acquire after expiry, SAME owner: epoch=1
gen3 acquire after expiry, DIFFERENT owner: epoch=1
stale gen1 release(pod-a, epoch=1) against gen3 row: released=false      ← owner 变了，挡住了

# 危险形状（第二个探针）：同一个 owner 字符串换代
gen2 acquire: sandbox=sb-2 epoch=1
stale gen1 release(pod-a, epoch=1) against gen2 row: released=true      ← 删掉了新世代的活行
after the stale release, the row is: <nil> (err=<nil>)
```

**判定**：这是 **L4(c) 的违反**（`h` 可重复 **且** `e` 复位 ⇒ `(h, e)` 逐次获取不唯一）。
发生条件是"同一个 `host:pid` 在上一代过期后重新取得同一 scope，且迟到者携带的令牌号恰好等于
新世代当前值"。今天的调用方传的是"自己最后收到的 epoch"（`e2b_executor.go:2601-2608`），
所以现实窗口很窄——**但注释声称的保证（"任何迟到的销毁都会失败"）不成立**，
而修复是一条款：

```sql
-- 抢占分支改为自增；插入分支保持 1（首行没有前驱）
UPDATE sandbox_leases SET …, epoch = epoch + 1, … WHERE scope_key = ? AND expires_at <= ?
```

**已落地（2026-09-19）**：抢占分支现在是 `epoch = epoch + 1`（`internal/store/sandbox_leases.go:69-80`），
新增 `TestSandboxLeaseEpochNeverResetsAcrossTakeover`（`internal/store/sandbox_leases_test.go`）钉住"两代接管后严格递增 + 老令牌释放被拒 + 活行仍在"。
它不改变任何现有测试的断言（原 `sandbox_leases_test.go:185/202` 断言的是**插入**分支的 1）。
**反证已实跑**：把 `epoch = 1` 改回去 ⇒ `gen1=1 gen2=1` 失败。

> 这条反例的价值不在沙箱本身（后果由 `owner` 兜住大半），而在于它证明了 L4(c) **不是教条**：
> 一个"每个世代从 1 开始"的令牌，配上可重复的持有者身份，就不再是围栏。

## 6. 对 `session_turns` 的实例化（设计规则 → 具体取值）

| 义务 | `session_turns` 的取值 |
|---|---|
| 键 `κ` | `(user_id, agent_id, session_key)` —— **照抄被守护资源的键**（`sessions` 的主键，`internal/store/database.go:1721`） |
| 持有者 `h` | `<pod>/<uuid>`，**每次 `Acquire` 新生成** ⇒ L4(c) 由构造满足（即便行被删除重建，`(h,e)` 也不会重复） |
| 令牌 `e` | 行内严格单调（`epoch = epoch + 1`，含抢占分支），永不复位 |
| 到期 `τ` | 45m（回合预算）+ 60s，回合自身的 goroutine 按 TTL/3 续租（不用沙箱的"活动驱动续租"：一个回合可以在一笔 `delegate_task` 里静默 605 秒，见事故） |
| 围栏点（L4a） | **写语句本身**：`AppendSessionMessageFenced` / `SaveSessionFenced` 携带 `(h, e)`，语句内 `EXISTS (SELECT 1 FROM session_turns WHERE … holder_id = ? AND epoch = ? AND expires_at > now)`；0 行 ⇒ `ErrSessionFenceLost`。**不是"先查再写"**——那有 TOCTOU 窗口，等价于把围栏放在调用方。类型归属：端口层用 `session.TurnFence`，适配器翻译成 `store.SessionFence`（内层接口不命名外层类型） | 

> **L4a 的前提，2026-09-19 明写（为什么围栏不能搭 ctx 的车）。**
> "把 `(h, e)` 带进语句"只有在**缺席可区分**时才可强制：ctx 值做不到这件事——"没有围栏"与"调用方忘了带围栏"
> 在类型层是同一个东西——所以围栏必须走写的签名，那里的 `nil` 是一个**显式表态**（"本次写不受租约管辖"）。
> 这一族随后统一骑在同一个值上（`session.WriteScope`），而**拒绝**同样按这个规则归属：`session.ErrSessionFenceLost`
> 属内层包，由适配器从 store 的哨兵翻译而来。可复用的判决：**同一族事实里，最强的义务决定传输**
> （拒绝语义 > 记录语义），年龄只在义务等强时作 tie-breaker。见证与反证记录在
> [../session-turn-integrity.md](../session-turn-integrity.md) §A1.4a。
| 释放（L5） | `DELETE … WHERE holder = ? AND epoch = ?` |
| 判决（L6） | `queued{holder, ETA}`（输家知道自己被谁挡住、要等多久）+ `turnActive` 上订阅/历史载荷（A4.1） |

**边界（写下来，不假装不存在）**：租约把"同一个 session_key 上的两个轮次"串行化，
但它**不**串行化 `session_key` 的生成。IM 会话的第一条消息在两个副本上并发时，
`resolveOrMintKey`（`internal/session/manager.go:294-304`）会各铸一个随机键 ⇒ 两个会话、两份租约。
Web（事故路径）的键 == `chatID`，是确定性的，所以不在本轮范围内；这是"两写者"家族的另一个入口，
与 G24 同族，等它出现真实形态再收。

## 7. 四层归属（Clean Architecture）

| 层 | 这一族的落点 | 依赖方向 |
|---|---|---|
| **Entities** | 不变式本身（"每个会话至多一个轮次在写"）+ 键的身份 `SessionKey`（= 会话的身份，不是传输的身份） | 不依赖任何东西；不知道 SQL、不知道 Redis |
| **Use Cases** | 准入策略：谁排队、谁直接拒绝（`TurnStartOrQueue` / `TurnStartIfIdle`，`internal/agent/admission.go`）、回合生命周期 | 定义端口 `SessionLease`，**由内层拥有** |
| **Interface Adapters** | 端口的实现装配：`storeLeaser` 的同形兄弟（`internal/gateway/channels.go:16-30`）；`session/store_adapter.go` 是**唯一**同时认识 `session.TurnFence` 与 `store.SessionFence` 的地方（翻译边界） | 实现内层定义的接口（DIP）；不知道路由/HTTP |
| **Frameworks & Drivers** | PostgreSQL / SQLite 的 DDL 与 CAS 语句（`internal/store`）、Redis（可选）、进程内 `NopSessionLease`（单实例） | 最外层；可替换 |

两条推论，都来自既有代码而不是偏好：

1. **端口的定义权在内层**：`channels.Leaser` 定义在消费者包 `internal/channels`，
   SQL 在 `internal/store`，适配器在组合根 `internal/gateway`。`session_turns` 照抄这一条：
   端口在 `internal/agent`（消费方），实现在 `internal/store`，装配在 `internal/gateway`
   （`agent.WithSessionLease`，与 `WithSessionStore` 同族，`internal/agent/manager.go:86-150`）。
2. **围栏必须落在最外层**：L4(a) 要求"资源侧在同一个原子步骤内校验"。把校验写在 Entities 或
   Use Case 里就退化成"先查再写"。这不是分层洁癖——它是围栏语义与存储语义**必须同源**。

## 8. 与 F1/F2/F3 的复合

```
租约解决的是 F1 的问题：谁有权做这次迁移（"开始一个轮次"就是一次迁移）
   L1/L3/L5  ⇒ 前置条件成立
   L4        ⇒ 前置条件在效果落地的那一刻仍然成立（围栏 = 前置条件的时延补偿）
租约的判决是 F2 的 δ：acquire 成功/失败、续租丢失、被接管
   该 δ 必须有 σ（L6）：给用户看的是 queued{holder, ETA}，给 agent 看的是"你的回合被接管了"
σ 的送达是 F3：排队事件走既有出口，回合中途被抢走走工具结果/回合提示词
```

一句话：**租约是 F1 的机制，它的判决是 F2 的素材，它的送达是 F3 的义务**——
所以三套形式系统里没有"第四套租约系统"，只有 F1 的一个被反复实现的机制。

## 9. 证据索引

| 断言 | 证据 |
|---|---|
| 渠道租约无令牌、释放受 holder 保护 | `internal/store/database.go:4975-5055`；探针输出 `channel_leases columns: [channel account_id holder_id expires_at]` |
| Redis 渠道租约 = `SETNX` + Lua | `internal/rediscoord/lease.go:28-97` |
| `RedisRefreshLocker` 的释放是无条件 DEL | `internal/mcp/oauth/adapter/redis_locker.go:38-45` |
| 沙箱令牌每代归 1、迟到释放能删活行 | 探针 `TestProbeSandboxLeaseEpochOnTakeover` / `TestProbeSandboxLeaseEpochResetFenceCollision`（2026-09-19 实跑，输出见 §5；探针文件跑完已删）。**修法与 witness**：`internal/store/sandbox_leases.go` 抢占分支 `epoch = epoch + 1` + `TestSandboxLeaseEpochNeverResetsAcrossTakeover`（反证已实跑） |
| 沙箱文档自相矛盾 | `docs/sandbox-pool-leases.md:87-89` vs `:237-240` |
| 回合租约的取值 | [../session-turn-integrity.md](../session-turn-integrity.md) A1.1–A1.5 |
| **租约存储不可用时是放行还是收手（§3.2 / G31）** | `internal/sandbox/e2b_executor.go:2182`（acquire 失败保留本地沙箱）、`:2434-2435`（Get 失败，注释原话 `fail open`）、`:2445`（reclaim 失败）、`:2489`（重建未上账）、`:2609`（release 失败保留存活——**方向相反，是对的**）；测试 `internal/sandbox/lease_pool_test.go:415` `TestE2BPoolFreshGetLeaseErrorsFailOpen`（**2026-09-21 取证**） |
| **epoch 未记录时释放被静默丢弃（§3.2 边界 4 / G32；缝登记 #5 的 P-WAD 第 1 行实测）** | 载体 `internal/sandbox/e2b_executor.go:1868` `leaseEpochs`（写点 `:2040`、读点 `:2111`、用点 `:2607`）、**丢弃点 `:2615`**（`!deleted` ⇒ `return nil`，无错、无日志）、发放期的 fail-open 注释 `:2215-2216`、SQL 判据 `internal/store/sandbox_leases.go:237`；受控实验 `internal/sandbox/lease_epoch_gap_test.go` `TestPWAD1_ReleaseWithNoRecordedEpochIsSilent`（**2026-09-21 取证**：`err=nil` / 销毁 0 / 日志 0，且与"被围栏挡下"那一格**逐项相同**；把 σ 探针放回去即变红——实测过） |

**witness 现状**：
- **已落地**：L1 并发唯一赢家（`TestSessionLeaseConcurrentAcquireHasOneWinner`）、L4(c) 令牌不复位（`TestSandboxLeaseEpochNeverResetsAcrossTakeover`，沙箱侧；回合侧随 A1 落地）、L5 迟到释放被拒（`TestSessionLeaseAcquireRenewReleaseAndTakeover`）——都在 `internal/store`。
- **待补**：L6 的排队载荷带 holder/ETA（随 A4.1）、L1/L4/L5 的真机 e2e（随 A1 的跨副本场景）——写在 [11 §12](./11-change-register.md)。
