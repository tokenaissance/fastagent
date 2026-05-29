# FastClaw → FastAgent Review（重新分类）

## 规则
- ❌ WebUI 前端源码（`web/src/`）：**不修改**
- ✅ Go 后端代码（`internal/`, `cmd/` 等）：**必须修改**
- ✅ 环境变量、配置、部署文件：**必须修改**

---

## 📋 需要 Review 的项目

---

## 1️⃣ 环境变量（必须修改）

### 1.1 所有 FASTCLAW_* 环境变量
**位置**: `internal/config/env.go` 及所有配置文件

```bash
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

**影响**: 所有部署环境需要更新环境变量配置
**决定**: ✅ 必须修改（这是你的核心目标）

---

## 2️⃣ 文件系统路径（Go 代码中）

### 2.1 默认数据目录
**位置**: `internal/config/config.go`, `internal/agent/skills.go`, `internal/agent/bundled_skills.go` 等

```go
// 当前
return filepath.Join(home, ".fastclaw"), nil

// 修改为
return filepath.Join(home, ".fastagent"), nil
```

**路径**:
```
~/.fastclaw           → ~/.fastagent
/data/.fastclaw       → /data/.fastagent
```

**影响**: 需要迁移现有数据目录
**决定**: ❓ 是否修改？

---

## 3️⃣ API/HTTP 层

### 3.1 自定义 HTTP Headers
**位置**: `internal/api/server.go`, `internal/api/openai.go`

```go
// CORS headers
w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-fastclaw-agent-id, x-fastclaw-session-key")

