# FastClaw → FastAgent Rebrand Record

## 概述

将所有用户可见的 `fastclaw` / `FastClaw` / `FASTCLAW` 品牌引用替换为 `fastagent` / `FastAgent` / `FASTAGENT`。遵循最小改动原则：只改用户能看到的，内部实现保持不变。

**总修改文件**: 152 个
**分支**: `fastagent`

---

## 1. 环境变量 (35 个)

`FASTCLAW_*` → `FASTAGENT_*`

```
FASTCLAW_HOME                          → FASTAGENT_HOME
FASTCLAW_PORT                          → FASTAGENT_PORT
FASTCLAW_BIND                          → FASTAGENT_BIND
FASTCLAW_STORAGE_TYPE                  → FASTAGENT_STORAGE_TYPE
FASTCLAW_STORAGE_DSN                   → FASTAGENT_STORAGE_DSN
FASTCLAW_STORAGE_AUTO_MIGRATE          → FASTAGENT_STORAGE_AUTO_MIGRATE
FASTCLAW_SANDBOX_ENABLED               → FASTAGENT_SANDBOX_ENABLED
FASTCLAW_SANDBOX_BACKEND               → FASTAGENT_SANDBOX_BACKEND
FASTCLAW_SANDBOX_IMAGE                 → FASTAGENT_SANDBOX_IMAGE
FASTCLAW_SANDBOX_BOXLITE_URL           → FASTAGENT_SANDBOX_BOXLITE_URL
FASTCLAW_SANDBOX_BOXLITE_CLIENT_ID     → FASTAGENT_SANDBOX_BOXLITE_CLIENT_ID
FASTCLAW_SANDBOX_BOXLITE_PREFIX        → FASTAGENT_SANDBOX_BOXLITE_PREFIX
FASTCLAW_OBJECT_STORE_TYPE             → FASTAGENT_OBJECT_STORE_TYPE
FASTCLAW_OBJECT_STORE_LOCAL_ROOT       → FASTAGENT_OBJECT_STORE_LOCAL_ROOT
FASTCLAW_OBJECT_STORE_REGION           → FASTAGENT_OBJECT_STORE_REGION
FASTCLAW_OBJECT_STORE_BUCKET           → FASTAGENT_OBJECT_STORE_BUCKET
FASTCLAW_OBJECT_STORE_PREFIX           → FASTAGENT_OBJECT_STORE_PREFIX
FASTCLAW_OBJECT_STORE_ACCESSKEY        → FASTAGENT_OBJECT_STORE_ACCESSKEY
FASTCLAW_OBJECT_STORE_SECRETKEY        → FASTAGENT_OBJECT_STORE_SECRETKEY
FASTCLAW_OBJECT_STORE_ACCOUNTID        → FASTAGENT_OBJECT_STORE_ACCOUNTID
FASTCLAW_OBJECT_STORE_ENDPOINT         → FASTAGENT_OBJECT_STORE_ENDPOINT
FASTCLAW_OBJECT_STORE_USESSL           → FASTAGENT_OBJECT_STORE_USESSL
FASTCLAW_OBJECT_STORE_ALIYUN_INTERNAL  → FASTAGENT_OBJECT_STORE_ALIYUN_INTERNAL
FASTCLAW_LOG_LEVEL                     → FASTAGENT_LOG_LEVEL
FASTCLAW_DEPLOY                        → FASTAGENT_DEPLOY
FASTCLAW_ALLOW_HOST_EXEC               → FASTAGENT_ALLOW_HOST_EXEC
FASTCLAW_INSTALL_DIR                   → FASTAGENT_INSTALL_DIR
FASTCLAW_MODE                          → FASTAGENT_MODE
FASTCLAW_AUTH_TOKEN                    → FASTAGENT_AUTH_TOKEN
FASTCLAW_SEARXNG_ENDPOINT              → FASTAGENT_SEARXNG_ENDPOINT
FASTCLAW_PLUGIN_CHAT_SEND_DELAY_MS     → FASTAGENT_PLUGIN_CHAT_SEND_DELAY_MS
FASTCLAW_DUMP_LLM                      → FASTAGENT_DUMP_LLM
FASTCLAW_DUMP_LLM_FILE                 → FASTAGENT_DUMP_LLM_FILE
FASTCLAW_AGENT_ID                      → FASTAGENT_AGENT_ID
FASTCLAW_DAEMON_IDLE_TIMEOUT_SECONDS   → FASTAGENT_DAEMON_IDLE_TIMEOUT_SECONDS
```

