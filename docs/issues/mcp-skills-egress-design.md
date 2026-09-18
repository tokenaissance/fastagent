# 设计：云端 skill 库的 MCP 出口（基于 fastagent）

**状态**：设计（未实现） · **日期**：2026-09-18
**方法**：Clean Architecture 四层 + 依赖规则 + Musk 五步门；形式化沿用 `docs/fastagent/fs-formal-proof` 的 F1/F2/F3 约定
**关联**：`mcp-skills-fastclaw.md`（提案）、`mcp-server-capability-map.md`（能力清单）

---

## 0. 四个决策（结论先行）

| # | 问题 | 决策 | 依据 |
| :-- | :--- | :--- | :--- |
| D1 | 粒度：用户级还是 agent 级 | **目录按 agent，凭据按用户**。URL 形如 `/mcp/agents/<agent-id>`，token 用用户已有的 apikey | §3 |
| D2 | 平台公共 skill 是否默认包含 | **默认包含**（platform 层是 agent 视图的固有成员） | §3.4 |
| D3 | `Gated` 的 skill 发不发布 | **照发**。gating 是"我们运行时的环境属性"，不是 skill 的内容属性 | §4 |
| D4 | 目录名与 frontmatter `name` 不一致 | **发布身份一律取 frontmatter `name`**；不一致在入口（安装/水合）治理，不在出口绕过 | §5 |

## 1. 现状事实（先把前提钉死）

**skill 的所有权与安装口径**（`internal/setup/skill_install.go` 的 `authorizeSkillInstallTarget`）：

| 安装目标 | 落盘位置 | 谁能装 |
| :--- | :--- | :--- |
| 全局 | `~/.fastagent/skills/`（对象存储 owner `_global`） | **平台管理员**（`CanAdminPlatform`） |
| 某个 agent | `~/.fastagent/agents/<id>/skills/`（owner = agentID） | **该 agent 的 owner**（`requireAgentOwner`） |
| 某个 chatter | `~/.fastagent/users/<uid>/skills/`（owner `_user_<uid>`） | 运行时自动（`MirrorSkillsUp`），无对外安装入口 |

**关键推论：代码里没有"账号用户级"的 skill 目录。** "用户级"不是一个现成的所有权范围，它只能是一个**投影**（该用户名下所有 agent 的并集）。

**运行时视图**（`internal/agent/skills.go` 的 `LoadSkills`，优先级从低到高）：

```
extra → managed(~/.fastagent/skills/) → user(home/skills) → team → personal(per-chatter) → agent → bundled
```

合并规则是 `map[目录名]Skill`，**后面的层覆盖前面的层**；`personal` 层（chatter 私有）夹在中间，`agent` 层最高。也就是说：**一个 agent 的目录视图已经有一套定义好的优先级与命名空间**。

**凭据与授权**（`internal/auth/auth.go`）：apikey `type=user` 的 `APIKeyAgents` = 该用户名下所有 agent（每次请求现算）；`type=agent` 走 `apikey_agents` 显式 ACL；`type=admin` 任意 agent。

**HTTP 面现状**：`internal/setup/server.go` 建 mux、挂 `/api/*`，并在同一 mux 上挂 OpenAI 兼容的 `internal/api`。**全仓库没有 MCP server 面**（`internal/mcp` 只有 Client）。

## 2. Clean Architecture 四层映射（依赖只能向内）

| 层 | 这里的职责 | 落在哪 |
| :--- | :--- | :--- |
| **Entities** | 不变式：发布身份 =（scope, frontmatter name）；可发布条件（frontmatter 合法、`name` == 目录名、512 文件 / 16 MiB）；`digest = sha256(原始字节)`；entry 自洽 | 新包 `internal/skillserve`（纯逻辑，不 import `net/http` / `store` / `agent`） |
| **Use Cases** | ①调用者 → scope ②枚举层、构建 entry ③按 URI 解析并校验一次读取 | 同上：`catalog.go` / `entry.go` / `read.go` |
| **Interface Adapters** | 入站：MCP 传输（JSON-RPC over Streamable HTTP：`initialize`/`server/discover`、`skills/*`、`resources/*`、兜底 tools）。出站：文件系统/对象存储读适配、`auth.Identity` → scope 适配 | 入站 `internal/mcpserver`（新）；出站 `internal/skillserve/fslayer`（新），水合复用 `internal/skills` |
| **Frameworks & Drivers** | `net/http` mux（`internal/setup/server.go` 挂 `/mcp/...`）、`.fastagent` 目录布局、对象存储、`internal/auth` | 不改语义，只提供事实 |

