# FastClaw → FastAgent 替换计划

## 📋 Review 结果汇总

### ✅ 需要修改的项目

| 序号 | 项目 | 修改内容 | 影响 |
|------|------|----------|------|
| 1.1 | 环境变量 | `FASTCLAW_*` → `FASTAGENT_*` (35+ 个) | 所有部署环境需要更新配置 |
| 2.1 | 文件系统路径 | `~/.fastclaw` → `~/.fastagent` | 需要迁移数据目录 |
| 3.1 | HTTP Headers | `x-fastclaw-*` → `x-fastagent-*` | API 用户需要更新代码 |
| 3.2 | Cookie 名称 | `fastclaw_session` → `fastagent_session` | 用户需要重新登录 |
| 4.1 | 错误消息 | Go 代码中的 `fastclaw` → `fastagent` | 用户看到的错误提示 |
| 4.2 | 命令示例 | `fastclaw upgrade` → `fastagent upgrade` | 错误提示中的命令 |
| 4.3 | 系统提示 | `FastClaw` → `FastAgent` | Agent 对话中的品牌名 |
| 4.4 | 注释示例 | `mike_fastclaw_bot` → `mike_fastagent_bot` | 代码注释 |
| 5.1-5.2 | 部署配置 | 所有 `FASTCLAW_*` 环境变量 | 跟随环境变量 |
| 5.3 | Ingress Cookie | `fastclaw-affinity` → `fastagent-affinity` | 浏览器 cookie |
| 5.4 | Helm Chart | `name: fastclaw` → `name: fastagent` | 需要重新安装 |
| 5.5 | K8s 标签 | `app.kubernetes.io/name: fastclaw` → `fastagent` | K8s 资源标签 |
| 5.6 | PostgreSQL | `POSTGRES_DB: fastclaw` → `fastagent` | 数据库名称 |
| 7.1-7.2 | 文档 | 所有文档中的 `fastclaw` → `fastagent` | 与代码保持一致 |

### ❌ 不修改的项目

| 序号 | 项目 | 原因 |
|------|------|------|
| - | WebUI 前端源码 (`web/src/`) | 按照规则不修改前端 |
| 5.7 | Docker 镜像名称 | 继续使用现有镜像 |
| 6.1 | Go Module 路径 | 用户看不到，避免复杂修改 |

---

## 🎯 修改范围

### Go 后端代码
- ✅ `internal/` 目录下所有 Go 文件
- ✅ `cmd/` 目录下所有 Go 文件
- ✅ 环境变量读取代码
- ✅ 错误消息和提示文本
- ✅ 系统提示文本
- ✅ 文件路径常量
- ✅ HTTP Header 名称
- ✅ Cookie 名称

