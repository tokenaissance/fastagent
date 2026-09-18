# Issue 提案：云端 skill 库的 MCP 出口（FastAgent / FastClaw 作为 Server）

> 本地 Agent 接一个 URL + token 即用，不需要 git。

**状态**：提案（未实现，待排期） · **目标仓库**：`tokenaissance/fastclaw` · **日期**：2026-09-18

**规范来源**：[modelcontextprotocol/ext-skills](https://github.com/modelcontextprotocol/ext-skills)
—— `specification/stable/skills.mdx`（扩展名 `io.modelcontextprotocol/skills`，基线协议 revision `2026-07-28`；
SEP-2640 已转为 Final）。实现清单见同仓库 `docs/implementations.md` 的 Servers 一栏。

---

## 1. 一句话

**用户在云端装一堆 skill，我们用一个 MCP 端点把它们发出去；用户本地的 Agent
（Claude Code / Codex / 任何 MCP host）只接一个 URL + token 就能用这些 skill
—— 不需要 git、不需要 `skills install`、不需要同步。**

## 2. 需求场景与现状差距

这件事今天做不到，缺的不是 skill 的存储与管理，而是**出口**：

- **云端已经有了**：skill 的安装（ClawHub / GitHub / 对象存储）、分层合并、frontmatter 解析、
  gating、dashboard 管理页（`internal/agent/skills.go`、`internal/skills/`、`/api/skills/*`）。
- **缺的是**：一个 MCP 端点，让别的 host 能"列出"和"取到"这些 skill。

MCP 分发相比 git 分发好在哪（这是需求背后的真正动机）：

1. **零安装步骤**：不用 clone、不用装依赖、不用管 skill 目录放哪、不用跑 `npx skills add`。
2. **没有版本漂移**：内容更新发生在云端，host 侧读时按 digest 校验，拿到的就是当前版本。
3. **权限与撤销在云端统一**：谁能用哪些 skill 由 token 决定，撤掉权限即刻生效。
4. **跨 host 同一份**：同一个端点可以被编辑器、CLI agent、别的 runtime 同时消费。

这条需求也把对外面**收窄**了：只做**只读的 skill 分发**，不涉及执行 ——
正好是能力清单里风险最小的那一组（组 A + skills 扩展），不需要开 `exec` / `spawn_subagent`
那一层（见 `docs/issues/mcp-server-capability-map.md`）。

与 host 侧的分工：本提案聚焦**对外发布 skill**；"消费别人 server 的 skill"见姊妹提案
`docs/issues/mcp-skills-fastagent.md`。两份提案的差别是归属与验收口径，不是能力限制 ——
FastAgent 与 FastClaw 是同一棵代码树上的两个发行版（`Makefile` 用 `./cmd/fastclaw` 构建
`bin/fastagent`），所以这一侧实现写一遍，两个发行版都能当 server。

### 2.1 端点身份与作用域（已定）

**已定：目录按 agent，凭据按用户。** 一个 URL + 一个 token = 一个 agent 的 skill 视图：

```
POST https://<cloud>/mcp/agents/<agent-id>
Authorization: Bearer <该用户已有的 apikey>
```

三点理由（完整分析见设计文档 `docs/issues/mcp-skills-egress-design.md` §3）：agent 是代码里唯一
已存在、且已经有优先级定义的所有权范围；用户级今天只能靠"该用户名下 agent 的并集"拼出来；
URI 前缀取 agent 同时解决规范约束、同名冲突与 host 侧审批稳定性。
`type=user` 的 apikey 已经把"该用户名下的 agent"解析好了（`APIKeyAgents` 每次请求现算），
端点只需判定"URL 里的 agent 在不在这个集合里"。

三种粒度的对照（供以后扩展参考）：

| 粒度 | token 绑定 | 视图内容 | 对应需求 |
| :--- | :--- | :--- | :--- |
| **agent 级（v1）** | user 或 agent | 该 agent 的目录视图（agent 层 + 平台公共层 + 其余非私有层） | "给我的这个 agent 配技能" |
| 用户级并集 | user | 该用户名下 agent 的并集（前缀仍是 agent） | 同一批 skill 想一次给多个 agent |
| 平台/团队级 | 组织 | 公共 skill 库 | 官方 / 共享技能集 |

无论哪种粒度：跨租户一律拒绝；**平台公共 skill 默认包含**（它就是 agent 视图的一层）；
per-user（chatter）私有层默认不发布
（`internal/agent/skills.go` 的 `userSkillsDir()` 那一层）。可复用的现成件：
`/api/skills`（列出）、`/api/apikeys`（签发 token）、`internal/auth`（作用域判定）。

### 2.2 本地 Agent 怎么吃到：三级兼容（决定可用性）

不能假设用户本地的 agent 都实现了 SEP-2640 —— ext-skills 的 client matrix 目前只有少数 host
是 `partial`。所以三级都要给，它们是同一份数据的三个视图：

| 本地 host 具备 | 我们提供 | 效果 |
| :--- | :--- | :--- |
| skills 扩展 | `skills/list` + `skills/get` + `skill://` 资源 | 最完整：有 digest、可列目录、可做内容绑定审批 |
| 仅 resources | `resources/list` 列出 `skill://<path>/SKILL.md` 及支持文件，`resources/read` 读内容 | 对不支持扩展的 host，`skill://` 就是普通资源，今天就能用 |
| 仅 tools | 兜底工具 `list_skills` / `read_skill` | 任何 MCP host 都能用，最差也能跑 |

兜底不是可选项：需求写的是"用户的本地 Agent"，而它们的 MCP 能力参差不齐；
少了兜底，这个前提不成立。多出来的成本只是两个只读工具。

**实测校正（2026-09-18，spike 结果见 `spike/mcp-oauth-stub/RESULTS.md`）**：三级里真正吃重的
目前是**工具那一级**。最新版 Claude Code（2.1.276）先探 `server/discover`，但最终仍用 `initialize`
协商到 **2025-11-25**；本机 Codex CLI（0.150.0-alpha.8）只用 `initialize`，协商到 **2025-06-18**。
两者连接后都只调 `tools/list`，既没调 `skills/list` 也没调 `resources/*`。

由此两条结论，与"只做新版本、要求用户升级"的直觉相反：

1. **出口必须同时答 `initialize`（按客户端请求的版本回）和 `server/discover`**；只声明
   `2026-07-28` 会让今天这两个客户端都用不了——今天并不存在一个协商到 2026-07-28 的已发布客户端，
   所以"让用户升级"暂时无版本可升。
2. **第一版先发工具兜底（`list_skills` / `read_skill`），skills 扩展随新客户端到来再加**。
   扩展路径要写清"最低客户端版本"，但它是加分项，不是能用与否的门槛。

## 3. 规范对 Server 的硬性要求（逐条要满足）

| # | 要求 | 规范出处 |
| :-- | :--- | :--- |
| 1 | 必须同时声明 `resources` 能力与 `extensions["io.modelcontextprotocol/skills"]`；`directoryRead: true` 才可实现目录读取 | Capability Negotiation |
| 2 | 声明扩展即必须实现 `skills/list` 与 `skills/get` | Capability Negotiation |
| 3 | `Skill` entry 在两个方法里形状与含义完全一致；`resources` 必须是**完整**数组（含 `SKILL.md` 自身）或字符串 `"dynamic"`，缺字段/其它值均非法 | Skill Entries / Resources |
| 4 | 每个文件给 `digest`（`sha256:{64 位小写 hex}`，覆盖原始字节）与 `size`（字节数） | Skill Entries / Integrity |
| 5 | `uri` 的最后一个 path segment **必须等于** frontmatter 的 `name`；`SKILL.md` 必须在 URI 里显式出现 | Resource Mapping |
| 6 | URI 形如 `skill://<skill-path>/<file-path>`；也允许 `github://…` 之类自有 scheme，但结构约束不变，且 `skills/list` 必须能枚举出来 | Resource Mapping |
| 7 | 每个文件通过 `resources/read` 可读；本扩展**不定义**任何打包/打包下发形式 | Getting a Skill / Resources |
| 8 | 单 skill ≤ 512 个文件、合计 ≤ 16 MiB；超限的 skill 不建议发布（不保证任何合规 host 能加载） | Limits |
| 9 | `resources/directory/read` 返回 `mimeType: inode/directory` 的目录资源；声明了 `directoryRead` 才需要实现 | Reading Directories |
| 10 | 结果须带 `resultType: "complete"`，并给出 `ttlMs` 与 `cacheScope`（缓存提示，不是完整性属性） | Listing / Getting |
| 11 | 未知 skill 的 `skills/get`、不存在的目录读取一律 `-32602`；内部错误 `-32603` | Error Handling |
| 12 | `skills/list` 可以为空或部分（不可枚举的大目录），但 `skills/get` **必须**能回答它服务的每一个 skill | Listing / Getting |
| 13 | 允许嵌套 skill；外层 entry 的 `resources` 必须包含嵌套 skill 的文件 | Nested Skills |
| 14 | 保留：`skills/` 方法前缀、`resources/directory/read` 方法名、`io.modelcontextprotocol/skills` 标签 | Reservations |

> 注意：server 侧不做审批，也没有"trust anchor"—— digest 只是让 host 能验证
> "entry 和内容一致"。所以发布内容本身要当公开材料对待（见 §7 的密钥一条）。

## 4. 现状与差距（代码事实）

- **没有 MCP server 面**：`internal/mcp/*` 只有 `Client` 接口（`Connect` / `ListTools` / `CallTool` / `Close`），
  `protocolVersion` 固定 `2024-11-05`，且丢弃 initialize 响应。仓库里唯一的 server 侧 JSON-RPC 是插件协议
  （`internal/plugin/protocol.go`，`tool.list`/`shutdown` 那一套），与 MCP 无关；没有 `server/discover`、
  没有 `resources/*`。
- **HTTP 面是 OpenAI 兼容 API**：`internal/api/server.go`（`/v1/chat/completions`、`/ws`、`/v1/…`），
  不是 MCP 传输；新增 MCP 端点属于新面（Streamable HTTP + 认证）。
- **skill 来源已成型**：`SkillsLoader.LoadSkills()` 按 `extra → managed(~/.fastagent/skills/) →
  user(agent home/skills) → team → agent → bundled` 合并，按名字去重；`Skill` 带 `BaseDir`、`Content`、
  `Metadata`、`Gated`/`GateReason`；对象存储 hydrate/mirror 在 `internal/skills/objectstore.go`。
- **沙箱里的 `/skills/<name>`**（`internal/agent/tools/exec.go`、`registry.go`）是**挂载视图**，
  与磁盘目录不完全一一对应 —— 发布时必须以磁盘目录为准，不能复用挂载路径规则。
- **名字与目录名可能不一致**：`discoverSkillsEnhanced` 用目录名做 `Name`，而规范要求 URI 末段等于
  frontmatter 的 `name` —— 这是 server 落地的第一个硬约束点。

## 5. 建议实现分片

| # | 分片 | 主要改动 | 验证 |
| :-- | :--- | :--- | :--- |
| S1 | MCP 端点与协商 | 新增 MCP server（Streamable HTTP），实现 initialize/`server/discover`，声明 `resources` + `extensions{skills:{directoryRead:true}}` | MCP Inspector 能连上并看到能力 |
| S2 | entry 构造 | 枚举分层 skill → 逐文件 walk（含嵌套）→ `sha256` + `size` → frontmatter 原样转 JSON → 上限预检 → name↔path 约束校验 | conformance `skills` server 场景 |
| S3 | `skills/list` / `skills/get` | 分页（cursor）、`ttlMs`/`cacheScope`、`resultType`、未知 skill `-32602` | 同上 |
| S4 | 内容读取 | `resources/read`（文本/二进制按 `mimeType` 区分）、`resources/directory/read` 目录枚举与分页，**以及 `resources/list`**（不支持扩展的 host 的兜底入口） | conformance `directory` 场景 |
| S5 | 兜底工具 | `list_skills` / `read_skill`（只读，签名对齐 skills 扩展的语义） | 只支持 tools 的 host 实测 |
| S6 | 作用域与认证 | token → 用户/agent 视图（§2.1）、只读、排除 per-user 私有层、与 ClawHub/GitHub 安装内容的关系 | 跨租户越权测试 + 安全复核 |
| S7 | 出口体验与登记 | dashboard 给出"接入你的本地 Agent"区块（端点 URL + token + 可复制的 host 配置片段，Claude Code / Codex CLI / Inspector 各一份）；登记至 ext-skills `docs/implementations.md` 的 Servers 表 | 手工走一遍 + PR |

## 6. 验收标准

**官方一致性测试**（`modelcontextprotocol/conformance`，server 侧由它当 client 打我们的端点）：

```bash
npx @modelcontextprotocol/conformance server --url http://localhost:<port>/mcp
npx @modelcontextprotocol/conformance server --url http://localhost:<port>/mcp --scenario <name>
```

对应场景（以 `npx @modelcontextprotocol/conformance list` 为准）：
`sep-2640-skills-manifest`、`sep-2640-skills-enumeration`（`skills/list` + `skills/get`）、
`sep-2640-skills-directory`（`resources/directory/read`）。

**自证清单**：

- `skills/list` 的每个 entry，其 `resources` 覆盖该 skill 目录下**所有**文件（含嵌套 skill 的文件），
  且每项 `digest`/`size` 与实际字节一致；
- `skills/get` 对未发布的 URI 返回 `-32602`，对"不在 list 里但确实服务"的 skill 能正常回答；
- 目录读取只声明才实现，未声明时按未知方法处理；
- 超过 512 文件 / 16 MiB 的 skill 被排除并有日志说明，而不是产出非法 entry；
- 空目录 / 无 skill 时返回合法空列表（不是错误）；
- MCP Inspector 能列出并加载一个真实 skill，内容与磁盘一致；
- **端到端**：在至少一个真实本地 Agent 上跑通"接 URL + token → 列出 skill → 加载并照做"
  （Claude Code / Codex CLI 二选一，以实测为准），全程不出现 git 与安装步骤；
- **作用域**：用 A 的 token 看不到 B 的 skill；per-user 私有层不出现。

## 7. 风险与待定

1. **名字与 URI 末段（已定）**：发布身份一律取 frontmatter `name`，末段永远等于它；
   目录名与它不一致的在**入口**治理（安装/水合时校验，不一致就拒绝并给出理由），
   出口不猜、不改末段。细节见设计文档 §5。
2. **per-user skill 的隐私边界**：`~/.fastagent/users/<uid>/skills/` 属于 chatter 私有，
   默认**不**对外发布（token 里也没有 chatter id，取不到该层）。
3. **密钥与 env**：skill frontmatter 里的 `env`/`primaryEnv` 会随 frontmatter 原样发出去。
   发布前要确认只有变量名/说明，没有真实取值。
4. **`Gated` skill（已定）**：照发。gating 判的是我们运行时的环境（bins/env/config），
   不是 skill 内容的质量；`requires` 本来就在 frontmatter 里，原样透传即可。
   我们也**不能**往 entry 里注入 `gated` 标记（frontmatter 必须与 SKILL.md 逐字段一致）。
   额外要注意：依赖 `{baseDir}` 替换的 skill 对外会露出字面量，建议列为诊断项。
5. **二进制与体积**：`resources/read` 要能返回 blob（图片、pdf 等），并遵守 16 MiB 上限；
   大文件需要预检而不是按需截断。
6. **认证模型**：MCP 端点必须带 token（复用现有 API key 体系），否则等于公开 agent 的 skill 目录。
7. **与 host 侧的先后顺序**：server 侧可独立先行；host 侧（消费）见姊妹提案。
8. **加载语义不由我们控制**：skill 什么时候进模型上下文由本地 host 决定 ——
   有的 host 只在用户显式引用时读 resource，我们的 skill 写法要按"可能按需加载"来设计
   （SKILL.md 自包含、支持文件用相对路径）。`ttlMs` 只能提示新鲜度。
9. **更新的到达方式**：云端改了 skill，digest 就变；host 是否立刻重读取决于它的缓存策略。
   对实现了扩展的 host，内容绑定审批会自动失效并重新确认；只支持 resources/tools 的 host
   可能要等到下一次读取。这属于预期行为，文档要写清。

## 8. 参考

- 规范（唯一真源）：`modelcontextprotocol/ext-skills` → `specification/stable/skills.mdx`
- SEP-2640：<https://modelcontextprotocol.io/seps/2640-skills-extension>（Final）
- 概览文档：<https://modelcontextprotocol.io/extensions/skills/overview>
- 一致性测试：<https://github.com/modelcontextprotocol/conformance>（`src/scenarios/server/skills/`）
- 姊妹提案：`docs/issues/mcp-skills-fastagent.md`（host 侧：消费别人 server 的 skill）
- 设计（四层映射 / 形式化 / 四项决策的推导）：`docs/issues/mcp-skills-egress-design.md`
