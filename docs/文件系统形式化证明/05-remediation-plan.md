# 05 · 系统性解决方案

> 状态：待评审 / 待排期 · 最后核对：2026-09-17
> ⚠️ **本文件的 P0（shadow 落盘）与 P2（基线仲裁）已被 [06-cordis-review.md](./06-cordis-review.md) 修正**：
> 形式化复审判定 `shadow` 违反 keyed diff 的收敛性（改变了 entry 集合），
> 正确动作是"前置条件不成立 → 报错 + 零迁移"。实施请以 06 的 P0′–P3′ 为准，
> 本文件保留为设计演进记录（其事故分析、测试矩阵 T1–T7、边界声明仍然有效）。
> 前置阅读：[01 实现实录](./01-current-implementation.md) · [02 语义分析](./02-semantics-and-architecture.md) ·
> [03 状态与时序](./03-state-machine-and-timing.md) · [04 事故记录](./04-incident-workspace-2026-09-17.md)

## 0. 目标不变式

整套改动只服务于一条规则（即 [02](./02-semantics-and-architecture.md) §5 的所有权声明）：

> **store 是同一逻辑路径的唯一权威副本；沙箱 `/workspace` 是它的缓存视图。**
> **沙箱中 store 尚不存在的路径属于沙箱产物，允许回写；**
> **沙箱中与 store 同路径的内容，只有在能证明它由本次沙箱运行产生时才允许回写。**

配套两条推论：

- **不销毁**：任何仲裁动作都不得静默删除任何一侧已成功写入的内容；冲突必须先落到可观测、可恢复的地方。
- **声明事实**：后端形态由 driver 声明为策略可读的事实，不允许上层用副作用（字节数）反推。

## 1. 阶段划分

| 阶段 | 目标 | 风险 | 依赖 |
|------|------|------|------|
| **P0** | 止血：不再静默覆盖，且冲突可见 | 低（只改回写路径的行为与日志） | 无 |
| **P1** | 观测：量化冲突频率与形态 | 无（日志/指标） | P0 |
| **P2** | 对症：引入基线，正确区分"沙箱编辑"与"宿主编辑" | 中（新增 scope 级状态） | P1 的数据 |
| **P3** | 加固：消除分叉源（宿主写入镜像进沙箱、统一路径解析） | 中 | P2 |

阶段顺序刻意如此：**没有 P1 的数据，P2 的设计就是在猜测**；P3 的镜像失败率在真实环境很高，
只能作为降低概率的加固，不能代替仲裁。

## 2. P0 · 止血

### 2.1 改动点一：分离式后端才回写（同时删除 docker 的无效全量读）

`syncSnapshot`（[internal/sandbox/lifecycle.go](../../internal/sandbox/lifecycle.go)）入口：

```go
// 只有 /workspace 不与宿主共享的后端才需要回写。
// docker 的 /workspace 是宿主目录的 bind mount —— 快照读回的就是 store 自己，
// 逐文件比较后全部跳过，纯属白读一遍全量工作区。
if _, remote := ex.(RemoteWorkspace); !remote {
    return
}
```

收益（[02](./02-semantics-and-architecture.md) §7 Step 2 的删除项）：
docker 不再在每次驱逐时 walk 宿主目录、读取全部文件（跳过 `node_modules` 之后依然可能上千文件）。

### 2.2 改动点二：判据改为"有方向 + 不销毁"

把当前的单条 `continue` 展开成显式三分支（保持"新增路径落盘"的能力不变）：

