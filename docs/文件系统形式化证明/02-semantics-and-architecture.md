# 02 · 用 Clean Architecture 拆解系统语义

> 状态：设计分析 · 最后核对：2026-09-17
> 作用：回答"当前实现把职责放错了哪一层"。阅读前建议先看 [01-current-implementation.md](./01-current-implementation.md)。
> 方法论：Robert C. Martin 的四层同心模型 + SOLID + 组件耦合；每个建议都过一遍 Musk 五步闸门（见 §7）。

## 1. 四层映射

| 层 | 本系统中对应的东西 | 应该负责什么 | 当前实际状态 |
|----|-------------------|-------------|-------------|
| **Entities** | "工作区是一份持久状态"这条不变式 | 声明**谁拥有版本**：任何一次成功的写入都不得被无关的后台动作静默覆盖 | 不变式不存在。同一份 store 在三个地方被赋予三种身份（见 §1.1） |
| **Use Cases** | `LifecyclePool.syncSnapshot`、`mirrorSandboxWrite`、`flushIfSupported`（[lifecycle.go](../../internal/sandbox/lifecycle.go)） | 编排两个写入者，冲突可判定 | 退化成"拷贝函数"，判据是字节数——该判据只在单写入者时成立（见 §1.2） |
| **Interface Adapters** | `RemoteWorkspace`、`WorkspaceSnapshotter`、`PortExposer`（[executor.go](../../internal/sandbox/executor.go)） | 把后端物理事实**翻译**成策略可用的词汇 | 只报"能力位"，不报语义。`RemoteWorkspace` 携带的正是仲裁需要的事实，却只在"要不要同步"上被使用（见 §1.3） |
| **Frameworks & Drivers** | docker bind mount、e2b/boxlite 沙箱 fs、S3/本地 FS | 只提供事实 | 事实确实只被提供，但**未被声明**，于是由上层从副作用反推（见 §1.4） |

### 1.1 Entities 层：没有所有权规则

真正的不变式只有一句：

> 任何一次成功的写入，都不能被一次与用户请求无关的后台动作静默覆盖。

这句话在代码里**不存在**。取而代之的是三处互相矛盾的隐含假设：

| 位置 | 它假设 store 是…… |
|------|------------------|
| [workspace.go](../../internal/workspace/workspace.go) 包注释："durable blob store for agent-generated artifacts" | 产物的**归档**，不是主副本 |
| [file.go](../../internal/agent/tools/file.go) 第 494 行：`read_file` 先读 store，失败才回退 executor | **权威主副本** |
| [lifecycle.go](../../internal/sandbox/lifecycle.go) 第 339 行注释："upload anything the sandbox wrote" | 沙箱的**助手留档**，沙箱才是源头 |

三种身份，没有一条规则说明"同一路径可以有两个写入者"。后果是每个局部看起来都合理，
而整体没有任何地方对"谁赢"负责——这正是缺陷能潜伏数月的原因。

**应有的形态**：把所有权写下来，并体现在类型与注释里，例如
`// workspace.Store 是同一逻辑路径的唯一权威副本；沙箱副本是可丢弃的缓存视图`，
或者相反的声明 `// 沙箱是权威，store 是归档`——两种都可以，但必须选一种并被所有写入者遵守。

### 1.2 Use Cases 层：判据与后端耦合

`syncSnapshot` 是**唯一同时见到两位写入者**的地方（宿主工具写 store、`exec` 写沙箱），
所以冲突仲裁天然属于它。它现在的判据是：

```go
if info, err := p.workspace.Stat(...); err == nil && info.Size == int64(len(data)) {
    continue
}
```

这个"大小相同就跳过"的优化，其成立前提是"store 是沙箱的镜像"——一个**后端相关**的前提：

| 前提是否成立 | 后果 |
|-------------|------|
| 单写入者（docker，物理共享） | 大小相同 ⇔ 内容相同 ⇔ 无需动作。判据正确 |
| 双写入者（e2b / boxlite） | 大小相同 ≠ 内容相同（**同长度编辑会被永久漏掉**）；大小不同 ≠ 快照更新（本次事故正是如此） |

也就是说，粒度（增量 vs 全量）这一**性能决策**被写进了判据，而它依赖后端物理形态；
这是把 Frameworks & Drivers 的事实渗进了 Use Case 的源码——依赖方向反了。

另一个被忽略的事实：**写入者不止两个**。完整的写入者集合是
`{宿主工具→store, mirrorCodingWriteToSandbox→沙箱, exec→沙箱, mirrorSandboxWrite→store, syncSnapshot→store}`。
任何"谁最新"的启发式在这个集合上都无法自行成立。

### 1.2.1 补记：镜像路径的错位（对上面这条的加强）

上述五个写入者里，有两个使用了**不被任何地方校验的路径映射**：
`mirrorCodingWriteToSandbox` 把逻辑路径硬编码拼到 `/workspace/` 根
（[file.go](../../internal/agent/tools/file.go) 第 896 行），
而 hydrate 是按作用域前缀展开的（[01](./01-current-implementation.md) §3.5 的对照表）。
在松散会话中两者指向**不同位置**，因此：

