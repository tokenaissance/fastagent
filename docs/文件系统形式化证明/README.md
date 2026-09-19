# fastagent 文件系统形式化证明

> 目录名：`文件系统形式化证明`（前身 `文件系统`）· 状态：as-built 记录 + **三套形式化系统** · 最后核对：2026-09-18
> **三套形式化**：F1 前置条件 / 零迁移（[06](./06-cordis-review.md) · [07](./07-formal-rootcause-and-fix.md)）·
> F2 可观测性（[08 §2](./08-state-observability-principle.md)）· F3 投递（[08 §2.2](./08-state-observability-principle.md)）——
> 总索引见 [00-formal-systems.md](./00-formal-systems.md)。
> **改动点总册**（每一处改动 ↔ 代码锚点 ↔ UT ↔ 真机 e2e ↔ 是否上线）：[11-change-register.md](./11-change-register.md)。
> **上层索引（L2）**：[../README.md](../README.md)（全部文档分桶 + 每篇一行；形式化入口同样指向 00）。
> 对象：`fastagent` 的 workspace（持久存储）与 sandbox `/workspace`（执行副本）之间的同步机制
> **英文版**：[`../fs-formal-proof/`](../fs-formal-proof/README.md)（同一套文档的 1:1 译本；代码、标识符、
> 路径、符号与面向 agent 的引文保持原样。中文版是 origin，两者章节编号一一对应，可并排阅读。）

## 这个目录解决什么问题

2026-09-17 生产环境出现**交付物静默回退**：用户在一个会话里被 agent 修好的三个文件
（`byo-account-design.html`、`qc-email-draft.md`、`todo.md`）在 agent 汇报"已恢复"之后
又被自动覆盖回更早的版本，而且全程没有任何报错。

排查结论是：这不是某个工具（`apply_patch` / `edit_file`）的实现缺陷，而是**工作区存在两份
副本、回写方向单向、版本仲裁缺位**这一组结构性问题。本目录把这套机制、它的语义错位、
时序、事故证据与系统性修法固化下来，供后续改动和评审引用。

## 阅读顺序

| 文件 | 内容 | 读者 |
|------|------|------|
| [**00-formal-systems.md**](./00-formal-systems.md) | **三套形式化系统的总索引**：F1 前置条件/零迁移（06/07）· F2 可观测性（08 §2）· F3 投递（08 §2.2）——各回答什么问题、如何组合、符号总表、文档地图、义务 ↔ 缺口 ↔ witness、仍开放项的形式化归类，以及 **§7 全量清点**（形式系统 / 机制层 / 子系统契约 / **单一来源族** / 模型 / 未形式化） | **任何人**（从这里进） |
| [01-current-implementation.md](./01-current-implementation.md) | 当前实现实录：端口、后端、写入者、同步路径、可观测性 | 需要改这块代码的人 |
| [02-semantics-and-architecture.md](./02-semantics-and-architecture.md) | 用 Clean Architecture 四层拆解系统语义，定位职责错位 | 做设计决策的人 |
| [03-state-machine-and-timing.md](./03-state-machine-and-timing.md) | 从状态变更 / 时序看同步原理，Docker 与 E2B 的差异为何成立 | 想理解"为什么 Docker 没事"的人 |
| [04-incident-workspace-2026-09-17.md](./04-incident-workspace-2026-09-17.md) | 本次事故：证据链、根因、历史归属（是否由租约引入） | 排查与复盘 |
| [05-remediation-plan.md](./05-remediation-plan.md) | 系统性解决方案、分阶段落地、测试与可观测性、决策记录（**其 P0/P2 已被 06 修正**，保留作为设计演进记录） | 排期与实施 |
| [06-cordis-review.md](./06-cordis-review.md) | 用 Cordis 形式化原则（revertible effect / 左逆 / 前置条件 / keyed diff / 系统边界）复审本设计，并给出修正后的 P0′–P3′ | 设计与评审 |
| [07-formal-rootcause-and-fix.md](./07-formal-rootcause-and-fix.md) | **根因的实证闭合（含活体沙箱直读）** + 用 Cordis 形式化语言描述缺陷、构造性证明、以及带前置条件的修复规则 | 设计权威 / 实施依据 |
| [08-state-observability-principle.md](./08-state-observability-principle.md) | **状态可观测性原则**：形式化表述（δ/σ）、**投递的形式化（§2.2：谁产生/谁投递/谁取走 + O1–O5 + P1"只有 pull 可达"）**、三条推论、当前 harness 逐项审计、新机制的审查清单、统一出口的架构决策 | 做任何 harness 改动前必读 |
| [09-sandbox-lifecycle-audit.md](./09-sandbox-lifecycle-audit.md) | **沙箱完整生命周期 × 文件系统交互的审计**：7 个状态、逐迁移可观测性判定、四个缺口与处置顺序 | 排查沙箱问题 / 设计生命周期改动 |
| [10-harness-state-audit.md](./10-harness-state-audit.md) | **全 harness 状态变更审计**：按"被 agent 触发 / 触发 agent"两类逐组件过 δ→σ，缺口 G5–G13（含一个本地复现的 P0：假信号） | 改任何 agent 状态前必读 |
| [11-change-register.md](./11-change-register.md) | **改动点总册**：F1/F2/F3 的每一处改动 ↔ 代码锚点 ↔ UT ↔ 真机 e2e ↔ 上线状态（工作区 vs 线上 HEAD） | 上线排期 / 评审 / 交付核对 |
| [12-lease-formal-design.md](./12-lease-formal-design.md) | **租约的形式化设计**：从 `channel_leases` / Redis / `sandbox_leases` 归纳出的六条义务 L1–L6、as-built 归类、**反例 G25（沙箱围栏令牌每代归 1，实测）**、`session_turns` 的实例化与四层归属 | 设计任何跨副本串行化机制前必读 |

