# 决策版：云端 skill 库的 MCP 出口（OAuth + skills）

> **已被取代（2026-09-22）**：本文冻结的 per-agent 出口（`resource = https://<host>/mcp/agents/<id>`）**从未上线**，
> 现已删除。当前出口只有一个账号级端点 `/mcp`（`resource = https://<host>/mcp`，工具 `list_agents` → `list_skills(agent)`
> → `read_skill(agent, …)`）；当前口径见 cloud 仓库 `docs/agent-readiness.md` §2。本文保留为当时的决策记录。

**状态**：决策记录（实现未开始） · **日期**：2026-09-18
**地位**：本文档是当时的决策来源；**出口面的部分自 2026-09-22 起以 cloud `docs/agent-readiness.md` §2 为准**（per-agent 面从未上线、已删），其余决策仍有效

**输入文档**（保留为推导过程，不再单独作为决策依据）：
`mcp-skills-fastclaw.md`（提案与分片）、`mcp-skills-egress-design.md`（四层与形式化）、
`mcp-oauth-2-design.md`（OAuth 设计）、`skill-name-migration.md`（改名与旧数据）、
`mcp-server-capability-map.md`（对外能力清单）、`codex-mcp-oauth-notes.md`（Codex 侧调研）、
`spike/mcp-oauth-stub/RESULTS.md`（本机实测）

---

## 1. 目标（一句话）

**用户在云端装 skill，我们用一个 MCP 端点把它们发出去；用户本地的 Agent 只接一个 URL + 一次浏览器登录就能用，
不需要 git、不需要安装。**

## 2. 决策表

| # | 决策 | 理由与证据 |
| :-- | :--- | :--- |
| **D1** | **目录粒度 = agent；凭据 = 用户已有 apikey** | URL 里指定 agent（`/mcp/agents/<id>`），token 用用户自己的凭证。agent 是代码里唯一已存在、已有优先级定义的所有权范围；用户级视图今天只能靠并集拼出来。内部跳复用 cloud 现有每用户凭证（`getFastagentCredentials` / `resolveUserCredentials`） |
| **D2** | **平台公共 skill 默认包含** | 不额外过滤 `managed` 层——它本来就是 agent 视图的一层；被 `disabled` / `entries` 关掉的仍过滤 |
| **D3** | **`Gated` 的 skill 照发** | gating 是我们运行时的环境属性，不是内容属性。规范不允许往 frontmatter 注入标记；`requires` 原样透传。依赖 `{baseDir}` 替换的 skill 会露出字面量，列为诊断项（2026-09-21 补充：触发条件是 **manifest** 带 token——只有 `SKILL.md` 会被替换；随包文件里的 token 没有任何解析它，agent 读到的同样是字面量，所以那不是读者差异） |
| **D4** | **发布身份 = frontmatter `name`，不一致在入口治理** | 安装/上传/水合时校验 `name == 目录名`，不一致按 frontmatter 改名（`FinalizeInstallDir`，已在 worktree 实现）。规范要求 URI 末段等于 frontmatter name；Agent Skills 要求 name 等于父目录名 |
| **D5** | **`resource` 冻结 = `https://<cloud-host>/mcp/agents/<id>`** | canonical：https、小写主机、无尾斜杠、无默认端口；RS 精确比对、不归一化。一旦签发就绑死，改形态要双 audience 过渡 |
| **D6** | **RS 在边缘（cloud），不在 fastagent** | 边缘 = RS，cloud = AS，两者**同源**；fastagent 是被断言身份的内部后端。理由：`resource`/audience 必须等于客户端实际打的 URL；RFC 9728 元数据必须挂在 RS 的 URL 上；401 挑战必须来自客户端打到的那层 |
| **D7** | **内部跳 = 服务凭证 + 身份断言，用户 token 不下沉** | 复用现有 `X-Fastagent-End-User` 模式（只在 apikey 请求上生效）。用户 token 不进内网日志；fastagent 不变成第二个 RS；两层各自判定、不一致取更严的一侧 |
| **D8** | **第一版 AS 实现 DCR；CIMD 只声明支持** | 实测：Codex 每次登录都走 DCR（即使我们 advertise 了 CIMD），且一次登录内可能重复注册多次 |
| **D9** | **同时答 `initialize` 与 `server/discover`，不要求客户端到 2026-07-28** | 实测：最新 Claude Code 协商到 2025-11-25，Codex 到 2025-06-18；今天没有客户端协商到 2026-07-28 | 
| **D10** | **第一版对外面 = 工具兜底（`list_skills` / `read_skill`），skills 扩展后置** | 实测：两个客户端连接后都只调 `tools/list`；工具路径已在真实 Codex 会话里端到端跑通 |
| **D11** | **错误语义写死** | 无 token/过期/audience 不符/scope 不足 → `401` + `WWW-Authenticate`（缺 scope 带 `scope=`）；无权访问该 agent → `403`；上游故障 → `502/503`，**绝不 401**。AS 侧失败一律用 OAuth error redirect，`error_description` 保持编码安全（客户端不解码 `+`） |
| **D12** | **`oauth_clients` / `oauth_authorization_codes` / `oauth_tokens` / `oauth_consents` 落在边缘库** | 只新增表；`users` / `apikeys` / `web_sessions` 一行不动。回滚 = 下线边缘端点；apikey 兜底始终可用 |
| **D13** | **skill 名字的三处持久化按序迁移** | 顺序：目录 → 对象存储 key → configs_kv 行。reconcile 命令已实现（默认 dry run）。顺序不可换：先改名会让 hydrate 的 prune 删掉新目录；先同步会让两个名字并存 |