**涉及文件**: `internal/config/env.go`, `internal/config/config.go`, 所有 deploy 配置

---

## 2. 文件系统路径

```
~/.fastclaw                            → ~/.fastagent
/data/.fastclaw                        → /data/.fastagent
.fastclaw                              → .fastagent
```

**涉及文件**: `internal/config/config.go`, `Dockerfile`, deploy 配置, 文档

---

## 3. HTTP Headers

```
x-fastclaw-agent-id                    → x-fastagent-agent-id
x-fastclaw-session-key                 → x-fastagent-session-key
x-fastclaw-channel                     → x-fastagent-channel
X-Fastclaw-End-User                    → X-Fastagent-End-User
X-Fastclaw-Channel                     → X-Fastagent-Channel
```

**涉及文件**: `internal/gateway/gateway.go`, `internal/auth/auth.go`, `internal/api/openai.go`, `internal/api/users.go`, `internal/setup/handlers_agents.go`, `cmd/fastclaw/cmd_apikey.go`

---

## 4. Cookie 名称

```
fastclaw_session                       → fastagent_session
fastclaw-affinity                      → fastagent-affinity
```

**涉及文件**: `internal/auth/auth.go`, Helm ingress 配置

---

## 5. 品牌名和命令名

```
FastClaw                               → FastAgent
fastclaw upgrade                       → fastagent upgrade
fastclaw version                       → fastagent version
fastclaw connect dialog                → fastagent connect dialog
```

**涉及文件**: `internal/agent/context.go`, `internal/agent/slash.go`, `internal/daemon/service.go`, `README.md`, `install.sh`, `scripts/release.sh`

---

## 6. 二进制名和构建产物

```
bin/fastclaw                           → bin/fastagent
fastclaw.exe                           → fastagent.exe
dist/fastclaw_darwin_arm64/            → dist/fastagent_darwin_arm64/
dist/fastclaw_linux_amd64/            → dist/fastagent_linux_amd64/
ENTRYPOINT ["fastclaw"]                → ENTRYPOINT ["fastagent"]
```

**涉及文件**: `Makefile`, `Dockerfile`, `.goreleaser.yaml`, `install.sh`

---

## 7. K8s 资源名称

```
namespace: fastclaw                    → namespace: fastagent
name: fastclaw-config                  → name: fastagent-config
name: fastclaw-secrets                 → name: fastagent-secrets
app.kubernetes.io/name: fastclaw       → app.kubernetes.io/name: fastagent
```

**涉及文件**: `deploy/k8s/fastagent.yaml`, `deploy/k8s/namespace.yml`, `deploy/k8s/postgres.yml`

---

## 8. Helm Chart

```
name: fastclaw                         → name: fastagent
description: FastClaw — ...            → description: FastAgent — ...
deploy/helm/fastclaw/                  → deploy/helm/fastagent/
fastclaw.fullname                      → fastagent.fullname
fastclaw.labels                        → fastagent.labels
fastclaw.dsn                           → fastagent.dsn
```

**涉及文件**: `deploy/helm/fastagent/` 全部文件

---

## 9. PostgreSQL

```
POSTGRES_DB: fastclaw                  → POSTGRES_DB: fastagent
POSTGRES_USER: fastclaw                → POSTGRES_USER: fastagent
postgres://fastclaw:...@.../fastclaw   → postgres://fastagent:...@.../fastagent
```

**涉及文件**: `deploy/k8s/postgres.yml`, Helm 配置

---

## 10. User-Agent

```
FastClaw/1.0 (AI Agent Web Fetcher)    → FastAgent/1.0 (AI Agent Web Fetcher)
```

**涉及文件**: `internal/toolproviders/webfetch/direct.go`, `internal/agent/tools/web_fetch.go`