```go
for path, data := range files {
    info, statErr := p.workspace.Stat(ctx, sc.agentID, sc.projectID, sc.sessionID, path)
    switch {
    case errors.Is(statErr, workspace.ErrNotFound):
        // 沙箱产物，store 里没有 —— 照旧落盘
        // （exec 生成的图片/报告依赖这条路径）
    case statErr != nil:
        // 存储不可读时不冒险改数据
        continue
    case info.Size == int64(len(data)):
        continue
    default:
        // 同路径、大小不同：无法证明沙箱侧更新（宿主工具也写这个 key）。
        // 既不覆盖 store，也不丢弃沙箱内容：另存 + 告警。
        shadow := path + ".sandbox-shadow"
        _ = p.workspace.Put(ctx, sc.agentID, sc.projectID, sc.sessionID, shadow,
            bytesReader(data), int64(len(data)), "")
        slog.Warn("sandbox sync: store wins, sandbox copy preserved as shadow",
            "path", path, "storeBytes", info.Size, "snapshotBytes", len(data), "cause", cause)
        continue
    }
    // …原有 Put…
}
```

为什么是"另存"而不是"直接跳过"：直接跳过会让**沙箱内的编辑永远回不到 store**
（同一场事故的镜像失败模式，见 [02](./02-semantics-and-architecture.md) §2 对第一版方案的修正记录）。
另存保证两侧数据都在，代价是临时文件名，需要一条清理策略（见 §6）。

### 2.3 P0 验收标准

- docker 上不再产生任何 `syncSnapshot` 的读盘行为（可用日志或基准测试验证）；
- e2b / boxlite 上，同路径大小不同时 **store 内容不变**，并出现一条带路径与两侧字节数的 warn；
- 新增 `sandbox-shadow` 对象可用 `List` 观察到，且不影响任何读取路径（UI 不展示、`read_file` 不读）；
- 三个回归测试通过（§5）。

## 3. P1 · 观测

P0 的 warn 已经能回答"冲突发生过几次"。补两类信息后可用于决策 P2：

| 指标 | 采集方式 | 用途 |
|------|---------|------|
| 冲突次数 / 天 / 后端 | 计数 P0 的 warn（按 `cause`、`backend` 分组） | 判断 P2 的紧迫度 |
| 冲突涉及的路径分布 | 同上，按路径聚合 | 判断是否集中在少数文件（如 `todo.md` 这类高频改写文件） |
| 沙箱存活时长 vs 冲突率 | 结合 `sandbox_leases` 的 `state`/`updated_at` | 验证 [04](./04-incident-workspace-2026-09-17.md) §6.2 "长寿实例放大问题"的假设 |

**判据**：如果冲突在高频改写文件上反复出现，说明 P2 必须做；
如果一个月内冲突次数趋近于零（例如因为 P3 的镜像已经覆盖了主要路径），P2 可以先不做。

## 4. P2 · 基线仲裁（对症）

### 4.1 模型

```
ScopeBase : 沙箱诞生（Hydrate）时 store 中该 scope 的指纹集合 {path → (size, hash?)}
X         : 沙箱当前内容
S         : store 当前内容
```

同步时对每个路径 `p`：

| 沙箱侧 | store 相对 Base | 沙箱相对 Base | 判定 | 动作 |
|--------|----------------|--------------|------|------|
| 不存在 | — | — | — | 无 |
| 存在 | 未变 | 变了 | 沙箱编辑 | **回写**（当前设计意图，且现在有据可依） |
| 存在 | 变了 | 未变 | 宿主编辑 | **跳过**（当前事故根因） |
| 存在 | 变了 | 变了 | 真冲突 | 保留 store + 另存沙箱版本 + 告警（P0 的 shadow 语义） |
| 存在 | 不在 Base 中 | 不存在 | 沙箱产物 | 回写 |

### 4.2 实现约束

- **指纹的粒度**：先用 `(size, mtime)` 或 `size + 内容哈希`。
  注意 [01](./01-current-implementation.md) §7：现有测试夹具的 `ObjectInfo.ModTime` 恒为零值，
  且 `SnapshotWorkspace` 只返回 `map[string][]byte`（不含时间）。
  若选择哈希，基线由宿主侧在 `Hydrate` 之后立即计算，**不需要给快照接口加时间字段**——
  这也让 P2 不依赖后端能力差异。
