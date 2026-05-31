# FastClaw → FastAgent 重命名计划 - 最小改动原则 Review

## 🎯 核心原则

**目标**: 不对外部用户暴露 `fastclaw` 品牌名
**策略**: 最小改动原则 - 只改用户能看到的，内部实现保持不变

---

## 📊 逐项 Review - 用户可见性分析

### 分类标准

- 🔴 **外部用户可见** - 必须修改
- 🟡 **开发者/运维可见** - 根据场景决定
- 🟢 **内部实现** - 不修改

---

## 1️⃣ 环境变量（35个）

### 当前计划：全部修改
```
FASTCLAW_HOME → FASTAGENT_HOME
FASTCLAW_PORT → FASTAGENT_PORT
... (35个)
```

### 可见性分析

| 环境变量 | 谁能看到 | 场景 | 是否修改 |
|---------|---------|------|---------|
| `FASTCLAW_HOME` | 🟡 运维人员 | 配置服务器时 | ❓ |
| `FASTCLAW_PORT` | 🟡 运维人员 | 配置服务器时 | ❓ |
| `FASTCLAW_STORAGE_DSN` | 🟡 运维人员 | 配置数据库时 | ❓ |
| `FASTCLAW_*` (其他) | 🟡 运维人员 | 配置服务器时 | ❓ |

### 用户能看到吗？

**场景 1**: 普通用户使用产品
- ❌ 看不到环境变量
- ❌ 不会接触服务器配置

**场景 2**: 用户查看进程信息
```bash
ps aux | grep fastclaw
# 可能看到: /usr/bin/fastclaw --env FASTCLAW_HOME=...
```
- ⚠️ 技术用户可能看到

**场景 3**: 用户查看错误日志
```
Error: FASTCLAW_STORAGE_DSN not configured
```
- ⚠️ 用户可能在错误消息中看到

### 🤔 问题：环境变量是否需要改？

**改的理由**:
- ✅ 技术用户可能在进程列表中看到
- ✅ 错误消息中可能暴露

**不改的理由**:
- ✅ 普通用户完全看不到
- ✅ 只有运维人员接触
- ✅ 改动成本高（所有部署环境）

### 💡 建议

**方案 A（激进）**: 全部改 → 彻底消除品牌暴露
**方案 B（保守）**: 不改 → 最小改动，只改用户直接看到的
**方案 C（折中）**: 只改错误消息中引用的环境变量名

---

## 2️⃣ 文件系统路径

### 当前计划：全部修改
```
~/.fastclaw → ~/.fastagent
/data/.fastclaw → /data/.fastagent
```

### 可见性分析

**场景 1**: 用户在文件管理器中看到
```
/Users/username/.fastclaw/
```
- 🔴 **用户可见** - 如果用户浏览隐藏文件

**场景 2**: 错误消息中出现
```
Error: Cannot write to ~/.fastclaw/logs/
```
- 🔴 **用户可见** - 错误提示中

**场景 3**: 文档中的路径示例
```
Your data is stored in ~/.fastclaw/
```
- 🔴 **用户可见** - 文档中

### 💡 建议

✅ **必须修改** - 用户可能看到这个目录名

---

## 3️⃣ HTTP Headers

### 当前计划：全部修改
```
x-fastclaw-agent-id → x-fastagent-agent-id
x-fastclaw-session-key → x-fastagent-session-key
x-fastclaw-channel → x-fastagent-channel
```

### 可见性分析

**场景 1**: API 开发者使用
```javascript
fetch('/api/chat', {
  headers: {
    'x-fastclaw-agent-id': 'xxx'
  }
})
```
- 🔴 **外部开发者可见** - API 文档中

**场景 2**: 浏览器开发者工具
```
Request Headers:
x-fastclaw-agent-id: xxx
```
- 🔴 **技术用户可见** - 开发者工具中

### 💡 建议

✅ **必须修改** - 这是公开的 API 接口，外部开发者会看到

---

## 4️⃣ Cookie 名称

### 当前计划：全部修改
```
fastclaw_session → fastagent_session
fastclaw-affinity → fastagent-affinity
```

### 可见性分析

**场景**: 浏览器开发者工具
```
Cookies:
fastclaw_session=xxx
```
- 🔴 **技术用户可见** - 开发者工具中

### 💡 建议

✅ **必须修改** - 用户在浏览器中可以看到

---

## 5️⃣ 错误消息和系统提示

### 当前计划：修改 Go 代码中的错误消息
```go
// 旧
errors.New("fastclaw connect dialog")
fmt.Sprintf("FastClaw: %s", version)

// 新
errors.New("fastagent connect dialog")
fmt.Sprintf("FastAgent: %s", version)
```

### 可见性分析

**场景 1**: 用户看到错误提示
```
Error: feishu webhook rejected — set it in the fastclaw connect dialog
```
- 🔴 **用户可见** - 前端错误提示

**场景 2**: Agent 在对话中提到
```
Agent: FastClaw: version 0.1.0. If you want to upgrade, run fastclaw upgrade.
```
- 🔴 **用户可见** - 对话内容

### 💡 建议

✅ **必须修改** - 用户会在错误提示和对话中看到

---

## 6️⃣ Helm Chart 名称

