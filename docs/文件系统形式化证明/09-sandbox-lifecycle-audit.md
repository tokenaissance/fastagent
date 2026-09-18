# 09 · 沙箱完整生命周期与文件系统交互的审计

> 状态：审计（2026-09-18）· 方法：用 [07](./07-formal-rootcause-and-fix.md) 的形式化模型与
> [08](./08-state-observability-principle.md) 的可观测性原则，逐格审沙箱从创建到销毁的每个状态迁移
> 及其与文件系统（store / `/workspace`）的交互。
> 证据：代码 + 生产 18 小时日志（8 次 create/hydrate、9 次 bind、1 次跨 pod adopt、1 次 rebuild、
> 18 次 pause、216 次续期、2 次 close、64 次快照失败）。

## 1. 状态与迁移（as-built）

沙箱的全程只有 7 个状态，迁移由**三类触发者**发起——这正是审计的关键：**触发者常常不是 agent**。

| # | 状态 | 由谁触发 | 对文件系统做了什么 | 对 store 做了什么 |
|---|------|---------|------------------|-----------------|
| S1 | 不存在（无实例） | — | — | — |
| S2 | 创建中（provisioned / bound） | agent 的首次工具调用（lazy），或驱逐前的重建 | — | — |
| S3 | 已 hydrate（`/workspace` = store 快照） | 创建流程内部 | store → `/workspace`（含 `/skills`） | 只读 |
| S4 | 使用中（exec / 文件工具） | agent | exec 改写 `/workspace`；宿主写入同时穿透进 `/workspace` | 宿主写入直接落 store |
| S5 | 同步（post-exec / evict） | **harness**（每条 exec 之后、每次驱逐之前） | 读 `/workspace` 快照 | 把沙箱改动写回（或拒绝） |
| S6 | 睡眠（paused / sleeping） | **harness**（10 分钟空闲 sweeper） | 文件系统与进程被冻结保留 | 同步已先跑过一次 |
| S7 | 销毁（expired / closed / release） | **provider 超时** 或 **harness 驱逐/关闭** | 整个文件系统消失 | — |

**跨副本**：`sandbox_leases` 让 S6 的实例可被**另一个 pod 采纳**（日志里 `adopted from shared lease`），
于是"谁在处理这个回合"与"谁创建了这个沙箱"可以不同。

## 2. 逐迁移的可观测性判定

| 迁移 | 变化的事实 | agent 侧信号 | 判定 |
|------|-----------|-------------|------|
| S1→S3 首次创建 + hydrate | `/workspace` 出现（内容 = store） | 无需信号（agent 只见过空世界） | ✅ |
| S3 hydrate **失败** | `/workspace` 可能是空的 | `workspaceUnhydratedSignal`（既有） | ✅ |
| S4 宿主写入 + 穿透成功 | 两副本一致 | 写结果（仅在被替换版本存在时提示） | ✅ |
| S4 穿透失败（沙箱不可达/重建） | store 已写、沙箱未更新 | 写结果 `[workspace]`（既有） | ✅ |
| S5 同步写回 | 沙箱改动进入 store | `exec` 结果 `[workspace] the sandbox changed …` | ✅ 本次补 |
| S5 同步**拒绝** | 两副本不同 | `exec` 结果 `[workspace] NOT synced …` | ✅ 本次补 |
| S5 同步**跑不起来**（快照超上限） | 沙箱改动没进 store | `[workspace] … could NOT be synced …` | ✅ 本次补（生产 64 次） |
| S5 同步发生在**空闲驱逐**（无工具结果可挂） | 同上 | 需要带走的那部分写入**耐久**载体（`SignalStore`），下一条工具结果取走 | ✅ 2026-09-18（原 ⚠️ G3） |
| S6 睡眠 | 内容不变（冻结） | 无需信号 | ✅ |
| S6→S4 唤醒 | 内容不变（同一文件系统） | 无需信号 | ✅ |
| S6 被**另一个 pod 采纳** | 内容不变 | 无需信号；未投递的信号在**耐久**载体里，采纳方照样取得到 | ✅ 2026-09-18（原 ⚠️ G3） |
| S7 由 **harness 驱逐**销毁 | 未同步内容消失 | 驱逐前已尝试同步；失败时发 S5「同步跑不起来」那条信号 | ⚠️ 见 G1 |
| S7 由 **provider 超时**销毁 | 未同步内容消失 | `[workspace] the sandbox was REPLACED …` | ✅ 2026-09-18（原 ❌ G1） |
| S3（重建）从 store 重新 hydrate | `/workspace` 被 store 覆盖 | 同上（重建时置位、下一次工具调用投递一次） | ✅ 2026-09-18（原 ❌ G2） |
| 沙箱内**删除**文件 | store 仍有、沙箱已无 | **无信号** | ❌ **G4** |

