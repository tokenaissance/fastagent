# 最小改动方案 - 详细说明和对比

## 📋 修改项详细说明

---

### 5️⃣ 文档中的用户可见引用

#### 什么是"用户可见引用"？

用户会阅读的文档中提到的内容，主要在：

**README.md** - 用户安装和配置指南
```markdown
# 旧的
| `FASTCLAW_HOME` | `~/.fastclaw` | Where the SQLite DB and skill folders live. |
| `FASTCLAW_PORT` | `18953` | Gateway HTTP port. |

Your data is stored in `~/.fastclaw/fastclaw.db`

# 改为
| `FASTAGENT_HOME` | `~/.fastagent` | Where the SQLite DB and skill folders live. |
| `FASTAGENT_PORT` | `18953` | Gateway HTTP port. |

Your data is stored in `~/.fastagent/fastagent.db`
```

**deploy/multi-pod/README.md** - 部署文档
```markdown
# 旧的
Both pods use `FASTCLAW_AUTH_TOKEN=dev-admin-token`.
Set `FASTCLAW_SANDBOX_BACKEND=docker` or `e2b`

# 改为
Both pods use `FASTAGENT_AUTH_TOKEN=dev-admin-token`.
Set `FASTAGENT_SANDBOX_BACKEND=docker` or `e2b`
```

**internal/agent/bundled_skills/README.md** - Skills 说明
```markdown
# 旧的
FASTCLAW_HOME/skills/ or per-agent skills/

# 改为
FASTAGENT_HOME/skills/ or per-agent skills/
```

#### 包括什么？
- ✅ 用户会阅读的 README
- ✅ 部署指南中的环境变量示例
- ✅ 配置说明中的路径示例
- ✅ 命令示例（如 `fastclaw upgrade`）

#### 不包括什么？
- ❌ 代码注释（用户看不到源码）
- ❌ 开发者文档中的技术细节（如 Go module 路径）
- ❌ Git commit 历史

---

### 6️⃣ 环境变量 - 我建议改哪些

#### 环境变量使用场景分析

**场景 1**: 用户在文档中看到
```markdown
# README.md
Set FASTCLAW_PORT=8080 to change the port
```
- 🔴 **用户可见** - 文档中

**场景 2**: 技术用户查看进程
```bash
ps aux | grep fastclaw
# 输出: /usr/bin/fastclaw (env: FASTCLAW_HOME=/data/.fastclaw)
```
- 🟡 **技术用户可见** - 进程列表中

**场景 3**: 代码内部读取
```go
// internal/config/env.go
if v := os.Getenv("FASTCLAW_PORT"); v != "" {
    cfg.Gateway.Port = p
}
```
- 🟢 **内部实现** - 用户看不到

#### 我的建议：改所有环境变量

**原因**:
1. ✅ **文档中会提到** - README.md 中有环境变量表格
2. ✅ **技术用户可能看到** - 进程列表、配置文件
3. ✅ **保持一致性** - 如果改了路径 `~/.fastagent`，环境变量 `FASTAGENT_HOME` 更合理
4. ✅ **错误消息可能暴露** - 虽然当前代码没有，但未来可能有

**需要改的环境变量列表**（从代码中提取）:

```bash
# Gateway 配置
FASTCLAW_PORT                          → FASTAGENT_PORT
FASTCLAW_BIND                          → FASTAGENT_BIND

# Storage 配置
FASTCLAW_HOME                          → FASTAGENT_HOME
FASTCLAW_STORAGE_TYPE                  → FASTAGENT_STORAGE_TYPE
FASTCLAW_STORAGE_DSN                   → FASTAGENT_STORAGE_DSN
FASTCLAW_STORAGE_AUTO_MIGRATE          → FASTAGENT_STORAGE_AUTO_MIGRATE

# Sandbox 配置
FASTCLAW_SANDBOX_ENABLED               → FASTAGENT_SANDBOX_ENABLED
FASTCLAW_SANDBOX_BACKEND               → FASTAGENT_SANDBOX_BACKEND
FASTCLAW_SANDBOX_IMAGE                 → FASTAGENT_SANDBOX_IMAGE
FASTCLAW_SANDBOX_BOXLITE_URL           → FASTAGENT_SANDBOX_BOXLITE_URL
FASTCLAW_SANDBOX_BOXLITE_CLIENT_ID     → FASTAGENT_SANDBOX_BOXLITE_CLIENT_ID
FASTCLAW_SANDBOX_BOXLITE_PREFIX        → FASTAGENT_SANDBOX_BOXLITE_PREFIX

# Object Store 配置
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

# Logging 配置
FASTCLAW_LOG_LEVEL                     → FASTAGENT_LOG_LEVEL

# 其他配置
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

**总计**: 35 个环境变量

#### 你需要确认

**问题**: 是否同意修改所有 35 个环境变量？

**如果同意** → ✅ 我会修改所有环境变量
**如果不同意** → 请告诉我哪些不改，我会调整脚本

---

## 📊 与原计划的对比

### ✅ 保持修改的项目（6项）

| 项目 | 原计划 | 最小改动方案 | 说明 |
|------|--------|-------------|------|
| 1. 文件系统路径 | ✅ 修改 | ✅ 修改 | 用户可见 |
| 2. HTTP Headers | ✅ 修改 | ✅ 修改 | API 开发者可见 |
| 3. Cookie 名称 | ✅ 修改 | ✅ 修改 | 浏览器中可见 |
| 4. 错误消息 | ✅ 修改 | ✅ 修改 | 前端错误提示 |
| 5. 系统提示 | ✅ 修改 | ✅ 修改 | Agent 对话内容 |
| 6. 环境变量 | ✅ 修改 | ✅ 修改 | 文档中可见 |

---

### ❌ 删除的修改项目（5项）

| 项目 | 原计划 | 最小改动方案 | 删除原因 |
|------|--------|-------------|---------|
| 1. Helm Chart 名称 | ✅ 修改 | ❌ **不改** | 只有运维看到，用户不可见 |
| 2. K8s 标签 | ✅ 修改 | ❌ **不改** | 只有运维看到，用户不可见 |
| 3. PostgreSQL 数据库名 | ✅ 修改 | ❌ **不改** | 内部实现，用户永远看不到 |
| 4. Helm Chart 目录重命名 | ✅ 修改 | ❌ **不改** | 跟随 Chart 名称 |
| 5. Helm 模板函数名 | ✅ 修改 | ❌ **不改** | 跟随 Chart 名称 |

---

### ✅ 保持不修改的项目（2项）

| 项目 | 原计划 | 最小改动方案 | 说明 |
|------|--------|-------------|------|
| 1. Go Module 路径 | ❌ 不改 | ❌ 不改 | 内部代码结构 |
| 2. Docker 镜像名 | ❌ 不改 | ❌ 不改 | 运维使用（假设 WebUI 不显示） |

---

## 📈 改动量对比

### 原计划（方案 B）

```
修改项目: 12 类
- 环境变量（35个）
- 文件系统路径
- HTTP Headers
- Cookie 名称
- 错误消息
- 系统提示
- Helm Chart 名称
- K8s 标签
- PostgreSQL 数据库名
- Helm 目录重命名
- Helm 模板函数
- 文档

影响文件: 60-80 个
需要数据迁移: ✅ 是（PostgreSQL）
需要重新安装 Helm: ✅ 是
```

### 最小改动方案（方案 A）

```
修改项目: 6 类
- 环境变量（35个）
- 文件系统路径
- HTTP Headers
- Cookie 名称
- 错误消息和系统提示
- 文档

影响文件: 40-50 个
需要数据迁移: ❌ 否
需要重新安装 Helm: ❌ 否
```

**减少的改动**:
- ❌ 不改 Helm Chart（5项相关修改）
- ❌ 不改 PostgreSQL 数据库名
- ❌ 减少约 20-30 个文件的修改

---

## 🎯 最终确认

### 需要你确认的问题

**问题 1**: 环境变量（35个）是否全部修改？
- ✅ 同意 → 修改所有 35 个
- ❌ 不同意 → 请告诉我哪些不改

**问题 2**: 是否同意删除以下修改？
- ❌ Helm Chart 名称
- ❌ K8s 标签
- ❌ PostgreSQL 数据库名

**问题 3**: Docker 镜像名
- WebUI 中是否显示 Docker 镜像名？
- 如果显示 → 需要改
- 如果不显示 → 不需要改

---

## 📝 修改后的脚本调整

如果你确认最小改动方案，我需要调整脚本：

**删除的部分**:
```bash
# 不再修改 Helm Chart 名称
# 不再修改 K8s 标签
# 不再修改 PostgreSQL 数据库名
# 不再重命名 Helm Chart 目录
# 不再修改 Helm 模板函数
```

**保留的部分**:
```bash
# 环境变量（35个）
# 文件系统路径
# HTTP Headers
# Cookie 名称
# 错误消息和系统提示
# 文档中的用户可见引用
```

---

请告诉我你的决定：
1. 环境变量（35个）是否全部修改？
2. 是否同意删除 Helm Chart、K8s 标签、PostgreSQL 的修改？
3. WebUI 是否显示 Docker 镜像名？
