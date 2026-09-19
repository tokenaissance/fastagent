# 06 · 用 Cordis 形式化原则复审本设计

> 状态：复审记录（结论：**原方案在 Cordis 框架下不合规，已修正**）· 最后核对：2026-09-17
> 依据：_A Programming Paradigm for Spatiotemporal Composability_（arXiv:2608.25512，
> Peking University × DeepSeek-AI）的落地摘要：
> [mcp-oauth-design.md §13](../mcp-oauth-design.md)
> ——本仓库已有的合规范例，本节直接把它的判据套到文件系统同步上。
> 复审对象：[02](./02-semantics-and-architecture.md) · [03](./03-state-machine-and-timing.md) ·
> [05](./05-remediation-plan.md)（尤其 02 §5 的所有权声明、03 §8 的着力点表、05 的 P0/P2）
> 定位：本文是 [00 总索引](./00-formal-systems.md) 里 **F1（前置条件 / 零迁移）** 的判据来源，
> 其权威定义在 [07 第二部分](./07-formal-rootcause-and-fix.md)；F2 可观测性与 F3 投递在 [08](./08-state-observability-principle.md)。
> 后续：[07-formal-rootcause-and-fix.md](./07-formal-rootcause-and-fix.md) 用同一套语言把根因证明与修复规则写全，
> 并修正了本文 §4.4 关于冲突出口的写法（`shadow` 文件方案已废弃，改以基线中的 `⟨CONFLICT⟩` 状态表达）。

## 1. 论文的判据（与本文相关的七条）

| # | 原则 | 精确含义 |
|---|------|---------|
| C1 | **revertible effect** | effect = Γ → Γ×(Γ→Γ)：作用后返回（新状态, **显式逆操作**）；逆在**应用现场**产出、运行时持有 |
| C2 | **左逆，只承诺 `g∘f`** | witness `g(δ)=γ`；从不要求 `f∘g`。逆是组件作者义务，运行时只持有回放 |
| C3 | **撤销一次性** | armed/dispose 只能触发一次；重复触发无保证 → 前置条件必须显式 |
| C4 | **恢复 = 观测等价 ≃** | 不承诺物理还原，只承诺观察者不可区分 |
| C5 | **前置条件错误 = 报错 + 零迁移** | `set(k,v)` 要求 `k∉dom`；违反即报错，**不得发生任何状态变更** |
| C6 | **系统边界** | 边界内可"独占修改 + 恢复"；跨边界 emission 不可逆，只能 withholding 或 compensation（同样 LIFO 合成） |
| C7 | **声明式 loader = entry 列表 + keyed diff** | 每组件一 entry；reconcile 收敛到"最终配置决定的状态"；**单 entry 重建不影响邻居** |

## 2. 映射：本系统的 Γ、effect、key、边界

| 论文概念 | 文件系统同步中的对应物 |
|---------|----------------------|
| 上下文 Γ | 一个 scope 的工作区状态：`(store 键集合, 沙箱 /workspace, 以及"上次同步时的共同观察")` |
| key 空间 | **每条路径**（不是每个文件）；`byo-account-design.html` 与 `todo.md` 是两个 key |
| entry | 一个路径的 `{digest}`；声明式文档 = 路径 → digest 的索引（记作 **B**，基线） |
| action（管理面） | `write_file` / `edit_file` / `apply_patch` / `exec` 写 / `sync` |
| inside 边界 | store 写入、沙箱写入、hydrate、reconcile（系统能把状态改回去） |
| outside 边界（emission） | 用户通过文件面板/下载看到的**已经读过的事实**、agent 已经根据旧内容做出的后续动作、跨 pod 采纳时另一个 pod 的行动 |

最后一行是本系统的特殊性，也是本次事故的本质：
**事故中真正不可逆的不是文件被覆盖，而是"用户已经读到、agent 已经据此行动"这件事。**

## 3. 逐条合规审计（原方案）