### 部署配置
- ✅ `deploy/docker/docker-compose.yml`
- ✅ `deploy/multi-pod/docker-compose.yaml`
- ✅ `deploy/k8s/fastclaw.yaml`
- ✅ `deploy/helm/fastclaw/` 所有文件
  - Chart.yaml
  - values.yaml
  - templates/*.yaml
  - templates/_helpers.tpl

### 文档
- ✅ `README.md`
- ✅ `deploy/multi-pod/README.md`
- ✅ 其他 `.md` 文档

### 脚本
- ✅ `install.sh`
- ✅ `scripts/*.sh`
- ✅ `deploy/docker/sandbox/build.sh`

### 前端（不修改）
- ❌ `web/src/` 目录下所有文件

---

## 📝 详细替换列表

### 1. 环境变量名称（35+ 个）

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

### 2. 文件系统路径

```bash
~/.fastclaw                            → ~/.fastagent
/data/.fastclaw                        → /data/.fastagent
.fastclaw                              → .fastagent
```

### 3. HTTP Headers

```bash
x-fastclaw-agent-id                    → x-fastagent-agent-id
x-fastclaw-session-key                 → x-fastagent-session-key
x-fastclaw-channel                     → x-fastagent-channel
```

### 4. Cookie 名称

```bash
fastclaw_session                       → fastagent_session
fastclaw-affinity                      → fastagent-affinity
```

### 5. 命令和品牌名

```bash
fastclaw upgrade                       → fastagent upgrade
fastclaw version                       → fastagent version
FastClaw                               → FastAgent
fastclaw connect dialog                → fastagent connect dialog
```

### 6. Helm Chart

```bash
# Chart 名称
name: fastclaw                         → name: fastagent
description: FastClaw — ...            → description: FastAgent — ...

# 目录
deploy/helm/fastclaw/                  → deploy/helm/fastagent/

# 模板函数
fastclaw.fullname                      → fastagent.fullname
fastclaw.labels                        → fastagent.labels
fastclaw.dsn                           → fastagent.dsn

# K8s 标签
app.kubernetes.io/name: fastclaw       → app.kubernetes.io/name: fastagent
```

### 7. PostgreSQL

```bash
POSTGRES_DB: fastclaw                  → POSTGRES_DB: fastagent
POSTGRES_USER: fastclaw                → POSTGRES_USER: fastagent
postgres://fastclaw:...@.../fastclaw   → postgres://fastagent:...@.../fastagent
```

### 8. 注释和示例

```bash
mike_fastclaw_bot                      → mike_fastagent_bot
```

---

## 🚫 排除规则

以下内容**不修改**：

1. **前端源码** (`web/src/` 目录)
2. **Docker 镜像名称** (`ghcr.io/fastclaw-ai/fastclaw`, `thinkany/fastclaw-sandbox`)
3. **Go Module 路径** (`github.com/fastclaw-ai/fastclaw`)
4. **Git 历史和 .git 目录**
5. **node_modules, dist, vendor 等依赖目录**

---

## ⚠️ 特别注意

### Helm Chart 重命名
由于 Helm Chart 名称改变，现有部署需要：
1. 备份数据
2. 卸载旧的 Helm release: `helm uninstall fastclaw -n fastclaw`
3. 安装新的 Helm release: `helm install fastagent ./deploy/helm/fastagent -n fastclaw`

### 数据目录迁移
K8s 环境中：
- Pod 本地存储使用 `emptyDir`，每次重启自动清空，无需迁移
- 持久化数据在 PostgreSQL 和对象存储中，无需迁移

本地环境中：
```bash
mv ~/.fastclaw ~/.fastagent
```

### PostgreSQL 数据库
你的 development 环境已经使用 `fastagent` 作为数据库名，代码修改后会匹配。

---

## 📊 影响评估

### 用户影响
- ✅ 需要重新登录（cookie 名称变更）
- ✅ 环境变量名称变更（运维需要更新配置）
- ✅ API Header 名称变更（API 用户需要更新代码）

### 运维影响
- ✅ 所有部署环境需要更新环境变量配置
- ✅ K8s 环境需要重新安装 Helm release
- ✅ 需要更新监控和日志配置中的环境变量引用

### 开发影响
- ✅ 代码中大量替换
- ❌ Go Module 路径不变，无需更新 import
- ❌ Docker 镜像名不变，无需更新 CI/CD

---

---

## 🛡️ 脚本安全机制

### 排除规则实现

脚本使用多层保护机制确保不修改不应该改的内容：

#### 1. 目录级排除（grep --exclude-dir）
```bash
--exclude-dir=".git"          # Git 历史
--exclude-dir="node_modules"  # Node 依赖
--exclude-dir="dist"          # 构建产物
--exclude-dir="vendor"        # Go 依赖
--exclude-dir="web"           # 前端源码（关键！）
```

#### 2. 文件类型过滤（grep --include）
```bash
--include="*.go"              # Go 源码
--include="*.sh"              # Shell 脚本
--include="*.yaml"            # YAML 配置
--include="*.yml"             # YAML 配置
--include="*.md"              # Markdown 文档
--include="Dockerfile"        # Docker 配置
--include="Makefile"          # Make 配置
```

#### 3. 内容级排除（sed 排除模式）

在文档替换阶段，使用 sed 的排除模式保护特定内容：

```bash
# 不替换包含 Go module 路径的行
sed -e '/github\.com\/fastclaw-ai\/fastclaw/!s/fastclaw/fastagent/g'

# 不替换包含 Docker 镜像名的行
sed -e '/ghcr\.io\/fastclaw-ai\/fastclaw/!s/fastclaw/fastagent/g'
sed -e '/thinkany\/fastclaw-sandbox/!s/fastclaw/fastagent/g'
```

**工作原理**：
- `/pattern/!` 表示"不匹配 pattern 的行"
- 只有不包含这些模式的行才会执行替换

---

## ✅ 验证方法

### 执行前验证

```bash
# 1. 确认在 git 分支中
git branch
# 应该显示当前分支名

# 2. 确认工作区干净
git status
# 应该显示 "nothing to commit, working tree clean"

# 3. 预览将要修改的文件
grep -rl "FASTCLAW_" --exclude-dir=web --exclude-dir=node_modules . | wc -l
# 显示将要修改的文件数量
```

### 执行后验证

#### ✅ 验证已修改的内容

```bash
# 1. 检查环境变量已修改
grep -r 'FASTAGENT_HOME' internal/config/env.go
# 应该输出: if v := os.Getenv("FASTAGENT_HOME"); v != "" {

grep -r 'FASTCLAW_' internal/config/env.go
# 应该没有输出（所有 FASTCLAW_* 都已改为 FASTAGENT_*）

# 2. 检查文件系统路径已修改
grep -r '\.fastagent' internal/config/config.go
# 应该输出: return filepath.Join(home, ".fastagent"), nil

# 3. 检查 HTTP Headers 已修改
grep -r 'x-fastagent-agent-id' internal/api/
# 应该找到修改后的 header 名称

# 4. 检查 Cookie 名称已修改
grep -r 'fastagent_session' internal/auth/auth.go
# 应该输出: const SessionCookieName = "fastagent_session"

# 5. 检查 Helm Chart 已重命名
ls -la deploy/helm/
# 应该看到 fastagent/ 目录，没有 fastclaw/ 目录

cat deploy/helm/fastagent/Chart.yaml
# 应该显示: name: fastagent

# 6. 检查 PostgreSQL 数据库名已修改
grep -r 'POSTGRES_DB: fastagent' deploy/helm/fastagent/
# 应该找到修改后的数据库名
```

#### ❌ 验证未修改的内容（关键！）

```bash
# 1. 检查前端源码未被修改
git diff web/src/
# 应该没有任何输出

git status web/src/
# 应该显示 "nothing to commit"

# 2. 检查 Go module 路径未被修改
grep 'module github.com/fastclaw-ai/fastclaw' go.mod
# 应该输出: module github.com/fastclaw-ai/fastclaw

grep -r 'github.com/fastclaw-ai/fastclaw' internal/ | head -5
# 应该看到 import 路径仍然是 github.com/fastclaw-ai/fastclaw

# 3. 检查 Docker 镜像名未被修改
grep -r 'ghcr.io/fastclaw-ai/fastclaw' deploy/
# 应该找到原始镜像名（如果配置中有的话）

grep -r 'thinkany/fastclaw-sandbox' deploy/
# 应该找到原始镜像名

# 4. 统计修改的文件数
git status --short | wc -l
# 应该显示修改的文件数量（预计 60-80 个）

# 5. 检查是否有意外的修改
git diff --name-only | grep -E "(web/src/|go.mod|go.sum)"
# 应该没有输出（这些文件不应该被修改）
```

#### 🔍 深度验证

```bash
# 1. 检查文档中的排除是否生效
grep -r 'github.com/fastclaw-ai/fastclaw' README.md
# 如果文档中提到 Go module 路径，应该保持不变

# 2. 检查是否有遗漏的 FASTCLAW_
grep -r 'FASTCLAW_' --exclude-dir=web --exclude-dir=.git . | grep -v Binary
# 应该没有输出（除了可能在注释中提到的历史信息）

# 3. 检查 Helm 模板函数是否正确替换
grep -r 'fastclaw\.fullname' deploy/helm/fastagent/
# 应该没有输出（都已改为 fastagent.fullname）

grep -r 'fastagent\.fullname' deploy/helm/fastagent/
# 应该找到所有模板函数引用

# 4. 检查是否有混合状态（部分改了部分没改）
grep -r 'FASTCLAW_.*FASTAGENT_' .
# 应该没有输出（不应该有混合状态）
```

---

## 🐛 常见问题排查

### 问题 1：前端被意外修改

**症状**：
```bash
git diff web/src/
# 显示有修改
```

**原因**：`--exclude-dir="web"` 未生效

**解决**：
```bash
git checkout web/src/
# 恢复前端文件
```

### 问题 2：Go module 路径被修改

**症状**：
```bash
grep 'module github.com/fastclaw-ai/fastagent' go.mod
# 找到了错误的路径
```

**原因**：sed 排除模式未生效

**解决**：
```bash
git checkout go.mod
# 恢复 go.mod

# 手动恢复所有 import 路径
find . -name "*.go" -exec sed -i '' 's|github.com/fastclaw-ai/fastagent|github.com/fastclaw-ai/fastclaw|g' {} \;
```

### 问题 3：Docker 镜像名被修改

**症状**：
```bash
grep 'ghcr.io/fastagent-ai/fastagent' deploy/
# 找到了错误的镜像名
```

**原因**：sed 排除模式未生效

**解决**：
```bash
# 恢复部署配置
git checkout deploy/

# 重新运行脚本（确保 sed 排除模式正确）
```

---

## 📋 完整验证检查清单

执行脚本后，按顺序执行以下检查：

### ✅ 第一阶段：基础验证（必须通过）

- [ ] `git diff web/src/` 无输出（前端未修改）
- [ ] `grep 'module github.com/fastclaw-ai/fastclaw' go.mod` 有输出（Go module 路径未变）
- [ ] `ls deploy/helm/fastagent` 目录存在（Helm Chart 已重命名）
- [ ] `grep 'FASTAGENT_HOME' internal/config/env.go` 有输出（环境变量已修改）

### ✅ 第二阶段：详细验证（建议执行）

- [ ] `grep -r 'FASTCLAW_' internal/ | wc -l` 输出为 0（所有环境变量已改）
- [ ] `grep -r 'fastagent_session' internal/auth/` 有输出（Cookie 已改）
- [ ] `grep -r 'x-fastagent-agent-id' internal/api/` 有输出（HTTP Header 已改）
- [ ] `grep -r '\.fastagent' internal/config/` 有输出（路径已改）

### ✅ 第三阶段：排除验证（关键！）

- [ ] `git diff --name-only | grep web/src` 无输出
- [ ] `git diff --name-only | grep go.mod` 无输出
- [ ] `grep -r 'ghcr.io/fastclaw-ai/fastclaw' deploy/` 有输出（镜像名未变）
- [ ] `grep -r 'thinkany/fastclaw-sandbox' deploy/` 有输出（镜像名未变）

### ✅ 第四阶段：功能验证（可选）

- [ ] `go build ./cmd/fastclaw` 编译成功
- [ ] `go test ./...` 测试通过（如有）
- [ ] 手动检查关键文件的修改是否正确

---

## 下一步

执行 `scripts/apply-rename-to-fastagent-final.sh` 脚本进行替换。