## 一句话结论

> `/workspace` 在 docker 上是宿主目录的 bind mount（**同一份**），在 e2b / boxlite 上是
> 沙箱内的独立副本（**两份**）。回写通道 `syncSnapshot` 用"字节数是否相同"判断要不要
> 覆盖，这个判据只在"单写入者"时成立；当宿主工具（`write_file` / `edit_file` /
> `apply_patch`）已经写过 store 时，沙箱里的旧副本会把新版本盖掉。

第二个同族结论（2026-09-17 复核补充）：

> 从"逻辑路径"到"store 键 / 沙箱路径"的映射**没有单一来源**——store 写入、hydrate、
> 编码镜像、回写镜像各自实现了一套。其中编码镜像把路径硬编码拼到 `/workspace/` 根，
> 与 hydrate 的作用域展开不一致，因此"写两处保持一致"在当前实现里并不成立。
> 详见 [01](./01-current-implementation.md) §3.5 与 §8。

第三个结论（形式化复审，见 [06](./06-cordis-review.md)）：

> 同步不是"带方向的拷贝"，而是 **reconciler**。正确的问题是"这条路径这次迁移的前置条件
> 是否成立"：成立则迁移并更新基线；不成立则**报错且零迁移**。据此，
> 05 里"冲突时另存 `shadow` 文件"的做法被判定违规（它改变了 entry 集合、不收敛），已作废。

第四个结论（根因已闭环，见 [07](./07-formal-rootcause-and-fix.md)）：

> 根因是 `Sync` **没有任何前置条件**：它从"状态差异"直接推断"意图方向"。
> 该推断在 docker 上恒真（`X ≡ S`，`T3` 分支不可达），在 e2b/boxlite 上失效。
> 修复不是"换一个判据"，而是把 `Sync` 变成带前置条件的 reconciler，
> 并在宿主写入后**把权威内容推回缓存**——两半缺一不可。

第五个结论（规则 (3) 的判据，见 [07 §3.8](./07-formal-rootcause-and-fix.md)；**该形态已下线**）：

> 当时的判据是 **hydrate 时的内容摘要**：同一个痕迹（"store 对象仍是 hydrate 那一份、
> 沙箱字节不同"）既可能是沙箱编辑、也可能是宿主编辑，只有"上次交给沙箱的那份内容指纹"
> 能把两者分开。这需要**记住**一份指纹，而记忆在跨 pod 沙箱租约下会给出**随副本而变**的
> 裁决，因此 2026-09-18 被换成两份副本**自描述**的 `size + mtime` 判据（07 §3.11.3）。

第六个结论（最终形态：穿透 + 前置条件，见 [07 §3.3](./07-formal-rootcause-and-fix.md) / §3.11.3）：

> 事故的第三个必要条件（"宿主写过、两副本不同"）在 docker 上永不成立，因为 bind mount
> 让 `X ≡ S`。**写入穿透就是把这条性质搬到远程沙箱**：宿主工具写文件时同时写进沙箱，
> 并把沙箱文件的时间戳对齐到 store 对象的时间戳——于是"两副本是不是同一版本"靠
> **两份副本自己的 `size + mtime`** 就能判（元数据不同时才读字节比对），
> **任何地方都不需要记住什么**，跨副本、跨重启结论一致（07 §3.11.3）。
> 穿透逐路径进行、失败也逐路径记录（只影响那一个路径）。

