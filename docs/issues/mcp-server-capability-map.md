# 能力清单：FastAgent / FastClaw 当 MCP server 时对外开什么

**状态**：能力盘点（配套提案，未实现） · **日期**：2026-09-18
**关联**：`docs/issues/mcp-skills-fastclaw.md`（server 侧提案）、`docs/issues/mcp-skills-fastagent.md`（host 侧提案）

---

## 0. 先定身份：一个端点 = 一个 agent

外部 agent 连上来的 MCP 端点，身份应该是**某一个 agent**（叠加 api key 的作用域），不是整台实例。
现有的 HTTP 面已经是这个模型 —— `GET /v1/agents` 只返回调用者有权访问的 agent，
请求用 `x-fastagent-agent-id` / `x-fastagent-session-key` 指定目标（`internal/api/server.go`、
`internal/api/openai.go`）。MCP 端点沿用同一套：跨 agent 访问一律拒绝。

## 1. 五组能力 → MCP 面

### 组 A · 只读资产（resources）—— 默认开

| 内部能力 | 现有入口 | MCP 面 |
| :--- | :--- | :--- |
| 系统文件 SOUL.md / IDENTITY.md / AGENTS.md / BOOTSTRAP.md / TOOLS.md / HEARTBEAT.md | `/api/agents/{id}/system-files/{name}` | `resources/*`（只读） |
| 知识库 KNOWLEDGE.md + knowledge/ 文件（可带 `[K1]` 引用） | `/api/agents/{id}/knowledge-files` | `resources/*` |
| workspace 文件与变更文件 | `/api/agents/{id}/files`、`/changed-files`、`files.zip` | `resources/*` |
| agent 的 skill 目录 | `/api/agents/{id}/skills` | **skills 扩展**（`skills/list` + `skills/get` + `skill://`） |
| 会话与历史 | `/api/chat/sessions`、`/api/chat/history` | `resources/*`（需分页 + 归属校验） |
| 用量 | `/v1/usage`、`/api/agents/{id}/usage` | `resources/*`（只读计量） |

注：`MEMORY.md` / `USER.md` / `KNOWLEDGE.md` 是 chatter 级状态
（`internal/setup/handlers_agents.go` 的 system-file 白名单把它们分层），
对外默认只暴露 agent 级、不暴露某个 chatter 的私有层。

### 组 B · 检索与对话（tools）—— 低副作用，建议第一批开

| 能力 | 来源 | 说明 |
| :--- | :--- | :--- |
| `memory_search` | `internal/agent/tools/memory_search.go` | 会话历史检索（关键词 + 时间权重） |
| `knowledge_search` | `internal/agent/tools/knowledge_search.go` | 知识库检索 |
| `web_search` / `web_fetch` | `internal/agent/tools/web_search.go` / `web_fetch.go` | provider 链 + 自动回退 |
| `ask_agent` | 尚未有：把 `POST /v1/chat/completions` 包成工具 | 外部 agent 委托"问这个 agent"的总入口 |

`ask_agent` 是这一组里最有价值的一个：外部 agent 不需要理解我们的会话模型，
只要它能把"问题"交给一个已经配好人格、skill、知识的 agent。

### 组 C · 执行与产物（tools）—— 高副作用，必须显式授权

| 能力 | 来源 | 风险 |
| :--- | :--- | :--- |
| `exec` / `host_exec` / `bash_output` / `kill_shell` | `internal/agent/tools/exec.go`、`bash_tools.go` | 等于把沙箱借出去 |
| `read_file` / `write_file` / `edit_file` / `list_dir` | `internal/agent/tools/file.go` | 直接读写 agent workspace |
| `apply_patch` | `internal/agent/tools/apply_patch.go` | 批量改文件 |
| `image_gen` / `tts` | `image_gen.go` / `tts.go` | 消耗第三方额度 |
| 项目运行时 `start_app_preview` / `app_preview_logs` | `internal/agent/runtime_tools.go` | 起真实进程 |

这一组对外开等于"把沙箱和额度借出去"，必须有 per-tool 开关、审批与配额（见 §4）。

### 组 D · 编排（tools）—— 中风险