- **状态存放**：scope 级一行即可。可复用 `sandbox_leases` 行（新增一列）
  或 scope 元数据存储；两者都有既有迁移路径。
  不建议新建独立表——这是 scope 的一个属性，不是独立实体。
- **失效处理**：沙箱被 `Release`（销毁）时基线随之失效；`Sleep` 保留（与 `hydrated` 标记一致）。
- **多 pod / 跨 pod 采纳**：基线是 scope 级持久状态，因此采纳方读到的是同一个基线
  ——这一点正是 P2 相对"进程内临时状态"的价值。

### 4.3 P2 验收标准

- [03](./03-state-machine-and-timing.md) §3 的四种情形都有对应单测，且第 2、4 情形不再销毁数据；
- 跨 pod 采纳场景（一个 pod 写 store、另一个 pod 做同步）有集成测试；
- 基线缺失（历史 scope、迁移遗留）时退化为 P0 行为（不覆盖 + shadow），不得退化为当前行为。

## 5. 测试矩阵

新增于 [internal/sandbox/lifecycle_test.go](../../internal/sandbox/lifecycle_test.go)，沿用既有夹具
（`fakeWorkspace` / `snapshottingExecutor` / `snappingPool`）：

| # | 场景 | 后端 | 期望 |
|---|------|------|------|
| T1 | store 有新版本，沙箱是旧版本，触发 evict | e2b 形态 | store **不变**；出现 shadow；warn 一条 |
| T2 | store 有新版本，沙箱是旧版本，触发 post-exec | e2b 形态 | 同 T1 |
| T3 | 沙箱有 store 中不存在的新文件 | e2b 形态 | 正常回写（现有 `TestLifecycle_FlushOnEvict` 保持绿） |
| T4 | 共享式后端（不实现 `RemoteWorkspace`） | docker 形态 | `syncSnapshot` 不产生任何 `Stat`/`Put` 调用（可用计数假件断言） |
| T5 | 沙箱编辑 + store 未变（P2） | e2b 形态 | 回写（保护"exec 编辑"这条合法链路） |
| T6 | 两侧都改（P2） | e2b 形态 | store 保留 + shadow + 告警，不算成功也算不丢 |
| T7 | **镜像路径对齐**：宿主工具写一个松散会话的路径 | e2b 形态 | 沙箱中被 hydrate 的那一份**就是**被更新的那一份；store 中不产生同内容多键（见 [01](./01-current-implementation.md) §3.5） |

**夹具必须同步扩展**：`fakeWorkspace` 需支持按路径记录并返回 `ModTime`（若 T5/T6 用时间判定）
或支持记录基线指纹（若用哈希）；`snapshottingExecutor` 需能表达"沙箱内容 ≠ store 内容"。
当前夹具的形状（`Stat` 只回 `Size`）让这类场景**根本无法被表达**，是这个缺陷能存活数月的直接原因之一。

T7 与 T5 是一对：T5 保护"沙箱侧编辑能回写"，T7 保护"宿主侧编辑真的抵达沙箱"。
两者同时绿，才说明[03](./03-state-machine-and-timing.md) §7 的四个条件被同时关掉。

## 6. 运维与清理

| 事项 | 策略 |
|------|------|
| ~~`*.sandbox-shadow` 的生命周期~~（**方案已下线**：06 判定 shadow 违反 keyed diff 的收敛性，改为「报错 + 零迁移」；本行只是那版设计的运维配套，不适用于当前实现） | — |
| 告警 | warn 级日志；出现频率高于阈值时升级（P1 决定阈值） |
| 用户可见性 | 默认不展示 shadow（避免污染文件面板）；排障时用 admin 文件浏览接口查看 |
| 文档 | 本文目录 + [01](./01-current-implementation.md) 的契约说明必须与代码同步更新（GEB 同构要求：代码变更不带文档更新视为未完成） |
| **一次性清理：决策 A 之前产生的 chat 子目录重复副本** | `scripts/workspace_project_chat_duplicate_cleanup.py`（`--selftest` 可无环境自检）。它**自己不持凭证**，只吃两份清单（`aws s3api list-objects-v2 …` 的对象清单 + `select project_id, session_key from sessions` 的 chat 清单），并且**只在"同样的字节在项目根另有存活"时才列入删除**（ETag 必须是非 multipart 的 md5）；没有项目根对应用的那份、内容不同的那份、以及 `--chats` 缺失的情况，一律只报告、不删除。默认干跑，`--emit-deletes` 只是打印 `aws s3 rm` 行并把清单写进 manifest——执行是操作者的单独一步。**执行顺序**：先让 A 上线（否则线上那份旧同步会继续产生副本，清了又长），再跑清理 |