**依赖方向**（源码依赖，非控制流）：

```
setup/server.go ──▶ mcpserver(入站适配) ──▶ skillserve(用例) ──▶ ports ◀── fslayer / auth 适配(出站)
                                                 ▲
                                      internal/skills(布局的单一源)
```

两条硬规矩：

1. `skillserve` **不得** import `internal/agent`（重量级编排层）；层枚举与优先级必须来自 `internal/skills` 的**单一源**，agent 加载器与 MCP 出口共用它。
2. 入站适配器不得直接读盘；出站适配器不得知道 JSON-RPC。

**为什么先抽出布局的单一源**：你们的形式化文档已经给出同族结论 ——"逻辑路径 → store key / sandbox path 的映射没有单一源，五个 writer 各写一份"（01 §3.5 / §8）。MCP 出口若自己再写一遍层枚举，就是第六份。

## 3. D1 粒度分析：用户级 vs agent 级

### 3.1 七个判定维度

| 维度 | agent 级（目录 = 这个 agent 的视图） | 用户级（目录 = 该用户名下 agent 的并集投影） |
| :--- | :--- | :--- |
| **所有权范围** | 已存在（`agents/<id>/skills/`，owner = agentID） | 不存在；要么新增"用户 skill 库"，要么定义"并集" |
| **变化轴（CCP）** | 一个 agent 的 skill 变化 | 任一 agent 变化 + 平台库变化 → 更宽的变化轴，更难复用与缓存 |
| **命名/冲突** | 合并规则已定义，视图内名字唯一 | 跨 agent 必然同名，必须靠前缀消歧 |
| **凭据** | 用户 apikey 与 agent apikey 都能表达 | 只能用用户 apikey；agent apikey 语义不清 |
| **隐私层** | `personal` 属于 chatter，不属于 agent → 天然排除 | 同样要排除，且 token 里没有 chatter id，取不到"哪个 personal" |
| **host 侧审批/缓存** | URI 稳定，一个 agent 一份审批 | 同一 skill 因前缀不同变成不同 URI，审批按 agent 分裂 |
| **产品心智** | "给我的这个 agent 装技能" | "我在云端装了一堆 skills" |

### 3.2 结论

**目录按 agent，凭据按用户。** 用户仍然只粘贴"一个 URL + 一个 token"：

```
POST https://<cloud>/mcp/agents/<agent-id>
Authorization: Bearer <该用户已有的 apikey>
```

理由：

1. **agent 是唯一已存在、且已有优先级定义的范围**；用户级视图今天只能靠并集拼出来（§1）。
2. **凭据粒度与目录粒度不必一致**：`type=user` 的 apikey 已经把"该用户名下的 agent"解析好了（`APIKeyAgents` 每次请求现算），端点只需判定"URL 里的 agent ∈ 该集合"。既不新造 token 类型，也不给用户增加"选哪个 key"的心智负担。
3. **URI 前缀 = agent**（`skill://<agent>/<name>/SKILL.md`）一次解决三件事：满足规范"末段 = frontmatter name"、跨 agent 同名不冲突、host 侧审批按 agent 稳定。
4. **CCP**：这个端点的变化原因是"某个 agent 的 skill 变了"，与 agent 级视图同轴；用户级并集把多个变化轴绑进一个组件。

### 3.3 什么时候再做"用户级并集端点"

按 Musk 第一步（质疑需求）与你们的"变化轴 ≥3 次历史证据"门槛：等"同一批 skill 要同时给多个 agent、不想逐个装"反复出现之后再做。届时它是**投影**（前缀仍是 agent），不是新的所有权范围 —— 本设计的 URI 形状已经为它留好位置。