---

## 11. 函数名

```
isFastClawInternalPath                 → isFastAgentInternalPath
```

**涉及文件**: `internal/agent/tools/route.go`

---

## 12. Skills 元数据结构

新增 `fastagent` JSON 字段，保留 `fastclaw` 和 `openclaw` 向后兼容：

```go
type SkillMetadata struct {
    FastAgent *OpenClawMeta `json:"fastagent"` // 新增，优先级最高
    FastClaw  *OpenClawMeta `json:"fastclaw"`  // 保留，向后兼容
    OpenClaw  *OpenClawMeta `json:"openclaw"`  // 保留，向后兼容
}

func (m *SkillMetadata) Meta() *OpenClawMeta {
    if m.FastAgent != nil { return m.FastAgent }  // 优先
    if m.FastClaw != nil  { return m.FastClaw }   // 兼容
    return m.OpenClaw                              // 兼容
}
```

**涉及文件**: `internal/agent/skills.go`

---

## 13. GitHub 仓库信息

```
owner: fastclaw-ai                     → owner: tokenaissance
name: fastclaw                         → name: fastagent
REPO="fastclaw-ai/fastclaw"            → REPO="tokenaissance/fastagent"
```

**涉及文件**: `.goreleaser.yaml`, `cmd/fastclaw/cmd_version.go`, `cmd/fastclaw/cmd_plugin.go`, `install.sh`, `scripts/dev-build.sh`

---

## 14. deploy 本地镜像名

```
image: fastclaw:local                  → image: fastagent:local
```

**涉及文件**: `deploy/multi-pod/docker-compose.yaml`

---

## 15. 代码注释 (50+ 处)

所有代码注释中的 `FastClaw` → `FastAgent`，`fastclaw` → `fastagent`。

涉及文件（部分）:
- `internal/agent/loop.go`, `context.go`, `slash.go`, `bundled_skills.go`
- `internal/agent/tools/file.go`, `route.go`, `registry.go`, `bash_session_windows.go`
- `internal/daemon/service.go`
- `internal/agentcli/agentcli.go`
- `internal/buildinfo/buildinfo.go`
- `internal/store/store.go`
- `internal/agent/bundled_skills/*/SKILL.md`
- `docs/QUERY_OPTIMIZATION.md`

---

## 16. 文档

- `FASTAGENT_README.md`: rebranded 版本的 README（`README.md` 保持原始版本与上游对齐）
- `deploy/multi-pod/README.md`: 部署说明
- `internal/agent/bundled_skills/README.md`: Skills 说明

---

---

## 17. Web UI — 品牌文本 (14 处, 11 文件) ✅

用户可见的 `FastClaw` 品牌文本 → `FastAgent`：

| 文件 | 行 (rebrand 后) | 内容 | 类型 |
|------|-----|------|------|
| `web/src/app/layout.tsx` | 34 | `title: "FastClaw"` → `"FastAgent"` | 页面标题 |
| `web/src/app/page.tsx` | 76 | `alt="FastClaw"` → `"FastAgent"` | Logo alt |
| `web/src/app/page.tsx` | 77 | `<h1>FastClaw</h1>` → `<h1>FastAgent</h1>` | 登录页标题 |
| `web/src/app/signup/page.tsx` | 91 | `"Sign up to start using FastClaw"` → `FastAgent` | 注册页文案 |
| `web/src/app/onboard/page.tsx` | 452 | `"Welcome to FastClaw"` → `FastAgent` | 引导页标题 |
| `web/src/app/overview/page.tsx` | 98 | `"Monitor your FastClaw gateway"` → `FastAgent` | 概览页描述 |
| `web/src/app/settings/about/page.tsx` | 46 | `FastClaw` label → `FastAgent` | 关于页标签 |
| `web/src/app/settings/about/page.tsx` | 9 | `"fastclaw upgrade"` → `"fastagent upgrade"` | CLI 升级命令 |
| `web/src/app/plugins/page.tsx` | 94 | `"Extend FastClaw"` → `FastAgent` | 插件页描述 |
| `web/src/app/channels/page.tsx` | 72 | `"fastclaw.json"` → `"fastagent.json"` | 频道页提示 |
| `web/src/components/login-screen.tsx` | 107 | `"Sign up to start using FastClaw"` → `FastAgent` | 登录页文案 |
| `web/src/components/login-screen.tsx` | 171 | `<h1>FastClaw</h1>` → `<h1>FastAgent</h1>` | 登录页标题 |
| `web/src/components/team-switcher.tsx` | 42 | `alt="FastClaw"` → `"FastAgent"` | Logo alt |
| `web/src/components/team-switcher.tsx` | 121 | `"FastClaw"` header → `"FastAgent"` | 侧栏标题 |