## 7. P3 · 加固项（消除分叉源）

P3 的顺序在补记后**必须调整**：先把路径映射统一，再谈镜像。否则镜像写两处只是增加副本
（[03](./03-state-machine-and-timing.md) §7.1）。

1. **统一"逻辑路径 → 物理位置"的映射（先行项）**：
   现状有四套并存——store 键由 `scopeSessionID()` + `wsPath()` 或 `r.sessionID` + 原样路径产生；
   hydrate 用 store `List` 返回的相对路径展开；镜像硬编码 `/workspace/<path>`；
   `mirrorSandboxWrite` 又按 `/workspace/` 前缀反推 store 键。
   建议抽出单一函数，例如
   `ResolvePath(logical string, scope Scope) (storeKey string, sandboxPath string)`，
   由全部写入者、hydrate 与同步共用。**这是 T7 能被写出来的前提。**
2. **让 `apply_patch` 与 `write_file` / `edit_file` 走同一条解析链**：
   现状 `apply_patch` 用 `r.sessionID` + 原样路径，另两个用 `scopeSessionID()` + `wsPath()`；
   在项目会话里解析到不同对象（[01](./01-current-implementation.md) §8）。
   第 1 项完成后本项自然成立。
3. **复核 coding 镜像是否还需要存在**：它服务的是 dev-server 热重载
   （引入于 2026-06-14 的预览功能），不是一致性；在路径映射统一之后，
   它应当退化为"用同一映射写入沙箱"的一个调用点，而不是一套独立的路径规则。
4. **镜像的失败语义**：镜像失败目前只记一条日志
   （[02](./02-semantics-and-architecture.md) §3 使用点）。应明确"镜像失败 = 分叉风险已产生"，
  并计入 P1 的指标，而不是静默降级。

> **补记（2026-09-18 第二轮）**：第 2 项已落地，第 3/4 项随之收窄，第 1 项还剩最后一块。
>
> - **第 2 项 ✅**：`apply_patch` 的 6 个触点统一到 `scopeSessionID()` + `wsPath()`
>   （[01](./01-current-implementation.md) §8.1）：3 条键单测 + 1 条"键↔沙箱路径一致"单测 + 1 条真机 E2E，
>   每条都做过反证（把键改回 `r.sessionID, path` 即变红）。
> - **第 3 项 ✅**：镜像是同一个函数里的一个调用点 `writeThroughSignal` —— 它的 store 端与沙箱端
>   **由同一个 `wsPath()` 产生**，`mirrorCodingWriteToSandbox` 已不存在。
> - **第 4 项 ✅**：镜像失败不再静默。`WriteThroughOutcome` 现在把"替换了不同版本 / 无基线可比 /
>   无第二副本"分成四态送进工具结果，"写不进沙箱"也有一条明确的 σ（[10](./10-harness-state-audit.md) §2.1）。
> - **第 1 项（部分）**：没有抽出 `ResolvePath(logical, scope)` 这个具名函数——按"单一实现不抽接口"的判据，
>   两个消费者（工具层 `wsPath()`、hydrate 的 scope 相对展开）各留一个调用点即可；但"两个端点必须同源"
>   这条要求已经由测试钉住。**仍然存在的第三个映射分歧是作用域本身**：hydrate 在项目会话里折叠掉 session，
>   sync 不折叠 —— [01](./01-current-implementation.md) §8.2 / [10](./10-harness-state-audit.md) G17，
>   属产品决策（沙箱里新生的文件落在项目根还是本 chat 子目录）。