- "镜像"既不能保证沙箱里的 hydrate 副本被更新，也不能保证不产生重复对象；
- `mirrorSandboxWrite` 又把镜像产生的那份以根键写回 store，于是 store 内部出现同内容多键。

这条补记把问题从"缺仲裁"推进到"缺一个被校验的路径映射"：
**从"逻辑路径"到"store 键 / 沙箱路径"的转换散落在五个写入者里各自实现**，
没有单一来源。这也是 [05](./05-remediation-plan.md) §7 把"统一路径解析"提前为 P3 首个动作的原因。

### 1.3 Interface Adapters 层：能力位替代了语义

端口应该把物理事实翻译成策略词汇。现有两个 marker 报的是：

```go
type WorkspaceSnapshotter interface { SnapshotWorkspace(ctx) (map[string][]byte, error) } // “我能给你字节”
type RemoteWorkspace interface { IsRemoteWorkspace() }                                    // “我的 /workspace 不与宿主共享”
```

没有回答的是：**这份快照是权威副本吗？它可能是过期的吗？当宿主写过同一路径时，我该让位吗？**
策略层因此只能靠比较字节数去反推后端类型。

需要更精确的批评（避免夸大）：`RemoteWorkspace` **确实携带了**"共享 / 分离"这一仲裁所需的事实，
而且它已被用于三处决策：

| 使用点 | 用途 |
|--------|------|
| [lifecycle.go](../../internal/sandbox/lifecycle.go) 第 735 行 | exec 之后**要不要**做快照回写 |
| [lifecycle.go](../../internal/sandbox/lifecycle.go) 第 772 行 | `WriteFile` 之后**要不要**镜像回 store |
| [file.go](../../internal/agent/tools/file.go) 第 893 行 | 宿主写入**要不要**镜像进沙箱 |

缺的位置是第四处：**仲裁"谁赢"**。所以准确表述是——
**事实已经报上来了，但被降级成"要不要打电话"的开关，而不是"电话里谁说了算"的依据。**

由此，修复不必然要新增接口：更省的做法是让这条既有事实在仲裁点被真正使用，并把它的语义写进注释。

### 1.4 Frameworks & Drivers 层：事实只被提供，未被声明

driver 层"只描述自己"是对的。问题在于描述出来的事实没有被翻译成上层可用的形式，
于是上层用副作用反推（"两份字节数一样吗？一样说明后端大概是共享的"）。

反推在 docker 上凑巧成立，在 e2b / boxlite 上失效——**不是适配器写错了，是端口没声明语义**：

| 事实 | 提供者 | 是否被表达 |
|------|--------|-----------|
| `/workspace` 是同一份还是两份 | docker 不声明 marker；e2b/boxlite 声明 `RemoteWorkspace` | 表达了，但语义未在仲裁点使用（§1.3） |
| 快照可能过期 / 可能来自远端 | 无 | **未表达** |
| 一份副本是否是"上一版" | 无（`ObjectInfo` 没有版本、没有作者） | **未表达** |

## 2. 依赖规则（Dependency Rule）

理想的依赖方向：

```
Frameworks & Drivers (docker bind mount / e2b fs / S3)
        ↑ 只实现抽象，不向上暴露细节
Interface Adapters (Executor markers / WorkspaceStore 实现)
        ↑ 把物理事实翻译成策略词汇
Use Cases (syncSnapshot / hydrate / mirror)
        ↑ 只依赖抽象；决策不因后端更换而改变
Entities (所有权不变式)
```

当前实际方向：

```
Use Cases  ──用字节数反推──▶  Frameworks & Drivers 的物理形态
```

这是典型的**依赖倒置缺口**：策略层依赖了它不应知道的细节，并且知道自己不知道，
于是用启发式去猜。`syncSnapshot` 里没有 `Backend()`，但它的正确性取决于后端是哪一个。

## 3. SOLID 视角

| 原则 | 当前违反点 | 说明 |
|------|-----------|------|
| **SRP** | `syncSnapshot` 同时做三件事：拉快照、判增量、写入 store | 三种职责，三个变化原因：后端形态变化、仲裁策略变化、存储 API 变化 |
| **OCP** | 新增一个后端（boxlite）需要重新评估既有判据是否仍然成立 | 扩展需要修改既有代码的"正确性前提"，而不是新增适配器 |
| **LSP** | `WorkspaceSnapshotter` 在 docker 上返回"宿主那一份"，在 e2b 上返回"远端那份" | 同一接口的两种实现语义不同，上层无法在同一假设下使用 |
| **ISP** | `RemoteWorkspace` 一个布尔值承担了三种用途（同步开关、镜像开关、隐含的冲突语义） | 接口过窄，语义承载不足；调用方只能自行补足理解 |
| **DIP** | 策略层用字节数反推后端物理事实 | 源码依赖指向了细节 |

其中 **LSP 是最核心的定性**：`WorkspaceSnapshotter` 这个名字让人以为"拿到工作区的字节"，
而它实际的含义随后端在"读取宿主自身"与"下载远端副本"之间摇摆。上层对这个接口的每个假设，
都只在一种后端上成立。