## 3. 四个缺口

### G1（已修）：超时销毁曾静默丢弃未同步内容

```
沙箱进行中（S4）→ 改动只在 /workspace
   ├─ 正常路径：空闲 sweeper 先同步、再睡眠（S6）           ✅
   └─ 异常路径：同步失败（快照超上限），或沙箱在 sweeper 之前就到期
        → provider 直接销毁实例（S7）
        → 下次调用检测到 "expired, recreating"，按 store 重新 hydrate
        → 只存在于沙箱里的那些改动，永久消失且无人被告知
```

生产证据：64 次快照失败（那一整段时间同步根本没跑成）+ 1 次 `expired, recreating`。
与事故同族：**变化发生了，而 agent 的世界里没有任何痕迹**。

### G2（已修，与 G1 同一机制）：重建本身曾经没有信号

重建成功后，下一次 `exec` **正常成功**——agent 无从知道它的 `/workspace` 刚刚被 store 覆盖过、
期间可能有未同步改动消失。既有的 `[sandbox replaced: …]` 只覆盖"实例不可用导致本次命令失败"那条路径。

### G3（已修，2026-09-18）：信号队列曾是进程内的，不构成投递保证

`pendingSignals` 存在 pod 内存里。空闲驱逐产生的信号若在投递前遇到 **pod 重启**或**另一个 pod 采纳该 scope**，
这条信号就没了——与刚因跨副本问题删掉基线是同一课：**进程内状态不是保证**。

**落点（2026-09-18）**：形式化判据见 08 §2.2（O4「不丢」）。先把队列里混着的三类事实拆开，各自用**恰好够用**的载体——
"可重算"这条判断只对其中两类成立：

| 队列里的事实 | 能重算吗 | 现在怎么走 |
|-------------|---------|-----------|
| **blocked**（拒绝覆盖的路径） | ✅ 能：拒绝**没有改变任何一侧**，下一次同步看到同样的两份副本，会再报一次 | **不排队**。下一次 post-exec 同步直接推出（顺带消灭了"驱逐时排一条 + 下次同步再报一条"的重复投递） |
| **problem**（同步跑不起来，如超快照上限） | ✅ 能：原因还在，下一次同步照样失败 | **不排队**，同上 |
| **moved**（驱逐同步刚写进 store 的路径） | ❌ 不能：这次写**抹掉了自己的证据**，之后两份副本一致，再没有任何东西可推 | **耐久载体**：`sandbox.SignalStore` 端口（`parkSignal` 写入 / `takeSignals` 取走并删除），实现在 `gateway.sandboxSignalStore`——scope 级 `configs_kv` 行（`kind=ws_signal`，与 `mcp_undo` 的光标同构） |
| **沙箱被替换**（一次性） | ❌ 不能：`TakeWorkspaceReplaced()` 一取即清 | **当次调用栈**：`getInner` 之后由**发现它的那次调用**把它渲染进自己的结果（`takeReplacedNote`）。失败的那次调用不消费它，留给下一次能带话的调用 |

于是 `pendingSignals` / `addSignal` / `drainSignals` 连同那个 map 一起**删除**：沙箱包里不再有任何投递用的进程内状态。
代价与边界（如实记）：

1. 耐久载体需要一笔 scope 级的 KV 读写，但**只在驱逐同步真的写进了东西时**发生（罕见），读取发生在下一次 exec 的同步点上（本来就要做 tar + find + N 次 stat）；
2. 没接载体的运行时（本地/CLI，没有关系库）**只记日志并 warn**，不假装会送达——这是"没有投递点就别声称有"；
3. `AppendSignal` 是读-改-写：极端并发下可能重复一句话，但不会丢（与 reconcile 的取舍同侧）。

### G4（2026-09-18 已修到"事实已声明"为止）：沙箱删除文件不可检测（删基线时引入）