### 3.4 D2：平台公共 skill 默认包含

`managed`（`~/.fastagent/skills/`）本来就是 agent 视图里的一层（Layer 3），优先级低于 agent 自己的层。所以"默认包含"不是新规则，而是**不额外过滤**；被 `skills.entries` / `disabled` 关掉的仍然过滤（与运行时一致）。

### 3.5 一个需要产品确认的 gap

如果产品真正想要的是"**每个用户一份自己的 skill 库**"（跨 agent 共享、与全局库分优先级），那是**新的存储范围**：等于给加载器加一层，并要回答"谁安装、与 agent 层谁优先、删了怎么办"。现在代码里没有这一层，建议当作独立需求评估，不要顺手塞进 MCP 出口的实现里。

## 4. D3：`Gated` 是什么，为什么照发

**Gated 的含义**（`internal/agent/skills.go` 的 `checkGating`）：SKILL.md 的 `metadata.<fastagent|fastclaw|openclaw>.requires` 声明 `bins` / `anyBins` / `env` / `config`。加载时逐项检查**我们这台机器 / 这个 agent 的运行环境**；缺任一项就 `Gated=true` 并给出 `GateReason`。代码注释写明了它为什么仍留在目录里：*"Keep gated skills visible in the catalog so the agent can explain missing credentials or platform support instead of claiming the skill is not installed."*

**为什么对外照发**：gating 判断的是**我们运行时**的环境，不是 skill 内容的质量。对外只发布"说明书"，执行环境由本地 host 决定 —— 我们这边缺一个 env 变量，不代表用户本地缺。

三条落地规则：

1. **不注入标记**：entry 的 `frontmatter` 必须与 SKILL.md 逐字段一致（规范硬要求），所以不能加 `gated: true`；`Skill` entry 也没有留给我们放自定义字段的位置。
2. **`requires` 原样透传**：它本来就在 frontmatter 里，外部 host 会忽略不认识的键，不影响合规。
3. **gating 不构成"发不出去"**：真正发不出去的是 frontmatter 非法、超限、名字冲突、名字与目录不一致 —— 这些必须进诊断（§6 的 O6）。

**一个真实的兼容性坑（要写进文档）**：`loadSkillContent` 会做 `{baseDir}` 替换，而 MCP 出口必须发原始字节（digest 要能对上）。所以依赖 `{baseDir}` 的 skill 在外部 host 里会看到字面量 `{baseDir}`。建议在 dashboard 的"接入"区块把"含 `{baseDir}` 的 skill"列为诊断项，让作者改成相对路径。

## 5. D4：命名与 URI，按协议来

**规范的三条硬约束**：

1. URI 形如 `skill://<skill-path>/<file-path>`，`SKILL.md` 显式出现；
2. `<skill-path>` 的**末段必须等于 frontmatter 的 `name`**；
3. 被发布的 skill 必须符合 Agent Skills 规范 —— 而该规范要求 `name` 与**父目录名一致**。

**现状（代码事实）**：`discoverSkillsEnhanced` 用**目录名**做 `Name`，并显式丢弃 frontmatter 的 name（`_ = fm.Name`）；`load_skill` 也按目录名找。也就是说，今天"目录名才是身份"。

**按协议来的做法 —— 名字只留一个源**：

```
skillName(dirName, frontmatter) -> (name string, err error)

  frontmatter 缺少合法 name        -> 不可发布（Agent Skills 要求 name + description 必填）
  frontmatter.name 不符合命名规则  -> 不可发布
  frontmatter.name != dirName      -> 不可发布，并在入口修（见下表）
  否则                             -> name = frontmatter.name（此时 == dirName）
```

三层治理，都在**入口**，不在出口：

| 时机 | 动作 |
| :--- | :--- |
| 安装 / 上传 / 水合 | 校验 `name == 目录名`；不一致就**拒绝安装**并给出明确错误（或按 frontmatter 统一改名后落盘）。此后磁盘与协议一致 |
| 发布（每次请求） | 遇到历史遗留的不一致：**不发布该 skill**，把原因写进诊断（不是静默丢弃） |
| 同名归一化后冲突 | 同一 agent 内按既有优先级取一个，被压掉的那个进诊断。**不允许改末段消歧**（规范禁止） |