## 4. 组件耦合视角

- **CCP（共同闭包）**：`/workspace` 语义相关的实现分散在四个地方——
  `internal/workspace`（存储）、`internal/sandbox`（执行器与同步）、`internal/agent/tools`（文件工具）、
  `internal/runtime`（coding 运行时）。四者共享同一个变化原因（"工作区语义变了"），
  但不在同一个组件里，所以一次语义变更需要改四处，容易漏（§1 的"三种身份"就是漏的结果）。
- **SDP（稳定依赖）**：`internal/agent/tools` 是易变层（工具集持续增加），
  却直接依赖 `internal/workspace` 与 `internal/sandbox` 的具体行为（例如 `apply_patch` 直接拼 store 参数、
  绕过 `scopeSessionID()`）。稳定层（不变式）反而不存在。
- **SAP（稳定抽象）**：最稳定的应该是"所有权规则"这条抽象，它当前既不稳定也不抽象——因为它不存在。

## 5. 目标语义：一句话版本

建议明确采用下面这条（两种都可行，但必须唯一且被所有写入者遵守）：

> **store 是唯一权威副本；沙箱 `/workspace` 是它的缓存视图。
> 沙箱里"store 尚不存在的路径"属于沙箱产物，允许回写；
> 沙箱里"与 store 同路径"的内容只有在能证明它是由本次沙箱运行产生时，才允许回写。**

这条语义的设计后果：docker 天然满足（缓存视图就是本体），e2b / boxlite 需要显式区分
"新产物"与"已知路径的改动"，并需要一份**基线**（沙箱是从哪个版本 hydrate 的）才能证明后者。

## 6. 由此推导出的四个职责修正（方向，不是实现）

| 层 | 修正方向 |
|----|---------|
| Entities | 把所有权规则写进 `internal/workspace` 的包注释与类型文档，并作为评审依据 |
| Use Cases | `syncSnapshot` 拆成"取快照 / 判定 / 落盘"三步；判定只依赖抽象事实，不依赖字节数推断后端 |
| Interface Adapters | 让既有事实在仲裁点被使用（`RemoteWorkspace` 决定"是否允许回写既有路径"）；把"快照可能是旧版本"写进契约文档 |
| Frameworks & Drivers | driver 只报事实；docker 不需要参与任何回写路径（见 §7 的删除项） |

## 7. 反过度设计闸门（Musk 五步）

**Step 1 · Question（让需求不那么蠢）**

- "需要版本历史 / 内容哈希 / 冲突三方合并吗？"——**不需要**。真实需求是"不能让成功写入被静默覆盖"，
  以及"exec 产生的新文件要能被看到"。版本谱系是臆想需求。
- "需要给 docker 也接一套同步协议吗？"——**不需要**。docker 物理上就是同一份，任何"同步"都是自欺。

**Step 2 · Delete（删除）**

- **删除 docker 的所有回写动作**：`SnapshotWorkspace` 每次驱逐都 walk 一遍宿主目录、读全部文件、
  再逐个比较大小后跳过——纯浪费。对 `RemoteWorkspace == false` 的后端直接短路。
- **删除"用字节数推断后端"的隐式判据**：不是优化它，而是不再需要它。
- **不要新增 `MountKind` 之类的平行枚举**：`RemoteWorkspace` 已经表达了同一事实，
  再造一个名字等于把隐式契约换成"两个名字的重复"，评审成本上升而语义收益为零。

**Step 3 · Simplify（简化）**

- 判据简化为"路径是否已存在于 store"这一条显式规则 + 一条显式的基线校验；
- 三后端三场景的验证矩阵用同一张表驱动，而不是为 e2b 写特例分支；
- 冲突处理先选最简单且**不销毁数据**的动作（保留原对象、把冲突版本另存并告警），而不是先做合并。

**Step 4 · Accelerate（加速）**

只有在 Step 1–3 完成后才谈：把"沙箱产物回写"做得更快（例如只在真正变化的路径上做 tar）、
把 hydrate 的大小限制与错误分类做细。当前不是性能问题。

**Step 5 · Automate（自动化）**

在手工跑通并稳定一周之后，再考虑把"冲突检测 → 通知 agent 重放"自动化，
或把基线指纹纳入沙箱租约行的自动维护。当前先靠日志与一次性脚本。

## 8. 结论

当前系统在 docker 上"看起来正确"是物理共享的巧合，在 e2b / boxlite 上出错是**端口语义缺位**的必然。
最小正确的方向不是加一套同步协议，而是：

1. 写下所有权规则（Entities）；
2. 让仲裁只依赖显式事实、不再比较字节数（Use Cases）；
3. 让既有事实在仲裁点被使用、并把语义写进契约（Interface Adapters）；
4. 删掉 docker 上无意义的回写动作与所有"猜后端"的分支（Frameworks & Drivers）。

具体落地步骤、测试矩阵与决策记录见 [05-remediation-plan.md](./05-remediation-plan.md)。