| 原则 | 原设计的实现 | 结论 |
|------|-------------|------|
| C1 | `sync` 没有逆，也不返回逆 | ❌ **不适用/未声明**：`sync` 被我当成了一个普通函数，而不是带逆的 effect |
| C2 | 无 witness（没有测试能证明"回退后可恢复到应用现场"） | ❌ T1–T6 只断言最终字节数，不构成 witness |
| C3 | `shadow` 路径每次冲突都新建，重复运行产生不同结果 | ❌ 违反"一次性"，且不可重放 |
| C4 | `shadow` 改变了工作区的**entry 集合** | ❌ 观察者（文件面板、`list_dir`、`read_file`）能看到多出来的对象 → 观测不等价 |
| C5 | `sync` 无前置条件；一切由"当前状态"推断 | ❌ **评审时的核心违规——现已修**：`shadow` 路径已从代码中消失（在 `internal/sandbox/` 下 grep 无命中），对账器在两份副本字节不同时**拒绝并上报**（`BLOCKED`，`internal/sandbox/lifecycle.go:744-764`，2026-09-18 的 T1 工作）。下面的评审正文作为"当时为真"的记录保留，不代表当前状态 |
| C6 | 没有区分"边界内可恢复"与"跨边界只能补偿" | ❌ 未声明（这也解释了"修 store 修不回来"：修 store 是 inside，旧副本仍在沙箱是另一条链路） |
| C7 | `shadow` 让一次 reconcile **改变了 entry 集合**，并且每次运行结果不同 | ❌ 违反 keyed diff 的两条：收敛性 + 单 entry 不影响邻居 |

### 3.1 最严重的一条：`shadow` 是"带副作用的 reconcile"

C7 要求 reconcile 是**幂等 + 收敛**的：对同一个声明式文档，反复运行必须得到同一个终态
（Theorem 80），且一条 entry 的重建不触碰邻居（Corollary 69）。而 P0 的 `shadow` 方案：

```
第一次 sync：冲突 → 新建 path.sandbox-shadow
第二次 sync：store 已被改动 → shadow 又更新（内容变了）
第三次 sync：……
```

entry 集合随每次运行变化，终态取决于"跑过几次"。在本仓库里已经有正确的对照：
`configs_kv` 的 per-key 行（[ports.go](../../internal/store/ports.go) 第 130 行）
就是"一个 key 一行、按 key diff 收敛"的实现。我的 `shadow` 恰好是它的反面。

### 3.2 第二严重的一条：缺前置条件 = 论文意义上的静默腐蚀

C5 把"在错误状态下执行"定义为必须报错且零迁移。原设计没有任何前置条件——
`sync` 只看"字节数是否相同"，等价于"从状态反推意图"。
本次事故正是 C5 被违反的直接后果：**状态不允许这次迁移（store 侧更新），系统照做。**

## 4. 修正后的设计

### 4.1 先修一个定性：`sync` 是 reconciler，不是 effect

论文的 effect/inverse 模型适用于**用户或 agent 发起的动作**。
`sync`（post-exec / evict 触发）不是动作，是一次**声明式收敛**：把沙箱调整到"与权威文档一致"。
对 reconciler 谈"逆"是错的提问；正确的要求是 C5 + C7：

> **每条路径一次 key 级操作，带显式前置条件；不满足前置条件时报错并零迁移。**

这也解释了为什么"给 sync 找逆操作"这条路走不通：逆属于 **per-path 的迁移**，不属于 sync 整体。

### 4.2 语法：声明式文档（基线索引 B）

```
B (scope 级，持久) : { path → digest }        digest = (size, content hash)

读法（唯一权威）：
   hydrate 完成 → B := store 当时的索引
   reconcile    → 依据下表逐路径判定；只有真发生迁移的路径才更新 B
```

存放：**复用 `configs_kv`**——`kind = "ws_baseline"`、`scope`/`scopeID` 对应
agent/project/session、每个路径一行（或整 scope 一份 JSON）。
理由与 §13.2 相同：per-key 行才能做 key 级 diff，
且不引入新表（[ports.go](../../internal/store/ports.go)、
[mcp_undo.go](../../internal/agent/mcp_undo.go) 的 `mcp_undo` 是现成范例）。

### 4.3 前置条件表（把 C5 具体化）

设 `Xs` = 沙箱当前 digest，`Ss` = store 当前 digest，`Bs` = 基线 digest：

| # | 沙箱 | store | 基线 | 判定 | 迁移 | 前置条件是否满足 |
|---|------|-------|------|------|------|-----------------|
| 1 | 不存在 | 不存在 | — | 无内容 | 无 | ✅（空操作） |
| 2 | 存在 | 不存在 | 不存在 | 沙箱产物 | 推 → store，`B := Xs` | ✅ `path∉dom(store)` |
| 3 | = Bs | = Bs | = Bs | 双方未变 | 无 | ✅ |
| 4 | ≠ Bs | = Bs | = Bs | **沙箱编辑**（exec 产出） | 推 → store，`B := Xs` | ✅ `Xs≠Bs ∧ Ss=Bs` |
| 5 | = Bs | ≠ Bs | = Bs | **宿主编辑** | 拉 → 沙箱，`B := Ss` | ✅ `Ss≠Bs ∧ Xs=Bs` |
| 6 | ≠ Bs | ≠ Bs | = Bs | **真冲突**（双方都变） | **零迁移 + 报错**（见 §4.4） | ❌ 显式报错 |
| 7 | 不存在 | ≠ Bs | = Bs | 沙箱侧删除 | **零迁移 + 报错**（删除的逆需快照，见 §5） | ❌ 显式报错 |
| 8 | — | — | 缺基线（历史 scope） | 未观察状态 | 退化为"只推 store 中不存在的路径" | ✅ 保守 |