## 3. 实测证据（2026-09-18，本机）

| 客户端 | 版本 | 握手 | 协商版本 | 连接后调用 |
| :--- | :--- | :--- | :--- | :--- |
| Claude Code | 2.1.276（最新发布） | 先探 `server/discover`，再 `initialize` | 2025-11-25 | `tools/list` |
| Codex CLI | 0.150.0-alpha.8 | 仅 `initialize` | 2025-06-18 | `tools/list`，以及真实会话中的 `tools/call list_skills`（通过） |

其他实测结论（细节见 `spike/mcp-oauth-stub/RESULTS.md`）：Codex 走 DCR；redirect 是随机端口的
loopback，因此**不需要** any-port 放宽；授权被拒时显示 `access_denied` 且 `error_description` 不解码；
Codex 对 MCP 工具调用有审批门（默认策略下会被拒绝，需要在文档里告诉用户）。

## 4. 第一版实现清单（按依赖顺序）

1. **内部出口（fastagent 侧）**：`/mcp/agents/<id>` 的内部实现，只信任云端服务凭证 + 身份断言；
   先出工具兜底 `list_skills` / `read_skill`，再出 `skills/list`、`skills/get`、`resources/read`。
2. **边缘 RS（cloud）**：`/mcp/agents/<id>` 对外端点 + 两种 `/.well-known/oauth-protected-resource` +
   401 挑战 + audience/scope 校验 + 转发到内部出口（复用现有每用户凭证）。
3. **边缘 AS（cloud）**：`/.well-known/oauth-authorization-server`（含 `code_challenge_methods_supported`）、
   `/oauth/register`（DCR）、`/oauth/authorize`（复用现有登录 + 同意页，显示 redirect 主机名）、
   `/oauth/token`（code + refresh）、`/oauth/revoke`；错误一律 OAuth error redirect。
4. **数据**：四张新表（D12），token 哈希存储、refresh 旋转 + 复用检测、登出/禁用级联撤销。
5. **诊断与文档**：`skill reconcile` 的 dry-run 报告进 doctor；"最低客户端版本 + 需要的审批设置"写进用户文档。

## 5. 明确不做（Musk 门）

- 不做用户级 skill 存储层（没有这个所有权范围；等真实需求 ≥3 次再加，且届时应是投影）。
- 不做 `skill://index.json`、不做打包下发（规范已删除/不定义）。
- 不做服务端审批存储（审批是 host 的职责）。
- 不做第二套层枚举或第二套权限语义（复用 `internal/skills` 的布局与 `CanAccessAgent`）。
- 不为旧协议版本做降级适配层（D9：答客户端请求的版本即可，不实现兼容矩阵）。

## 6. 待定与风险

- **Q3 的 Claude 侧**：`mcp list` 不触发授权，失败文案只会在会话里出现；需要在真实会话里补测一次。
- **Codex 的 `Auth required` 警告**：会话结束后出现，原因未解释，复现后再定性。
- **scope 升级**：第一版只有 `skills:read`；未来加 tools 面时要按 rmcp 的 `403 insufficient_scope` 语义实现 step-up。
- **CIMD 何时启用**：等出现只走 CIMD 的客户端再打开（现在声明支持即可）。