第七个结论（允许不同步，但必须可识别、可恢复，见 [07 §3.11](./07-formal-rootcause-and-fix.md)）：

> **不做 CAS，也不留兜底副本**：覆盖前只**观测一次**，把"替换了一个不同的版本（N 字节）"
> 写进工具结果，然后照常穿透。前两版（CAS 拒绝、保全 `.sandbox-version` 副本）都被推翻——
> 前者制造死锁，后者与感知通道重复。机制因此收敛成四个：穿透 / 无记忆的版本判定 /
> 拒绝 + 信号 / exec 变化信号（07 §3.11.1），且没有死锁。

第八个结论（感知的不对称，见 [07 §3.11.1](./07-formal-rootcause-and-fix.md)）：

> **agent 能感知 store 的变化（它自己写的），感知不到沙箱的变化（脚本改的）。**
> 事故隐蔽的根源就是后者没有通道。所以同步现在会**主动发出信号**：
> `exec` 的结果里附上「沙箱改了哪些路径、已同步」或「哪些路径因两侧都动过而未同步」。
> 这条通道让"两副本是否一致"变成可观测状态——保全与解析工具是它的兜底，而不是唯一手段。

第九个结论（**状态可观测性原则**，见 [07 §3.12](./07-formal-rootcause-and-fix.md)）：

> **harness 内的状态变更必须让 agent 可感知**，否则 agent 会基于一个已经不存在的世界推理。
> 关键是区分"agent 自己造成的"（工具结果即回执）与"发生在它世界里的"（默认完全不可见）——
> 后者必须显式发信号，且要进 agent 真正读的通道（工具结果），不是 slog。
> 按此审计本目录相关的机制：文件同步这一族已补齐（含空闲驱逐期发生的变更），
> 另发现上下文压缩、后台记忆更新、技能列表刷新三处"改了但没说"，记在 §3.12 待处理。

**这条原则现已独立成文**：[08-state-observability-principle.md](./08-state-observability-principle.md)。
它不是文件同步的附带结论，而是约束整个 harness 的设计要求——形式化表述见其 §2
（agent 的 `Belief` 与世界 `World` 的一致性），落地审计与**新机制审查清单**见 §5/§6。
§5 的审计已全部收敛：文件同步一族（含驱逐期变更）与"缺省不可见"一族
（压缩 / 记忆 / 技能 / 工具集）现在都有信号，后者由**一个统一的每轮环境变化信号**承担，
而不是各子系统各发明一条提示。

## 验证策略：哪些结论必须用真实 E2B 验证

不是所有断言都值得起一个云沙箱——假件足够快、足够确定。判据只有一条：

> **只要断言里出现"后端物理事实"，就必须在真实后端上验证**；
> 纯逻辑（编排、判据、收敛性）用假件。

| 断言类型 | 例子 | 验证方式 |
|---------|------|---------|
| 后端物理事实 | 沙箱与 store 是**两份**副本；hydrate 保留 store 的写入时间；沙箱内路径布局 | **真实 E2B**（`TestE2BLive*`） |
| 编排与判据 | 前置条件表、零迁移、收敛、局部性、store-only 键 | 假件（`lifecycle_sync_contract_test.go`） |
| 历史事实 | 缺陷引入于 `950070b`、租约是放大器 | `git log`/`git show`（无需运行） |
| 生产事实 | 事故会话的字节数、日志、对象时间戳 | 已固化为 §1.2/§2.5 的证据表（一次性核查） |

具体到本目录，真实 E2B 的三个验收测试见
[07 §3.9](./07-formal-rootcause-and-fix.md)；它们覆盖的是 01 §2/§3.5 的对照表、
07 §1.1 的元数据观测和 §3.3 的修复承诺——也就是那些"只有真后端才能证伪"的结论。
其余全部落在假件与文档核对上。

运行方式（不进常规 CI，需显式开闸）：

```bash
FASTAGENT_E2B_LIVE=1 E2B_API_KEY=e2b_... \
  go test ./internal/sandbox/ -run TestE2BLive -v -count=1
```

## 相关既有文档

- [../sandbox-pool-leases.md](../sandbox-pool-leases.md) — 跨 pod 沙箱租约
- [../sandbox-scope-leak.md](../sandbox-scope-leak.md) — 作用域隔离与 hydrate 失败
- [../coding-agent-runtime.md](../coding-agent-runtime.md) — coding 子目录与 dev server 预览
- [../per-chatter-files.md](../per-chatter-files.md) — 每 chatter 文件路由