与原方案的三处实质差异：

1. **第 5 行（宿主编辑）现在的正确动作是"拉"而不是"跳过"**。原方案只做到"不用旧副本盖新版本"，
   却留下了沙箱与 store 长期不一致（也是 [01](./01-current-implementation.md) §3.5 里 exec 看到旧内容的原因）。
   有了基线就能安全地拉，这才是"两份副本收敛"。
2. **第 6 行是报错 + 零迁移，而不是 shadow 落盘**。冲突的可见性由"报错"承担，
   而不是由"多出来的文件"承担（C4/C7）。
3. **第 7 行把删除也纳入了判定**（原方案完全没考虑沙箱侧删除）。

### 4.4 冲突（第 6 行）的正确处理

论文要求：违反前置条件 → 报错 + 零迁移。但**零迁移会让同一个冲突在下次 sync 再次触发**，
系统必须有一个可收敛的出口。按 C7 的收敛要求，出口只能是"改变 B"这个声明式事实：

```
① 报错（fail loud，进入 tool_result / session trace；对齐 mcp 的 fail-loud 惯例）
② B[path] := ⟨CONFLICT, Xs, Ss⟩     ← 冲突本身是文档里的一个状态，而不是一个新文件
③ 下一次 sync：读到 CONFLICT → 按已声明的策略收敛（默认：store 权威 → 拉 → Xs := Ss，B := Ss）
```

为什么不是 `shadow` 文件：C4 的观测等价 + C7 的"单 entry 不影响邻居"。
`shadow` 引入了新 entry，而 `⟨CONFLICT⟩` 只是同一个 entry 的一个状态值。

**同时必须声明补偿（C6）**：如果产品希望"沙箱那份编辑不丢"，那不是 reconcile 能承担的，
而是 compensation ——例如保留在 session trace 里（`session_events` 已经承载 tool_result）
或提供一次性导出。二者的区别就是论文里 inside 与 outside 的区别：**别把补偿伪装成可逆。**

### 4.5 文件动作层面（C1/C2/C3）：现在完全缺失

上面的设计解决了"同步"，但没有解决"**文件写入本身是否可逆**"。
按 C1，`write_file` / `edit_file` / `apply_patch` 应当返回（新状态, 逆操作），
且逆在**应用现场**产出。本仓库已有现成范式：

```
<mcp-undo> marker：remove 在应用现场把"完整被删 entry"随 tool_result 返回，
运行时把它持久化进 session_events，undo 再从 trace 读回自动重放（LIFO）。
```

对应到文件系统，最小可行形态是：

| action | 前置条件 | 逆（应用现场产出） | witness |
|--------|---------|------------------|---------|
| `write_file`/`apply_patch` 到既存路径 | （可选，见下） | **旧内容全文**（或 `sha256` + store 版本引用）随 tool_result 返回 | 回写旧内容后 `digest == 旧 digest` |
| `write_file` 到新路径 | `path∉dom(store)` | 删除该路径 | 删除后路径不存在 |
| `edit_file` | `old_string` 唯一存在（**已有**） | 反向替换 | 替换后 digest 回到前值 |

注意 `edit_file` 的 `old_string` 匹配已经是 C5 意义下的前置条件（不匹配就报错、零迁移），
这是本系统里唯一已经合规的一处——其余工具（`write_file`、`apply_patch` 的 Add/Delete）都没有。

**是否要给文件写入加前置条件？** 按 C5 的严格读法，`write_file` 覆盖既存文件应当先要求
"持有最新版本"（类似乐观并发：先 `read_file` 拿到 `digest`，写入时带上）。
这会改变 agent 的使用方式，属于产品决策，不应在本次修复中悄悄引入。
分阶段建议见 §6。

## 5. 边界声明（C6）

必须显式写下"哪些不可逆、只能补偿"，否则下次还会有人用"补一个副本"去修：

| 位置 | 分类 | 理由 |
|------|------|------|
| store 写入、沙箱写入、hydrate、reconcile | **inside（可逆）** | 系统独占修改，能恢复到上次观察等价状态 |
| agent 已经读到旧内容并据此行动（例如基于 41,262 字节版本写了后续文档） | **outside（emission）** | 无法收回；只能由 agent 重做（compensation） |
| 用户已下载/已分享的文件版本 | **outside** | 同上 |
| 跨 pod 采纳期间另一个 pod 的写 | **outside** | 采纳方无法回滚对端的动作；应通过基线在采纳时重新观察 |
| 沙箱侧删除（第 7 行） | **inside 但当前不可逆** | 删除的逆需要被删内容快照；没有快照就不能承诺可逆 → 必须报错（C5） |