"沙箱删了它"与"这份文件只在 store 里（上传）"无法区分——**这一点至今没变**，因为两者留下的是
同一条痕迹。变的是：这条痕迹以前**没人看**。

**落点（2026-09-18）**：同步的遍历定义域是**沙箱快照**，"store 有、沙箱没有"这一格本来就在域外。
现在每次同步多一次 `store.List`，把差集作为 δ 的第三种形状（`storeOnly`）报出来，措辞共用
`sandbox.StoreOnlyLine`（与 `list_dir` 的 G7a 同一句）：

```
[workspace] N path(s) are in the workspace store but NOT in this sandbox, so anything run with exec will
not find them: … — either they were added to the store after this sandbox started (an upload), or they
were deleted inside it; this runtime cannot tell which. read_file still sees them; if a script needs one,
read it and write it again.
```

**归属仍然做不到**，而且明确不做：要区分"沙箱删的"与"后来上传的"，需要一个 **store 侧持久清单**。
留着它的理由只剩"想知道是谁干的"，而 agent 需要的是"exec 看不到这些"——那已经说了。
代价：每次同步多一次 List（与既有的 tar + find + N 次 stat 同级），`skills/` 命名空间被显式跳过
（它属于只读 `/skills` 挂载，不属于 `/workspace`，否则每个 agent 级沙箱都会误报）。

## 4. 两个注意点

**A. 信号可以晚于事实。** G1/G2 的信号都只能在下一次工具调用时送达，那时重建已经发生。
这不违反原则——原则要求"可感知"，不要求"实时"；但措辞必须说明**已经发生**，而不是"将要发生"。

**B. `/workspace` 的体积上限同时是功能上限。** 32 MiB 快照上限让 S5 整段失效（同步全失败）。
它不是可观测性问题，但**制造**了 G1 的前置条件。系统提示已要求大文件放 `/tmp`，
G1 的信号也必须指向这一条。

## 5. 形式化对照（07 §2.4 的四个触发条件）

| 条件 | 在生命周期中的位置 | 现状 |
|------|------------------|------|
| 分离式后端（两份副本） | S3 起 | 固有 |
| 路径在沙箱中存在 | S3 hydrate / S4 exec 创建 | 固有 |
| 宿主改过 store 且两副本不同 | S4 宿主写入 vs 沙箱副本 | 穿透使其在**成功时**不成立 |
| 一次 Sync 发生 | S5（post-exec / evict） | 每次 exec 与每次驱逐都会发生 |

**审计结论**：条件三被穿透大幅削弱但**未消灭**——它在"穿透失败 + 沙箱副本陈旧"时仍成立，
而新判定表（字节比较 + 拒绝）保证那种情况**只会被拒绝，不会被覆盖**。
真正剩下的风险不在"错误覆盖"，而在 **G1/G2 的静默丢失**：内容在沙箱里、沙箱没了、store 里也没有。

## 6. 建议的处置顺序

| 优先级 | 动作 | 依据 |
|--------|------|------|
| ~~P0~~ | ~~重建/expired 时给 agent 一条信号~~ **已落地**：执行器置 `workspaceReplaced`，由**发现它的那次调用**渲染进自己的结果（`takeReplacedNote`，2026-09-18 从队列改为调用栈）（`TestRebuiltSandboxIsAnnounced`） | G1/G2，唯一一类"静默丢失" |
| ~~P1~~ | ~~把 `pendingSignals` 改成可重算~~ **已落地（2026-09-18）**，但原计划只对了一半：可重算的（blocked/problem）**不排队**，不可重算的（moved）走**耐久端口**（`SignalStore`），重建走**当次调用栈**。测试 `TestEvictSignalOutlivesThePoolThatProducedIt` 用另一个 pool 实例投递，正是原计划想证明的那一格 | G3，跨副本 |
| ~~P2~~ | ~~恢复删除检测，但用 store 侧持久清单而非进程内状态~~ **2026-09-18 降级**：删除留下的痕迹已经会被报出（delta.storeOnly，见 §3 G4），因此**"检测"不再需要清单**；清单只在"想给这次分歧归属"时才有价值，而那是产品选择，不是可观测性缺口 | G4 |
| P3 | 快照上限与 `/workspace` 使用纪律：信号里给出可行动指引（把大文件移出） | 注意点 B |