## 8. 决策记录（ADR 摘要）

| 决定 | 为什么 |
|------|--------|
| **不引入 CRDT / 事件溯源 / 每文件锁** | 需求是"不静默覆盖"，不是分布式协同编辑；三个后端的物理差异是唯一变量，用基线即可覆盖 |
| **不新增 `MountKind` 枚举** | `RemoteWorkspace` 已表达同一事实；再加一个名字只是把隐式契约变成重复契约。[02](./02-semantics-and-architecture.md) §7 Step 2 |
| **不用文件 mtime 作为跨副本判据** | docker 的 mtime 与宿主同一份但语义不同、e2b 快照的 mtime 依赖 tar 行为、测试夹具无 mtime；用基线指纹更稳且可测 |
| **冲突默认保留 store 而非保留沙箱** | store 是用户可见副本（文件面板、下载、`read_file`），保留它能保持产品一致；沙箱版本以 shadow 保留可恢复 |
| **分阶段而非一次到位** | 仲裁策略的正确性依赖真实冲突频率与形态，P1 之前无法证明 P2 的设计假设 |
| **docker 不参与回写** | 物理上就是同一份，任何同步都是自欺；同时删掉一笔纯浪费 |
| **G4 的"归属"不做持久清单（2026-09-18 决策：选项 a）** | 这条缺口现在的代价只有**因果未知**，而**可行动的后果已经在同一句话里**（`storeOnly` 行明说"exec 看不到它们；需要就 `read_file` + `write_file` 放回去"）。做"交付清单"要在**每次 hydrate 落上千行**（事故形态：一次 1228 个文件），换来的仍然只是"报出来"；做"写者清单"要动 6 条写路径 + 新表 + 历史行无署名。等真出现"分不清就赔钱"的场景（结算/审计）再上写者清单——那时它顺带覆盖交付清单 |
| **G7b 上传半边：不穿透（2026-09-18 决策：选项 a）** | 产品语义 = **面板是"文件库"**：用户上传是给 agent 的持久文件库加一份，**不要求**"下一次 `exec` 立刻 `ls` 得到"。理由：① 代价与补救已经由既有信号说清（`StoreOnlyLine`：点名 + `read_file`+`write_file` 的搬法），agent 不会在错误的世界里推理；② 穿透会把"文件库操作"变成"沙箱写入"，还要定义"沙箱不存在时怎么办"（不建实例 vs 为一次上传付建沙箱的代价）；③ 线上是 e2b + S3，穿透需要 `internal/setup` 拿到沙箱池句柄（跨层接线），而收益只是"少一次搬运"。**注意**：LocalFS+docker 上它天然立刻可见（同一棵树），本决策不要求把它藏起来——"不承诺、也不阻止" |
| **G7b 删除半边：穿透（2026-09-18 决策：**d1**）** | 删除不是"文件库语义"能兜住的：库删了而沙箱还留着，下一次同步会把沙箱那份当"沙箱新增"写回 ⇒ **"删了又回来"**。两条候选里选 d1（穿透删除）而不是 d5（删除墓碑），理由：d1 修在**源头**（沙箱里那份直接没了，同步没有东西可写回），因此**天然不受"两个作用域"（01 §8.2 / G17）影响**；d5 要在同步侧重建一次作用域判断，而那正是这一轮反复在修的坑。代价：需要一处跨层接线（`internal/setup` 没有沙箱池 → 经 `userResolver` 上的可选接口到达 gateway），但这条线只为"一个已确认的 bug"付一次；且**不建实例**（`LiveExecutorPool` 只找活实例）——为删一个文件而建沙箱是不可接受的代价 |
| **删除必须先删对（Fix 0，同日发现并修）** | 在做 d1 的过程中发现删除**当前根本没生效**（路径被作用域拼了两次，见 [10 §3.2 的更正](./10-harness-state-audit.md) 与 G21）：列表给的是带前缀的 agent 相对路径，而 handler 又把 query 里的作用域拼了一次，键就多了一层 ⇒ 目标不存在 ⇒ 两个后端都返回成功 ⇒ UI 以为删了、**刷新那行又回来**。Fix 0 = 让删除与下载同一个约定。**它与 d1 必须成对**：只修 Fix 0 会把"静默无效"变成"删掉后复活" |
| **G17：预览容器按项目寻址（G）+ 写入/删除广播到项目内所有活容器（H）（2026-09-18 决策）** | 项目是"**一个文件树、多个容器**"：容器每 chat 一个（有意为之 —— 并发 chat 不共享 shell），而预览的 dev server 只跑在其中一个。docker 靠 bind mount 天然解决；云后端没有挂载，就必须显式做。**G**：有项目就按 `session=""` 取容器（`previewSandboxSession`）⇒ 一个项目一个预览、两个入口同一个容器（此前控制台起的预览用的容器 agent 的回合永远不用，写入永远到不了）。**H**：写入与删除广播到项目内**所有活容器**（`LiveProjectExecutors`）——保留每 chat 独立 shell，只共享文件数据。**备选 F′（一个项目一个容器）被否**：它会连 shell/进程/端口一起共享，而那是 docker 注释里明确说要避免的。**残余 A**（sync 回写仍落 chat 子目录 ⇒ store 里出现重复对象）单独待决 |
| **写入穿透的盖章（G22）：已修（2026-09-18，同一轮）** | 盖章用**沙箱作用域**查 store，而 coding-root 项目会话里工具写项目根 ⇒ miss ⇒ 项目会话里盖章失效，对账对这类路径回落到整对象读取。修法 = 让写入方把 store 作用域交下来（`WriteThroughScope` 加 `sandbox.StoreScope`；唯一调用点是 `writeThroughSignal`）。真机实测 **1 → 0** 次整对象读取；单测钉“用被声明的作用域”，反证 = 改回沙箱作用域立刻红 |
| **G17 残余 A：同步回写折叠到项目根（2026-09-18 决策，已修）** | 折叠前：sync 用**容器的作用域**回写，于是沙箱里每个项目文件都被复制进 `<项目>/<会话>/…`——文件面板与 `list_dir` 里出现重复，`exec` 新建的文件工具看不见（它落在会话子目录）。折叠后（`syncStoreScope`：有项目就 `session=""`）：回写与 hydrate、与文件工具**同一个键**，重复不再产生，`exec` 的产物立刻可被 `read_file` 看到。**已接受的后果**：项目内 store 键不再按会话隔离（这正是"一棵树"的语义）；**未做迁移**——折叠前已经产生的那些副本仍在库里（不再被刷新，也无人清理），需要时按一次性清理处理 |
| **不把"宿主写入镜像进沙箱"当作正确性机制** | 现有镜像的路径映射与 hydrate 不一致（[01](./01-current-implementation.md) §3.5），在松散会话里写到别处；在版图修正前它只能降低概率，不能承担正确性。P3 第 1 项完成后再复用同一映射 |

## 9. 待决问题（需要在实施前确认）

1. 基线指纹用 `(size, mtime)` 还是内容哈希？前者便宜但可能漏判（同长度同秒编辑），后者准确但 hydrate 时需多一次全量读。
2. shadow 的保留期与清理责任方（是否纳入现有 workspace 生命周期任务）。
3. 是否需要在 agent 侧暴露"检测到冲突"的信号（让 agent 主动重放），还是只做后台告警。
4. P3 的镜像是否只在 e2b / boxlite 上启用，还是也覆盖 boxlite 的未实现 `PortExposer` 场景（无端口暴露 ⇒ 无 dev server ⇒ 镜像收益较小）。