#### 5.1 "按 frontmatter 统一改名"具体指什么

安装/上传时落盘的目录名来自**请求参数**（ClawHub 的 slug、GitHub 的 repo、上传表单的名字或 zip 顶层目录），
而 skill 的名字由**作者写在 SKILL.md 里**。两者不一致时的处理叫"统一改名"：

```
安装前： target/<请求名>/SKILL.md   frontmatter: name: <作者名>
改名后： target/<作者名>/SKILL.md   （同一份字节，只换目录名）
        SyncSkillUp(..., <作者名>, target)   // 对象存储 key 也必须用作者名
```

三条边界：

1. **改名必须发生在 `SyncSkillUp` 之前**，否则对象存储里仍是旧 key，其他 pod 水合回来又是旧名字
   —— 这正是形式化文档里"映射没有单一源"的同一类问题。
2. **目标目录已存在**时**拒绝安装**并报错，不覆盖（覆盖等于静默替换别人已装的 skill）。
3. **没有 frontmatter / 名字非法**的情况改不了名（没有目标名可用）：本地保持原样并在诊断里标出，
   但对 MCP 发布而言它不可发布（Agent Skills 要求 name + description 必填）。
   真实样本：`skills/fastagent-api-integration/SKILL.md` 就没有 frontmatter。

改名不改变任何文件字节，因此 digest 不变、相对引用不变；差异只在目录名，
且安装结果里要回显 `renamed: <请求名> → <作者名>`，避免用户以为装的是请求名。

**一句话**：末段永远等于 frontmatter `name`；不一致在入口治理，出口只做忠实发布。更彻底的做法（另开一张单）是把 `SkillsLoader` 的键也统一到 frontmatter name，让"身份"在整个系统里只有一个定义 —— 这正是你们形式化文档里"映射没有单一源"的同族问题。

## 6. 形式化：F1 / F2 / F3 与新增的 O6

### 6.1 F1 —— 前置条件与零迁移

发布路径是**只读**的。把一次读取写成前置条件：

```
READ(scope, uri) 的前置条件:
  P1  uri ∈ Entry(scope).resources      -- 未列出的文件 = 校验失败（规范）
  P2  sha256(bytes) == digest(uri)
  P3  len(bytes)    == size(uri)
  违反任一 -> 返回错误，且绝不返回未通过校验的字节
```

零迁移（本设计的硬边界）：**MCP 读路径不新增任何写**。对象存储水合沿用现有实现与其既有前置条件（skip-if-size-matches、`keepLocal` 保留 bundled），不得为出口再造第二条写路径。

### 6.2 F2 —— 可观测：δ ⇒ σ

| δ（世界变化） | σ（客户端能读到的那句话） |
| :--- | :--- |
| 某 skill 被安装 / 删除 / 改写 | 下一次 listing/get 里 entry 的 `digest` 变化（或消失） |
| 某 skill 被我们**拒绝发布** | 诊断项：name + 原因（frontmatter 非法 / 超限 / 名字冲突 / 名字与目录不一致） |
| 列表不完整（水合失败等） | 应答里的诊断（不得把不完整的目录当完整目录） |

**O6（本系统新增的义务）：拒绝必须发声。** 规范侧的对应物是"空列表 ≠ 没有 skill"；服务端的对应物是"**我们没发出去的 skill，客户端有办法知道它存在但被拒**"。

### 6.3 F3 —— 投递：produce / place / take

| 角色 | 在这里 |
| :--- | :--- |
| produce | entry builder (`skillserve/entry.go`) |
| place | `skills/list` / `skills/get` / `resources/read` 的应答 |
| take | 本地 host 的下一次调用（纯拉模型，**P1 天然成立**：我们没有推送通道） |