> 注：`settings/about/page.tsx:11` 的 GitHub 发布链接 URL 按计划保持不变（真实 URL）。

## 18. Web UI — 路径引用 (5 处, 5 文件) ✅

用户可见的 `~/.fastclaw/` 路径 → `~/.fastagent/`：

| 文件 | 行 (rebrand 后) | 内容 |
|------|-----|------|
| `web/src/app/skills/page.tsx` | 461 | `~/.fastclaw/skills/` → `~/.fastagent/skills/` |
| `web/src/app/agents/[id]/skills/page.tsx` | 521 | `~/.fastclaw/agents/{agentId}/skills/` → `~/.fastagent/` |
| `web/src/app/agents/[id]/plugins/page.tsx` | 105 | `~/.fastclaw/plugins/` → `~/.fastagent/plugins/` |
| `web/src/app/agents/[id]/context/page.tsx` | 238 | `~/.fastclaw/plugins/fastclaw-plugin-demo` → `~/.fastagent/plugins/fastagent-plugin-demo` |
| `web/src/components/chat-screen.tsx` | 30 | `~/.fastclaw/workspaces/...` → `~/.fastagent/workspaces/...` (注释) |

## 19. Web UI — localStorage Key (6 处, 3 文件) ✅

| 文件 | 行 (rebrand 后) | 内容 |
|------|-----|------|
| `web/src/lib/api.ts` | 273 | `localStorage.setItem("fastclaw_token", ...)` → `"fastagent_token"` |
| `web/src/lib/api.ts` | 275 | `localStorage.removeItem("fastclaw_token")` → `"fastagent_token"` |
| `web/src/lib/api.ts` | 281 | `localStorage.getItem("fastclaw_token")` → `"fastagent_token"` |
| `web/src/components/theme-provider.tsx` | 7 | `STORAGE_KEY = "fastclaw-theme"` → `"fastagent-theme"` |
| `web/src/app/layout.tsx` | 48 | `localStorage.getItem('fastclaw-theme')` → `'fastagent-theme'` (inline script) |

## 20. Web UI — Custom Event (10 处, 5 文件) ✅

`fastclaw:sessions-changed` → `fastagent:sessions-changed`：

| 文件 | 行 (rebrand 后) | 用法 |
|------|-----|------|
| `web/src/components/nav-projects.tsx` | 89 | `new CustomEvent("fastclaw:sessions-changed")` → `"fastagent:"` |
| `web/src/components/chat-screen.tsx` | 954, 1058, 1669 | 3× dispatch |
| `web/src/components/app-sidebar.tsx` | 242 | `addEventListener("fastclaw:sessions-changed")` → `"fastagent:"` |
| `web/src/components/app-sidebar.tsx` | 244 | `removeEventListener("fastclaw:sessions-changed")` → `"fastagent:"` |
| `web/src/components/app-sidebar.tsx` | 255 | `new CustomEvent("fastclaw:sessions-changed")` → `"fastagent:"` |
| `web/src/app/agents/[id]/chats/page.tsx` | 110 | `new CustomEvent("fastclaw:sessions-changed")` → `"fastagent:"` |
| `web/src/components/nav-projects-list.tsx` | 93 | 注释引用 |
| `web/src/components/app-sidebar.tsx` | 207 | 注释引用 |

## 21. Web UI — MIME Type (1 处) ✅