// Request headers
agentID := r.Header.Get("x-fastclaw-agent-id")
sessionKey := r.Header.Get("x-fastclaw-session-key")
channel := r.Header.Get("x-fastclaw-channel")
```

**可见性**: API 开发者/集成方可见
**影响**: 使用 API 的开发者需要更新 HTTP header 名称
**决定**: ❓ 是否修改？

---

### 3.2 Cookie 名称
**位置**: `internal/auth/auth.go`

```go
const SessionCookieName = "fastclaw_session"
```

**可见性**: 浏览器开发者工具可见
**影响**: 用户需要重新登录
**决定**: ❓ 是否修改？

---

## 4️⃣ Go 代码中的错误消息和提示

### 4.1 错误消息中的品牌名
**位置**: `internal/channels/feishu.go`

```go
errors.New("feishu webhook is encrypted but no encryptKey configured (set 加密策略 → Encrypt Key in fastclaw connect dialog, or clear it in feishu console)")
```

**可见性**: 用户在配置错误时会在前端看到
**影响**: 错误提示中出现品牌名
**决定**: ❓ 是否修改？

---

### 4.2 错误提示中的命令示例
**位置**: `internal/agent/tools/exec.go`

```go
err = fmt.Errorf("%w\n[hint: this looks like a sandbox-environment miss (binary or path not present in the container). If the command needs the user's actual host machine — e.g. `fastclaw upgrade`, `~/Downloads`, host CLI tools — retry with the `host_exec` tool instead.]", err)
```

**可见性**: 用户在执行命令错误时会看到
**影响**: 错误提示中出现 `fastclaw upgrade` 命令
**决定**: ❓ 是否修改？

---

### 4.3 系统提示中的品牌名
**位置**: `internal/agent/context.go`

```go
fastclawLine = fmt.Sprintf(`FastClaw: %s (commit %s, built %s). Self-hosted install — the chatter is the operator. If they ask about upgrading, tell them: run %sfastclaw upgrade%s in a terminal (and %sfastclaw version%s to verify). Don't try to run those yourself unless the chatter explicitly asks you to and you have host shell access (no sandbox).`, ...)
```

**可见性**: 这是发送给 LLM 的系统提示，用户在对话中可能看到
**影响**: Agent 可能在回复中提到 "FastClaw"
**决定**: ❓ 是否修改？

---

### 4.4 Bot 用户名注释示例
**位置**: `internal/channels/base.go`

```go
// BotUsername returns the bot's username for this channel (e.g. "mike_fastclaw_bot").
```

**可见性**: 仅代码注释，用户不可见
**影响**: 无
**决定**: ❓ 是否修改？（建议修改以保持一致性）

---

## 5️⃣ 部署配置文件

### 5.1 Docker Compose 环境变量
**位置**: `deploy/docker/docker-compose.yml`, `deploy/multi-pod/docker-compose.yaml`

所有 `FASTCLAW_*` 环境变量
**决定**: ✅ 必须修改（跟随环境变量）

---

### 5.2 Kubernetes 配置
**位置**: `deploy/k8s/fastclaw.yaml`, `deploy/helm/fastclaw/templates/*.yaml`

所有 `FASTCLAW_*` 环境变量和 volumeMounts
**决定**: ✅ 必须修改（跟随环境变量）

---

### 5.3 Ingress Session Cookie
**位置**: `deploy/helm/fastclaw/templates/ingress.yaml`

```yaml
nginx.ingress.kubernetes.io/session-cookie-name: "fastclaw-affinity"
```

**可见性**: 浏览器开发者工具可见
**影响**: K8s 负载均衡会话保持
**决定**: ❓ 是否修改？

---

### 5.4 Helm Chart 名称
**位置**: `deploy/helm/fastclaw/Chart.yaml`

```yaml
name: fastclaw
description: FastClaw — Cloud AI Agent Runtime
```

**可见性**: 运维人员可见，用户不可见
**影响**: Helm release 需要重新安装
**决定**: ❓ 是否修改？

---

### 5.5 Kubernetes 标签
**位置**: `deploy/helm/fastclaw/templates/_helpers.tpl`

```yaml
app.kubernetes.io/name: fastclaw
```

**可见性**: 运维人员可见，用户不可见
**影响**: K8s 资源选择器
**决定**: ❓ 是否修改？

---

### 5.6 PostgreSQL 数据库名称
**位置**: `deploy/helm/fastclaw/templates/postgres.yaml`, `deploy/helm/fastclaw/templates/_helpers.tpl`

```yaml
POSTGRES_DB: fastclaw
POSTGRES_USER: fastclaw
postgres://fastclaw:password@host:5432/fastclaw
```

**可见性**: 运维人员可见，用户不可见
**影响**: 需要数据库迁移
**决定**: ❓ 是否修改？

---

### 5.7 Docker 镜像名称
**位置**: 多处

```
ghcr.io/fastclaw-ai/fastclaw
thinkany/fastclaw-sandbox:latest
```

**可见性**: 运维人员可见，用户不可见
**影响**: 需要更新镜像仓库和 CI/CD
**决定**: ❓ 是否修改？

---

## 6️⃣ Go Module 路径

### 6.1 Go Module 和 Import 路径
**位置**: `go.mod` 和所有 Go 文件（307 处）

```go
module github.com/fastclaw-ai/fastclaw

import "github.com/fastclaw-ai/fastclaw/internal/..."
```

**可见性**: 开发者可见，用户不可见
**影响**: 需要全局替换 import 路径
**决定**: ❓ 是否修改？

---

## 7️⃣ 文档

### 7.1 README.md
多处提到 fastclaw
**决定**: ❓ 是否修改？

### 7.2 部署文档
**位置**: `deploy/multi-pod/README.md` 等
**决定**: ❓ 是否修改？

---

## 📊 总结

**明确要修改的**:
- ✅ 所有 `FASTCLAW_*` 环境变量（35+ 个）
- ✅ 部署配置文件中的环境变量引用

**需要你决定的**:
- ❓ 文件系统路径 `~/.fastclaw` → `~/.fastagent`
- ❓ HTTP Headers `x-fastclaw-*` → `x-fastagent-*`
- ❓ Cookie 名称 `fastclaw_session` → `fastagent_session`
- ❓ Go 代码中的错误消息和提示
- ❓ Ingress cookie 名称
- ❓ Helm Chart 名称
- ❓ K8s 标签
- ❓ PostgreSQL 数据库名
- ❓ Docker 镜像名
- ❓ Go module 路径
- ❓ 文档

---

## 下一步

请告诉我你对每一项的决定，我们逐个 review。