| 能力 | 来源 | 说明 |
| :--- | :--- | :--- |
| `spawn_subagent` / delegate | `internal/agent/tools/subagent.go`、`delegate.go` | 再起一个 agent 干活 |
| `create_cron_job` / `list_cron_jobs` / `delete_cron_job` | `internal/agent/tools/cron.go` | 定时任务 |
| `update_goal`（`/goal`、`/plan`） | `internal/agent/tools/goal.go`、`slash_goal.go` | 长任务的计划与推进 |
| `message` | `internal/agent/tools/message.go` | 往 Telegram / Slack / 飞书 / 微信 / LINE 等通道发消息 |
| skill 安装/更新 | `internal/agent/tools/skill_install.go`、`internal/skills/` | ClawHub / GitHub / object store |

### 组 E · 管理面（默认**不**对外）

`/api/users`、`/api/apikeys`、`/api/providers`、`/api/config`、`/api/plugins`、agent CRUD、
quota 设置、MCP OAuth 管理（`internal/agent/mcp_config_tool.go`）—— 这些是 operator 面，
不是 agent 面。外部 agent 拿到它们等于拿到实例控制权。

## 2. prompts：斜杠命令天然就是 MCP prompts

`internal/agent/slash.go` 里已有的 `/goal`、`/plan`、`/status`、`/usage`、`/compact`、`/reset`、
`/model`、`/personality`、`/insights` 等等，本质是"参数化提示词"，可以直接映射成 MCP 的
`prompts/*`：用户在外部 host 里选一个 prompt，host 填参数，走我们这边已经存在的执行路径。
这是投入最小、最能体现产品差异的一块对外面。

## 3. 建议分期

第一个真实场景已经把 v0 定死了：**用户在云端装一堆 skill → 我们只读发布 → 本地 Agent 接 URL + token 直接用**
（不需要 git、不需要安装）。也就是说 v0 = 组 A 的 skill 部分 + skills 扩展，不碰 tools 与执行。

| 版本 | 开什么 | 外部 agent 能做什么 |
| :--- | :--- | :--- |
| **v0** | **skill 分发**：组 A 的 skill 目录 + skills 扩展（+ 仅 resources / 仅 tools 的兜底） | 本地 Agent 直接用云端 skill；不能改 |
| **v0.5** | 组 A 其余（知识库、系统文件、会话）+ 组 B 的 `memory_search` / `knowledge_search` / `ask_agent` | 能看、能问 |
| **v1** | 组 B 全开 + 组 D 的 `spawn_subagent` / cron | 能委派与定时，受配额约束 |
| **v2** | 组 C（写与执行）+ `message` | 能动手；每类工具单独开关 + 审批 + 审计 |
| — | 组 E 永不对外（除非另做 operator 端点） | — |

## 4. 协议与命名注意

- **工具命名**：我们是 server，外部看到的是裸名字。不要和 host 侧
  `mcp_<server>_<tool>` 前缀规则混用（`internal/mcp/manager.go` 的 `prefixToolName` 是消费侧规则）。
- **resources URI**：建议 `fastagent://agent/<agent-id>/<kind>/<path>`；
  skill 走规范要求的 `skill://<skill-path>/SKILL.md`。
- **分页与缓存**：list 方法带 `cursor`，结果带 `ttlMs` / `cacheScope`（对齐 base 协议与 ext-skills）。
- **错误**：未知资源 `-32602`；跨 agent 拒绝也要给可读原因，不要静默空结果。
- **能力声明**：`tools` / `resources` / `prompts` 分别声明，外部 host 才能按需打开。

## 5. 安全与治理（对外 = 把内部能力借出去）

- **作用域**：端点绑定 agent + api key 作用域；跨 agent 一律拒绝。
- **递归执行**：外部 agent 调 `exec` / `spawn_subagent` 会消耗我们的沙箱与额度，
  必须接上 `internal/usage` 的计量与 `/v1/quota`、`internal/api/ratelimit.go` 的限流。
- **隐私层**：per-user（chatter）的文件、skill、记忆默认不对外。
- **密钥**：provider key、skill frontmatter 里的 env 取值一律不出现（只出变量名与说明）。
- **审计**：谁在何时调了什么 —— `internal/bus` + store 已有落库能力。
- **与 ext-skills 一致的原则**：默认只读；写与执行必须显式批准，不做隐式授权。

## 6. 与 ext-skills 的关系

skills 扩展是这五组能力里的**一块**，而且是唯一有规范形态的一块：
外部 host 通过 `skills/list` / `skills/get` / `skill://` 直接消费我们的 skill 目录。
tools / resources / prompts 是通用 MCP 面，两者可以分开交付：
先发 skills（无争议、可被 Inspector 与一致性测试验证），再分批开 tools 与 prompts。
