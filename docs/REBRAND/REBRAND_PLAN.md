# FastClaw → FastAgent Rebrand Record

## 概述

将所有用户可见的 `fastclaw` / `FastClaw` / `FASTCLAW` 品牌引用替换为 `fastagent` / `FastAgent` / `FASTAGENT`。遵循最小改动原则：只改用户能看到的，内部实现保持不变。

**总修改文件**: 130 个
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
| 前端代码 | `web/src/` | 管理员前端，不改 |
| 管理员工具 | `tools/openclaw-plugin-bridge/` | 管理员前端代码 |
| 插件示例 | `plugins/fastclaw-plugin-demo/` | 管理员前端代码 |
| 插件示例 | `plugins/openclaw-plugin-demo/` | 管理员前端代码 |
| README.md | 保持原始 FastClaw 版本 | 与上游 repo 对齐，rebranded 版本见 `FASTAGENT_README.md` |

---

## 用户影响

- 需要重新登录（cookie 名变更，旧 `fastclaw_session` 失效）
- 环境变量名变更（运维需更新 `FASTCLAW_*` → `FASTAGENT_*`）
- API Header 名变更（第三方 API 调用者需更新 `X-Fastclaw-*` → `X-Fastagent-*`）
- 数据目录变更（本地用户需 `mv ~/.fastclaw ~/.fastagent`）
- K8s 部署需更新 ConfigMap/Secret 中的环境变量名

## 不影响

- 数据库数据（DSN 值不变，只是读取它的环境变量名变了）
- 对象存储数据（bucket/路径不变）
- Pod 本地缓存（emptyDir，重启自动重建）
- 前端功能（cookie 由浏览器自动处理，前端不硬编码 cookie 名）
