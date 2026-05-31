# 环境变量完整清单

## 📋 所有将被修改的环境变量

根据代码分析（`internal/config/env.go`），以下是所有将被修改的环境变量：

---

## 1️⃣ Gateway 配置（2个）

| 旧名称 | 新名称 | 用途 | 默认值 |
|--------|--------|------|--------|
| `FASTCLAW_PORT` | `FASTAGENT_PORT` | Gateway HTTP 端口 | 18953 |
| `FASTCLAW_BIND` | `FASTAGENT_BIND` | 绑定地址 | loopback (127.0.0.1) |

**代码位置**: `internal/config/env.go:59-66`

---

## 2️⃣ Storage 配置（3个）

| 旧名称 | 新名称 | 用途 | 默认值 |
|--------|--------|------|--------|
| `FASTCLAW_STORAGE_TYPE` | `FASTAGENT_STORAGE_TYPE` | 存储类型 | sqlite |
| `FASTCLAW_STORAGE_DSN` | `FASTAGENT_STORAGE_DSN` | 数据库连接字符串 | 空（使用 SQLite） |
| `FASTCLAW_STORAGE_AUTO_MIGRATE` | `FASTAGENT_STORAGE_AUTO_MIGRATE` | 自动迁移数据库 | true |

**代码位置**: `internal/config/env.go:68-76`

**注意**: `FASTCLAW_HOME` 在代码中被引用但不在 `LoadEnv()` 中，它在 `config.go` 中使用。

---

## 3️⃣ Sandbox 配置（6个）

| 旧名称 | 新名称 | 用途 | 默认值 |
|--------|--------|------|--------|
| `FASTCLAW_SANDBOX_ENABLED` | `FASTAGENT_SANDBOX_ENABLED` | 启用 Sandbox | false |
| `FASTCLAW_SANDBOX_BACKEND` | `FASTAGENT_SANDBOX_BACKEND` | Sandbox 后端 | - |
| `FASTCLAW_SANDBOX_IMAGE` | `FASTAGENT_SANDBOX_IMAGE` | Sandbox 镜像 | - |
| `FASTCLAW_SANDBOX_BOXLITE_URL` | `FASTAGENT_SANDBOX_BOXLITE_URL` | Boxlite API URL | - |
| `FASTCLAW_SANDBOX_BOXLITE_CLIENT_ID` | `FASTAGENT_SANDBOX_BOXLITE_CLIENT_ID` | Boxlite Client ID | default |
| `FASTCLAW_SANDBOX_BOXLITE_PREFIX` | `FASTAGENT_SANDBOX_BOXLITE_PREFIX` | Boxlite 工作区前缀 | default |

**代码位置**: `internal/config/env.go:78-104`

**注意**: `E2B_API_KEY` 和 `BOXLITE_API_KEY` 不是 `FASTCLAW_` 前缀，不修改。

---

## 4️⃣ Object Store 配置（11个）

| 旧名称 | 新名称 | 用途 |
|--------|--------|------|
| `FASTCLAW_OBJECT_STORE_TYPE` | `FASTAGENT_OBJECT_STORE_TYPE` | 对象存储类型 |
| `FASTCLAW_OBJECT_STORE_LOCAL_ROOT` | `FASTAGENT_OBJECT_STORE_LOCAL_ROOT` | 本地存储根目录 |
| `FASTCLAW_OBJECT_STORE_REGION` | `FASTAGENT_OBJECT_STORE_REGION` | S3 区域 |
| `FASTCLAW_OBJECT_STORE_BUCKET` | `FASTAGENT_OBJECT_STORE_BUCKET` | S3 Bucket 名称 |
| `FASTCLAW_OBJECT_STORE_PREFIX` | `FASTAGENT_OBJECT_STORE_PREFIX` | S3 对象前缀 |
| `FASTCLAW_OBJECT_STORE_ACCESSKEY` | `FASTAGENT_OBJECT_STORE_ACCESSKEY` | S3 Access Key |
| `FASTCLAW_OBJECT_STORE_SECRETKEY` | `FASTAGENT_OBJECT_STORE_SECRETKEY` | S3 Secret Key |
| `FASTCLAW_OBJECT_STORE_ACCOUNTID` | `FASTAGENT_OBJECT_STORE_ACCOUNTID` | 账户 ID（Cloudflare R2） |
| `FASTCLAW_OBJECT_STORE_ENDPOINT` | `FASTAGENT_OBJECT_STORE_ENDPOINT` | S3 Endpoint |
| `FASTCLAW_OBJECT_STORE_USESSL` | `FASTAGENT_OBJECT_STORE_USESSL` | 使用 SSL |
| `FASTCLAW_OBJECT_STORE_ALIYUN_INTERNAL` | `FASTAGENT_OBJECT_STORE_ALIYUN_INTERNAL` | 阿里云内网 |