| 文件 | 行 (rebrand 后) | 内容 |
|------|-----|------|
| `web/src/components/nav-projects.tsx` | 20 | `"application/x-fastclaw-chat"` → `"application/x-fastagent-chat"` |

## 22. Web UI — Sandbox 占位符 (1 处变更 + 3 处保持不变) ✅

按计划：只有 BoxLite snapshot placeholder 改 `fastclaw-sandbox` → `fastagent-sandbox`，Docker 默认值 `thinkany/fastclaw-sandbox:latest` 保持不变（已发布镜像名）。

**已变更**:
| 文件 | 行 (rebrand 后) | 内容 |
|------|-----|------|
| `web/src/app/onboard/page.tsx` | 885 | `placeholder="fastclaw-sandbox"` → `"fastagent-sandbox"` |
| `web/src/app/settings/runtime/page.tsx` | 221 | `placeholder="fastclaw-sandbox"` → `"fastagent-sandbox"` |

**保持不变**:
| 文件 | 行 | 内容 |
|------|-----|------|
| `web/src/app/onboard/page.tsx` | 161 | `useState("thinkany/fastclaw-sandbox:latest")` |
| `web/src/app/onboard/page.tsx` | 909 | `placeholder="thinkany/fastclaw-sandbox:latest"` |
| `web/src/app/settings/runtime/page.tsx` | 245 | `placeholder="thinkany/fastclaw-sandbox:latest"` |

## 23. Web UI — Channel 页注释 (6 处, 1 文件) ✅

| 文件 | 行 (rebrand 后) | 内容 |
|------|-----|------|
| `web/src/app/agents/[id]/channels/page.tsx` | 928 | `fastclaw verifies inbound` → `fastagent verifies` |
| `web/src/app/agents/[id]/channels/page.tsx` | 1048 | `fastclaw doesn't surface` → `fastagent doesn't` |
| `web/src/app/agents/[id]/channels/page.tsx` | 1258 | `fastclaw is now opening a WebSocket` → `fastagent is now` |
| `web/src/app/agents/[id]/channels/page.tsx` | 1273 | `this fastclaw instance` → `this fastagent instance` |
| `web/src/app/agents/[id]/channels/page.tsx` | 1297 | `fastclaw opens a WebSocket` → `fastagent opens` |
| `web/src/app/agents/[id]/channels/page.tsx` | 1341 | `fastclaw rejects webhook payloads` → `fastagent rejects` |

## 24. Web UI — 其他注释 (5 处, 4 文件) ✅

| 文件 | 行 (rebrand 后) | 内容 |
|------|-----|------|
| `web/src/app/skills/page.tsx` | 51 | `~/.fastclaw/skills dir` → `~/.fastagent/skills dir` |
| `web/src/app/agents/[id]/skills/page.tsx` | 495 | `~/.fastclaw/agents/<id>/skills` → `~/.fastagent/` |
| `web/src/lib/api.ts` | 331 | `// driven by FASTCLAW_DEPLOY` → `// driven by FASTAGENT_DEPLOY` |
| `web/src/components/team-switcher.tsx` | 23, 84 | `FastClaw logo`, `show "FastClaw"` → `FastAgent` |
| `web/src/components/app-sidebar.tsx` | — | `falling back to "FastClaw"` → `"FastAgent"` |

## 25. LICENSE — 按计划未修改

全部 ~15 处 `FastClaw` 保持原样。按 scope 约定：LICENSE 文件不在本次 web UI 仅 rebrand 范围内。

## 26. 插件清单 — 按计划未修改 (3 文件)

| 文件 | 内容 | 状态 |
|------|------|------|
| `plugins/fastclaw-plugin-demo/plugin.json` | `"id": "fastclaw-plugin-demo"`, `"name": "FastClaw Plugin Demo"` | 保持（外部标识符） |
| `plugins/openclaw-plugin-demo/plugin.json` | description 含 `"FastClaw"` | 保持 |
| `tools/openclaw-plugin-bridge/package.json` | `@fastclaw/` npm name, description | 保持 |

## 27. Deploy 文档注释 — 按计划未修改 (4 文件)

