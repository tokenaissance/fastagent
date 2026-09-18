# Issue 提案：FastAgent 以 Host 身份支持 Skills over MCP

**状态**：提案（未实现，待排期） · **目标仓库**：`tokenaissance/fastagent` · **日期**：2026-09-18

**规范来源**：[modelcontextprotocol/ext-skills](https://github.com/modelcontextprotocol/ext-skills)
—— `specification/stable/skills.mdx`（扩展名 `io.modelcontextprotocol/skills`，基线协议 revision `2026-07-28`；
SEP-2640 已转为 Final）。实现清单见同仓库 `docs/implementations.md` 的 Hosts 一栏。

---

## 1. 一句话

让 agent 连上一个 MCP server 之后，能把它发布的 **skill** 当作自己的 skill 使用
——发现 → 审批 → 按需加载 → 逐字节校验 —— 同时不破坏现有文件系统 skill 的信任边界。

## 2. 为什么值得做

今天的 FastAgent 里，skill 只来自文件系统分层（`extra → managed → user → team → agent → bundled`），
而 MCP server 只能给**工具**。于是"这个 server 该怎么用"要么塞进 tool description（常驻上下文、结构丢失），
要么靠用户去 ClawHub / GitHub 单独装一份 skill —— server 和它的使用说明天然会漂移。

ext-skills 把 skill 变成 server 的另一类资源（`skill://<path>/SKILL.md`），host 通过
`skills/list` / `skills/get` 发现、通过 `resources/read` 取内容，并在读取时校验。具体收益：

1. 接上 `quandora`、`hf-mcp-server` 这类 server 就同时拿到"该怎么用它"的说明，不需要再单独分发一次。
2. 现有机制不用换：`<skill_catalog>` 摘要、`load_skill`、always-load 都保持不变，MCP skill 只是多一个来源。
3. 生态位置：ext-skills 的 implementations 表有 Hosts 一栏（已有 fast-agent、MCP Inspector、Goose 等登记），
   FastAgent 可作为 `planned → partial` 补进去，链接本 issue。

代价同样要说清：这是把**服务器可控的指令文本**送进模型上下文，规范因此对 host 提了一组硬性要求（§3），
其中来源隔离、内容绑定审批、缓存隔离是安全边界，不能省 —— 所以不建议"先随便读进来，安全以后再说"。

### 2.1 FastAgent 同样可以当 server（不是 fastclaw 专属）

FastAgent 与 FastClaw 是**同一棵代码树上的两个发行版**：`Makefile` 用 `./cmd/fastclaw` 构建
`bin/fastagent`，模块路径是 `github.com/fastclaw-ai/fastclaw`。因此"把 skill 目录发布出去"这件事
不存在产品限制 —— 在 `internal/` 里实现的 server 侧能力，两个发行版同时获得。

对 FastAgent 反而更自然的一点：**skill 的归属就是 agent**。每个 agent 有自己的 skill 层
（agent 目录、per-user 目录，dashboard 有 Skills 管理页），把这个目录按规范发布，
等于让编辑器类 host、别的 agent runtime 直接消费"这个 agent 的 skill 集"，
而不是让每个消费方再走一次 ClawHub / GitHub 安装。

多出来的约束只在多租户这一侧（详见姊妹提案 §7）：发布哪个 agent 的目录、用谁的 API key、
per-user 私有 skill 默认不发布、`Gated` skill 与 frontmatter 里 env 声明如何处理。
这些是设计选择题，不是能力缺失。

结论：两份 issue 的差别是**归属与验收口径**，不是"哪一半只能给谁做"。
server 侧实现只写一遍，落在 `internal/`，两边共用。

## 3. 规范对 Host 的硬性要求（逐条要满足）

| # | 要求 | 规范出处 | 落在我们哪一层 |
| :-- | :--- | :--- | :--- |
| 1 | 只在 server 声明 `extensions["io.modelcontextprotocol/skills"]` 后才调用 `skills/*`；`directoryRead` 单独门控 | Capability Negotiation | MCP client |
| 2 | `skills/list`（分页 + `ttlMs`/`cacheScope`）与 `skills/get`（按 `SKILL.md` URI）；**空列表 ≠ 没有 skill** | Listing / Getting a Skill | MCP client |
| 3 | 内容一律走 `resources/read`，读时校验 `digest` + `size` | Integrity and Verification | 校验层 |
| 4 | `SKILL.md` 的 frontmatter 与 entry 逐字段比对，不一致即失败 | Frontmatter Verification | 校验层 |
| 5 | acting window 内只能读 held entry 列出的文件；读到未列出文件 = 校验失败 | Acting Window | 校验层 |
| 6 | 惰性取用：连接时、列表时、审批时都不得预取文件 | Lazy Retrieval | 加载路径 |
| 7 | skill 身份 =（host 给 server 的**本地标签**, `uri`）；名字不是标识；任何落盘路径必须编码 server 身份 | Skill URIs / Names | 注册表 + 缓存 |
| 8 | 不得静默覆盖或冒充本地同名 skill；跨源同名要按 origin 消歧并提示用户 | Names / Security | catalog 呈现 |
| 9 | 不隐式执行：MCP skill 不得触发 `exec` 等宿主代码执行，除非用户对该 skill 明确授权；`allowed-tools` 一律忽略 | Security Considerations | 工具门控 |
| 10 | 审批绑定审批时刻看到的整个 `resources` 集合；集合变化即撤销并重新确认；`"dynamic"` 不可绑定 | Security Considerations | 审批存储 |
| 11 | 磁盘缓存必须只宿主可写（或每次重算 digest），且必须位于**所有 skill 发现路径之外**；断连/重启后仍按 MCP 来源对待 | Cache integrity | 缓存目录 |
| 12 | 单 skill 上限 512 文件 / 16 MiB，超限要告知原因而不是静默失败 | Limits | 注册表 |
| 13 | 嵌套 skill 需单独同意 | Nested Skills | 审批 |
| 14 | 进入模型上下文时必须标注来源 server | Security Considerations | prompt 组装 |

## 4. 现状与差距（代码事实）

- `internal/mcp/client.go`：`Client` 接口只有 `Connect` / `ListTools` / `CallTool` / `Close`；
  `initializeParams.Capabilities` 是空 `struct{}`，`protocolVersion` 固定 `2024-11-05`，
  且 `Connect()`（`internal/mcp/http.go`、`internal/mcp/stdio.go`）**丢弃 initialize 响应**。
  → 没有能力协商，也没有任何 `resources/*`、`skills/*`。
- `internal/mcp/manager.go`：只有 `toolMap` 与 `prefixToolName`（`mcp_<server>_<tool>`），没有 resource/skill 概念。
- `internal/agent/skills.go`：`SkillsLoader` 只扫文件系统分层，`Skill` 的主键是 `Name`；
  `BuildSkillsSummary` 生成 `<skill_catalog>`，`load_skill` 按名字读 `BaseDir/SKILL.md`。
- `internal/agent/tools/load_skill.go`、`registry.go`、`route.go`：`load_skill` 与 `/skills/<name>`
  直接映射到文件系统 skill 目录 —— 与"名字不是标识"（§3.7）和"缓存必须在发现路径之外"（§3.11）直接冲突。
- `internal/agent/loop.go`：MCP 只注册工具与一个 OAuth 管理工具，**没有 per-skill 审批面**。

结论：缺的不是一个方法，而是一整条"server → 模型上下文"的可信路径（发现、身份、审批、校验、缓存、注入）。

## 5. 建议实现分片

每片独立可合、可单独验证，建议按序落地。

| # | 分片 | 主要改动 | 验证 |
| :-- | :--- | :--- | :--- |
| S1 | 传输与协商 | `internal/mcp/*`：解析 initialize/`server/discover` 的能力与 `extensions`，记录 per-server 声明；新增 `resources/read`、`skills/list`、`skills/get`（分页 + 缓存字段），可选 `resources/directory/read` | 假 server 回放 + 现有 MCP 测试回归 |
| S2 | 发现与注册 | `internal/agent/skills.go`：MCP skill 作为独立 layer（`mcp`），键为（serverLabel, uri）；`Skill` 增 Origin / URI / Entry；catalog 显示来源、跨源同名消歧、不覆盖本地 | 单元测试：同名不同源 |
| S3 | 加载与校验 | `load_skill` 的 MCP 分支：取 entry → `resources/read` → digest+size 校验 → frontmatter 逐字段比对 → 持有 entry 打开 acting window | conformance client skills 场景 |
| S4 | 审批与缓存 | 内容绑定的持久审批（键含 serverLabel + uri + resources 集合），集合变化自动失效；缓存置于 `<home>/cache/mcp-skills/<serverLabel>/<sha256>/…`，宿主独占写 | 单元 + 端到端 |
| S5 | 安全与体验 | 上下文标注来源；忽略 `allowed-tools`；acting on MCP skill 时代码执行工具走显式授权；UI 呈现来源、内容预览、审批与撤销；per-server 开关与上限配置 | 手工 + e2e |
| S6 | 文档与上游 | `docs/` 设计文档；把 FastAgent 登记进 ext-skills `docs/implementations.md` 的 Hosts 表（`planned`，链接本 issue） | PR |
| S7 | server 侧发布（可选，可与 S1–S5 并行） | MCP 端点 + `skills/list`/`skills/get` + `resources/read`（含 `directoryRead`），把 agent 的 skill 目录按规范发布；实现与 fastclaw 提案共用，差异只在多租户作用域。对外还能开什么见 `docs/issues/mcp-server-capability-map.md`（tools / resources / prompts 五组清单） | conformance server skills 场景 + MCP Inspector |

## 6. 验收标准

**官方一致性测试**（`modelcontextprotocol/conformance`，客户端 skills 场景是"harness 当 server、被测客户端当 host"）：

```bash
npx @modelcontextprotocol/conformance client --command "<fastagent ...>" --scenario <name>
```

- `sep-2640-client-no-prefetch`：连接并 `skills/list` 之后不得读取任何技能文件；
- `sep-2640-client-verify-digest` / `sep-2640-client-verify-size` /
  `sep-2640-client-verify-frontmatter`：校验失败时不得使用内容。

以 `npx @modelcontextprotocol/conformance list` 输出为准。

**自证清单**：

- 空 `skills/list` 不阻断"仅凭 URI 加载"（`skills/get` + `resources/read` 仍可用）；
- digest / size 不符、读未列出文件、frontmatter 不一致 → 内容不进模型上下文，且用户看得到原因；
- 两个 server 发同名 skill：互不覆盖、互不串读（A 的 skill 不能引发对 B 的 `resources/read`）；
- 审批在 `resources` 集合变化后失效并重新确认；
- 缓存目录不在任何 skill 发现路径内，且模型/沙箱不可写；
- 超限（512 文件 / 16 MiB）给出原因；
- 现有文件系统 skill 行为不变（`load_skill_test`、`skill_manifest_gate_test`、registry 相关测试全绿）。
- 若同一批发 S7：server 侧再要过 `sep-2640-skills-manifest` / `-enumeration` / `-directory`，
  且多租户作用域正确（只发布该 agent 可见的层，per-user 私有 skill 不外泄）。

## 7. 风险与待定

1. **协议版本跳跃**：`protocolVersion` 从 `2024-11-05` 提到 `2026-07-28` 可能与现有 server 的握手假设冲突。
   建议版本协商 + 失败回退，且回退路径必须有测试（现有 server 的工具能力不能因此退化）。
2. **审批粒度**：规范允许 per-server，但内容绑定（per-skill 集合）是 MUST。建议先做 per-skill，
   UI 再补"本 server 全部技能"的批量入口。
3. **`resources: "dynamic"`**：首版建议直接拒绝并给出原因（规范允许 host 拒绝）。
4. **索引缓存**：`ttlMs` 只是新鲜度提示，不能当完整性依据。
5. **与 ClawHub / GitHub 安装路径的关系**：MCP skill 不写成文件系统 skill（否则会被同名覆盖规则与发现路径污染），
   只走内存 + 隔离缓存。
6. **沙箱 `/skills` 挂载**：首版建议不把 MCP skill 挂进沙箱，避免绕过审批与校验。

## 8. 参考

- 规范（唯一真源）：`modelcontextprotocol/ext-skills` → `specification/stable/skills.mdx`
- SEP-2640：<https://modelcontextprotocol.io/seps/2640-skills-extension>（Final）
- 概览文档：<https://modelcontextprotocol.io/extensions/skills/overview>
- 一致性测试：<https://github.com/modelcontextprotocol/conformance>（`src/scenarios/{client,server}/skills/`）
- 姊妹提案：`docs/issues/mcp-skills-fastclaw.md`（server 侧细节：发布自己的 skill，
  含多租户作用域、密钥与上限的取舍）