- **O1 真话**：entry 与实际字节一致（§6.1 的 P1–P3）。
- **O2 落点**：诊断放在客户端一定会读的应答里（listing / get），不能只写日志。
- **O3 取用时刻**：客户端下次调用时取；`ttlMs` 只是新鲜度提示。
- **O4 不丢**：服务端无队列、无内存态；entry 按请求现算（或按 digest 键缓存，缓存不是真相）。
- **O5 不吵**：稳定排序、稳定 digest；没有变化时不得产生"变化"。

### 6.4 请求级状态机与不变式

```
未认证 --401--> (结束)
   | Bearer 有效
已认证(identity) --URL 里的 agent 不在可访问集合--> 403
   | 在集合内
scope(agent) --枚举层(带水合前置条件)--> 候选集 --构建 entry(校验/摘要/上限)--> 应答
                                                      `-被拒项 --> 诊断
```

| 不变式 | 内容 | 见证（测试 / 证据） |
| :--- | :--- | :--- |
| **I1 作用域隔离** | 返回的 skill 都属于该 token 可访问的 agent；`personal` 层永不出现 | 跨租户 / 跨 agent 越权测试 |
| **I2 URI 确定性** | 同一 (scope, skill 内容身份) 在所有请求里得到同一 URI | 两次 listing 逐字节相等 |
| **I3 摘要忠实** | 返回字节满足 P2 / P3，否则报错 | 篡改文件后读取必须失败 |
| **I4 只读** | 发布路径不产生任何写（除既有水合） | 对可写目录做前后快照 |
| **I5 完整诚实** | 列表要么完整，要么带诊断 | 水合失败注入测试 |
| **I6 身份唯一** | 发布身份只有一个定义（frontmatter name） | 名字不一致的 fixture 不被发布且有诊断 |

## 7. Musk 五步门（为什么不做那些"看起来该做"的东西）

**① 质疑需求**：需要"用户级 skill 库"吗？今天不需要 —— agent 级范围已存在，用户级只是并集投影（§3.3）。需要服务端审批吗？不需要 —— 审批是 host 的职责（规范明确）。

**② 删除**：不做 `skill://index.json`（规范已删除该形态，改用 `skills/list`）；不做打包/zip 下发（规范不定义）；不做服务端 approval store；不做 per-host 专属格式；**不复用第二套层枚举**（必须与 agent 加载器共用单一源）；不新增数据库表（目录就是事实来源）。

**③ 简化**：一个纯函数决定 URI；一次 walk 同时得到 digest / size；读取返回原始字节；兜底只需两个只读工具（`list_skills` / `read_skill`）。

**④ 加速**：先用 MCP Inspector 手动跑通，再上真实 host，最后才把 conformance 接进 CI。

**⑤ 自动化**：只有前面都稳定之后，才考虑缓存、批量索引之类的优化。

## 8. 文档同构（L1 / L2 / L3）

| 层 | 要更新什么 |
| :--- | :--- |
| **L1** | 仓库 README / 架构段落：加"云端 skill 的 MCP 出口 + 端点 URL 形态（`/mcp/agents/<id>`）"一行 |
| **L2** | `internal/skillserve` 的模块说明（每文件一句：entity / usecase / ports）；`internal/mcpserver` 的适配器说明；`internal/skills` 增加"布局单一源"的说明 |
| **L3** | 每个新文件头按仓库惯例写 INPUT / OUTPUT / POS / PROTOCOL |
| **docs/** | 本设计 ←→ 提案 ←→ 能力清单三者交叉链接；D1–D4 变更必须同步到提案文档 |

## 9. 验收与见证

1. **规范一致性**：`sep-2640-skills-manifest` / `-enumeration` / `-directory` 三个 server 场景；兜底工具在只支持 tools 的 host 上实测。
2. **不变式**：I1–I6 每条一个测试（§6.4 表）。
3. **端到端**：在至少一个真实本地 Agent（Claude Code / Codex CLI）上"接 URL + token → 列出 → 加载并照做"，全程不出现 git 与安装步骤。
4. **诊断**：构造"名字不一致 / 超限 / 非法 frontmatter"的 fixture，确认它们**不被发布但在诊断里可见**。