第 3 条正是本次事故真正的伤害面：字节回退可以修（inside），
但 agent 已经根据被回退的版本继续推理了两轮（outside）。
**所以"防回退"必须发生在写入被观察之前，而不是之后补救**——这条给 P0 的紧迫性提供了论文层面的理由。

## 6. 修正后的实施顺序

| 阶段 | 内容 | 论文依据 |
|------|------|---------|
| **P0′** | ① `sync` 对共享式后端短路；② 用**前置条件表**替换字节数判据：第 2/3/4 行照旧迁移，第 5/6/7 行**零迁移 + 报错**（不写 shadow，不改 entry 集合） | C5、C7 |
| **P1′** | 基线文档 `B` 落地（`configs_kv`, kind=`ws_baseline`），hydrate 后写入；先只用于**记录与对比**（观测模式），迁移逻辑仍走 P0′ | C7 的 keyed diff 前提 |
| **P2′** | 打开第 5 行的"拉"（宿主编辑 → 同步进沙箱）与第 6 行的 CONFLICT 收敛；补 witness 测试 | C2、C4、C7 |
| **P3′** | 文件动作的可逆性：`tool_result` 携带旧内容（`<file-undo>` marker，仿 `mcp-undo`）、`undo` 从 trace LIFO 回放；删除动作补前置条件（要么快照、要么报错） | C1、C2、C3 |

P0′ 比原 P0 更小的原因：**报错 + 零迁移比"另存一份"更简单**，且不再引入新 entry。
原 P0 的 shadow 方案作废。

## 7. 验收清单（对应 §13.11 的护栏格式）

新增或评审工作区同步相关改动时逐条核对：

- [ ] 每个 key（路径）的操作前置条件是否显式写出？（`path∉dom(store)` / `Xs=Bs` / `Ss=Bs` …）
- [ ] 前置条件不满足时是否**报错且零迁移**？有没有"顺手写一份"的行为？
- [ ] reconcile 是否幂等收敛？同一份文档跑两次是否得到同一终态？
- [ ] 一次 reconcile 是否**不改变 entry 集合**（不新增/删除 neighbour key）？
- [ ] 越过系统边界的部分（用户已读、agent 已行动、跨 pod 写）是否显式声明为 emission/compensation？
- [ ] 是否有 witness（UT/e2e）能证明"迁移后可恢复到应用现场"（`g(δ)=γ`）？
- [ ] 撤销/冲突处理是否最多一次、失败是否无半提交？
- [ ] 文件写入动作是否需要旧值快照？（需要而没做 = C1 违规）

## 8. 与既有 Cordis 范例的关系

本目录的设计应对齐（而不是另立）[mcp-oauth-design.md §13](../mcp-oauth-design.md)：

| 论文原则 | MCP 管理面的实现 | 文件系统同步的对应实现（修正后） |
|---------|-----------------|--------------------------------|
| per-key 声明存储 | `agent_mcp_servers` / `mcp_oauth_tokens` … 按 PK 分行 | `configs_kv` 的 `ws_baseline`，按路径分行 |
| keyed diff 收敛 | 声明式 loader：entry 列表 + reconcile | 路径 → digest 索引 + 前置条件表 |
| 前置条件即错误语义 | `ErrMCPServerExists` / `ErrNotFound`，写库前拒绝 | 冲突/删除态 → 报错、零迁移 |
| 逆在应用现场产出 | `<mcp-undo>` marker 携带完整被删 entry | **待补**：`<file-undo>` marker 携带旧内容 |
| 边界 + 补偿 | `remove` 不吊销；`login↔logout` 只补偿 | 用户已读/已下载/跨 pod 写 = emission；沙箱删除需快照才可逆 |
| witness = UT/e2e | store/agent/gateway 全套 + undo e2e | **待补**：T1–T7 改为可证明 `g(δ)=γ` 的形式 |

## 9. 一句话结论

原设计把 `sync` 当成"带方向的拷贝"，于是只能在"覆盖"与"不覆盖"之间选，
两种选择都会静默丢一侧的数据；Cordis 给出的正确问题是**每条路径这次迁移的前置条件是否成立**——
成立就迁（并更新基线），不成立就报错且零迁移，越过边界的那部分显式声明为补偿。
这个改写同时消掉了 `shadow` 这个非收敛动作，也让"防回退"落在写入被观察之前。