**代码位置**: `internal/config/env.go:112-148`

---

## 5️⃣ Logging 配置（1个）

| 旧名称 | 新名称 | 用途 | 默认值 |
|--------|--------|------|--------|
| `FASTCLAW_LOG_LEVEL` | `FASTAGENT_LOG_LEVEL` | 日志级别 | info |

**代码位置**: `internal/config/env.go:106-108`

---

## 6️⃣ 其他配置（需要从其他文件查找）

从部署配置和文档中发现的其他环境变量：

| 旧名称 | 新名称 | 用途 |
|--------|--------|------|
| `FASTCLAW_HOME` | `FASTAGENT_HOME` | 数据目录 |
| `FASTCLAW_DEPLOY` | `FASTAGENT_DEPLOY` | 部署模式 |
| `FASTCLAW_ALLOW_HOST_EXEC` | `FASTAGENT_ALLOW_HOST_EXEC` | 允许主机执行 |
| `FASTCLAW_INSTALL_DIR` | `FASTAGENT_INSTALL_DIR` | 安装目录 |
| `FASTCLAW_MODE` | `FASTAGENT_MODE` | 运行模式 |
| `FASTCLAW_AUTH_TOKEN` | `FASTAGENT_AUTH_TOKEN` | 认证 Token |
| `FASTCLAW_SEARXNG_ENDPOINT` | `FASTAGENT_SEARXNG_ENDPOINT` | SearXNG 端点 |
| `FASTCLAW_PLUGIN_CHAT_SEND_DELAY_MS` | `FASTAGENT_PLUGIN_CHAT_SEND_DELAY_MS` | 插件延迟 |
| `FASTCLAW_DUMP_LLM` | `FASTAGENT_DUMP_LLM` | 转储 LLM 请求 |
| `FASTCLAW_DUMP_LLM_FILE` | `FASTAGENT_DUMP_LLM_FILE` | LLM 转储文件 |
| `FASTCLAW_AGENT_ID` | `FASTAGENT_AGENT_ID` | Agent ID |
| `FASTCLAW_DAEMON_IDLE_TIMEOUT_SECONDS` | `FASTAGENT_DAEMON_IDLE_TIMEOUT_SECONDS` | Daemon 空闲超时 |

---

## 📊 统计总结

### 按类别统计

| 类别 | 数量 |
|------|------|
| Gateway | 2 |
| Storage | 3 |
| Sandbox | 6 |
| Object Store | 11 |
| Logging | 1 |
| 其他 | 12 |
| **总计** | **35** |

### 按来源统计

| 来源 | 数量 |
|------|------|
| `internal/config/env.go` (核心配置) | 23 |
| 其他代码和配置文件 | 12 |
| **总计** | **35** |

---

## ✅ 确认修改

根据你的决定：

1. ✅ **环境变量**: 修改所有 35 个
2. ✅ **Helm Chart**: 修改（包括名称、标签、模板函数）
3. ✅ **K8s 标签**: 修改
4. ✅ **PostgreSQL**: 修改数据库名
5. ❌ **Docker 镜像名**: 不修改（保持仓库原样）

---

## 🔄 与原计划的变化

### 相比原计划（RENAME_PLAN.md）

**保持不变**:
- ✅ 所有 35 个环境变量
- ✅ Helm Chart 名称和相关配置
- ✅ K8s 标签
- ✅ PostgreSQL 数据库名

**唯一变化**:
- ❌ Docker 镜像名：原计划是"不修改"，现在确认"不修改" ✅

**结论**: 你的决定与原计划基本一致，只是明确了 Docker 镜像名不修改。

---

## 📝 下一步

我需要确认：原计划（`RENAME_PLAN.md` 和 `apply-rename-to-fastagent-final.sh`）已经包含了所有这些修改，是否需要调整？

**答案**: ✅ **不需要调整**

原计划已经包含：
- ✅ 所有 35 个环境变量
- ✅ Helm Chart 修改
- ✅ K8s 标签修改
- ✅ PostgreSQL 修改
- ✅ Docker 镜像名排除（已经在脚本中排除）

**可以直接使用原脚本 `apply-rename-to-fastagent-final.sh`！**