| 文件 | 内容 | 状态 |
|------|------|------|
| `deploy/docker/sandbox/e2b.toml` | 4 处注释提及 `fastclaw-sandbox` | 保持 |
| `deploy/docker/sandbox/build.sh` | 注释 `Default: thinkany/fastclaw-sandbox:latest` | 保持 |
| `deploy/k8s/fastagent.yaml` | 2 处注释 | 保持 |
| `scripts/apply-rename-to-fastagent-final.sh` | 脚本自身注释 | 保持 |

---

## ✅ 保持不变

| 类别 | 内容 | 原因 |
|------|------|------|
| Go module | `github.com/fastclaw-ai/fastclaw` | 内部代码结构，用户不可见 |
| Go imports | ~316 处 | 跟随 module path |
| Go 源码目录 | `./cmd/fastclaw` (8 处 build 命令) | Go 包路径，不影响产物名 |
| Docker 镜像 | `ghcr.io/fastclaw-ai/fastclaw` | 注册表已发布名称 |
| Docker 镜像 | `thinkany/fastclaw-sandbox` | 注册表已发布名称 |
| E2B 模板 | `fastclaw-sandbox` (e2b.toml) | 外部平台注册的模板名 |
| JSON tag | `json:"fastclaw"` | 向后兼容 |
| Go struct field | `FastClaw *OpenClawMeta` | 配合 json tag |
| README.md | 保持原始 FastClaw 版本 | 与上游 repo 对齐 |

---

## 用户影响

现有 rebrand 已造成的影响:
- 需要重新登录（cookie 名变更，旧 `fastclaw_session` 失效）
- 环境变量名变更（运维需更新 `FASTCLAW_*` → `FASTAGENT_*`）
- API Header 名变更（第三方 API 调用者需更新 `X-Fastclaw-*` → `X-Fastagent-*`）
- 数据目录变更（本地用户需 `mv ~/.fastclaw ~/.fastagent`）
- K8s 部署需更新 ConfigMap/Secret 中的环境变量名

Web UI rebrand 的额外影响 (已应用):
- **需重新登录**: `fastclaw_token` localStorage key 已变更为 `fastagent_token` → 旧 token 无法读取
- **主题重置**: `fastclaw-theme` localStorage key 已变更为 `fastagent-theme` → 需重新选择主题
- **Custom event 兼容**: `fastclaw:sessions-changed` → `fastagent:sessions-changed`，所有 dispatch/listener 已同步变更 (10 处, 5 文件)
- **MIME type**: `application/x-fastclaw-chat` → `application/x-fastagent-chat` (拖拽 dataTransfer)

## 不影响

- 数据库数据（DSN 值不变，只是读取它的环境变量名变了）
- 对象存储数据（bucket/路径不变）
- Pod 本地缓存（emptyDir，重启自动重建）
- 前端功能（cookie 由浏览器自动处理，前端不硬编码 cookie 名）

---

## 汇总统计

| 类别 | 文件数 | 发生处 | 状态 |
|------|--------|--------|------|
| 1-16 (已有 rebrand) | ~130 | ~300+ | **已完成** (commit `9265d46`) |
| 17. Web UI 品牌文本 | 11 | 14 | **已完成** (commit 待定) |
| 18. Web UI 路径引用 | 5 | 5 | **已完成** |
| 19. Web UI localStorage | 3 | 6 | **已完成** |
| 20. Web UI CustomEvent | 5 | 10 | **已完成** |
| 21. Web UI MIME Type | 1 | 1 | **已完成** |
| 22. Web UI Sandbox 占位符 | 2 | 2 变更 + 3 保持 | **部分变更** |
| 23. Web UI Channel 注释 | 1 | 6 | **已完成** |
| 24. Web UI 其他注释 | 4 | 5 | **已完成** |
| 25. LICENSE | 1 | ~15 | 按计划未修改 |
| 26. 插件清单 | 3 | 6 | 按计划未修改 |
| 27. Deploy 文档注释 | 4 | ~8 | 按计划未修改 |
| **Web UI 已 rebrand 合计** | **22** | **~49** | **已完成** |
| **按计划保持不变** | — | ~320+ | — |