### 当前计划：修改
```
name: fastclaw → name: fastagent
```

### 可见性分析

**场景**: 运维人员使用 Helm
```bash
helm list
# NAME        NAMESPACE   STATUS
# fastclaw    default     deployed
```
- 🟡 **运维人员可见** - 只有运维接触

### 💡 建议

❌ **不需要修改** - 这是内部技术名称，用户看不到

**理由**:
- 只有运维人员使用 Helm
- 普通用户不会接触 K8s
- 修改需要重新安装，成本高

---

## 7️⃣ K8s 标签

### 当前计划：修改
```yaml
app.kubernetes.io/name: fastclaw → fastagent
```

### 可见性分析

**场景**: 运维人员使用 kubectl
```bash
kubectl get pods -l app.kubernetes.io/name=fastclaw
```
- 🟡 **运维人员可见** - 只有运维接触

### 💡 建议

❌ **不需要修改** - 这是内部 K8s 标签，用户看不到

---

## 8️⃣ PostgreSQL 数据库名

### 当前计划：修改
```
POSTGRES_DB: fastclaw → fastagent
```

### 可见性分析

**场景**: 数据库管理员
```sql
\l  -- 列出数据库
-- fastclaw
```
- 🟢 **内部实现** - 用户完全看不到

### 💡 建议

❌ **不需要修改** - 数据库名称是内部实现细节

**理由**:
- 用户永远看不到数据库名
- 修改需要数据迁移，风险高
- 你的 development 环境已经用 `fastagent`，但这不是必须的

---

## 9️⃣ Go Module 路径

### 当前计划：不修改
```
github.com/fastclaw-ai/fastclaw
```

### 可见性分析

**场景**: 开发者查看源码
- 🟢 **内部实现** - 只有开发者看到

### 💡 建议

✅ **不修改** - 这是内部代码结构，用户看不到

---

## 🔟 Docker 镜像名

### 当前计划：不修改
```
ghcr.io/fastclaw-ai/fastclaw
thinkany/fastclaw-sandbox
```

### 可见性分析

**场景 1**: 运维人员部署
```bash
docker pull ghcr.io/fastclaw-ai/fastclaw
```
- 🟡 **运维人员可见**

**场景 2**: 用户在 WebUI 配置中看到
```
Sandbox Image: thinkany/fastclaw-sandbox:latest
```
- 🔴 **用户可见** - 如果在 WebUI 中显示

### 💡 建议

**取决于 WebUI 是否显示镜像名**:
- 如果 WebUI 显示 → ✅ 需要修改
- 如果 WebUI 不显示 → ❌ 不需要修改

---

## 📊 最小改动原则 - 最终建议

### ✅ 必须修改（用户可见）

| 项目 | 原因 | 优先级 |
|------|------|--------|
| 文件系统路径 | 用户可能看到目录名 | 🔴 高 |
| HTTP Headers | API 开发者会看到 | 🔴 高 |
| Cookie 名称 | 浏览器中可见 | 🔴 高 |
| 错误消息 | 前端错误提示 | 🔴 高 |
| 系统提示 | Agent 对话内容 | 🔴 高 |

### ❓ 可选修改（根据场景）

| 项目 | 场景 | 建议 |
|------|------|------|
| 环境变量 | 技术用户可能在进程列表/错误中看到 | 🟡 建议改 |
| Docker 镜像名 | 如果 WebUI 显示 | 🟡 视情况 |

### ❌ 不需要修改（内部实现）

| 项目 | 原因 |
|------|------|
| Helm Chart 名称 | 运维内部使用 |
| K8s 标签 | 运维内部使用 |
| PostgreSQL 数据库名 | 内部实现细节 |
| Go Module 路径 | 代码内部结构 |

---

## 🎯 推荐方案

### 方案 A：最小改动（推荐）

**只改用户直接看到的**:
1. ✅ 文件系统路径
2. ✅ HTTP Headers
3. ✅ Cookie 名称
4. ✅ 错误消息和系统提示
5. ✅ 文档中的引用

**不改内部实现**:
- ❌ 环境变量（或只改错误消息中引用的）
- ❌ Helm Chart 名称
- ❌ K8s 标签
- ❌ PostgreSQL 数据库名
- ❌ Go Module 路径
- ❌ Docker 镜像名（如果 WebUI 不显示）

**优点**:
- ✅ 达到目标：用户看不到 fastclaw
- ✅ 改动最小，风险最低
- ✅ 无需数据迁移
- ✅ 无需重新安装 Helm

---

### 方案 B：彻底改动（当前计划）

**改所有能改的**:
- 包括环境变量、Helm Chart、K8s 标签、PostgreSQL

**优点**:
- ✅ 彻底消除 fastclaw 痕迹

**缺点**:
- ❌ 改动大，风险高
- ❌ 需要数据迁移
- ❌ 需要重新安装 Helm
- ❌ 所有部署环境需要更新

---

## 🤔 需要你决定

请告诉我你倾向于哪个方案，或者我们逐项讨论每个是否需要修改。

**关键问题**:
1. 环境变量是否需要改？
2. Helm Chart 名称是否需要改？
3. PostgreSQL 数据库名是否需要改？
4. Docker 镜像名在 WebUI 中是否显示？
