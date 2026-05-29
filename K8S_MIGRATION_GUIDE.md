# K8s 环境迁移指南：FASTCLAW → FASTAGENT

## 🎯 影响分析

### ✅ **不需要迁移的数据**

1. **PostgreSQL 数据库**
   - ✅ 所有用户、agent、会话数据都在 Postgres 中
   - ✅ 只是环境变量名称变了，DSN 连接字符串不变
   - ✅ **无需任何数据库迁移操作**

2. **Object Store (S3/OSS/MinIO)**
   - ✅ 所有 workspace 文件、skills、artifacts 都在对象存储中
   - ✅ Bucket 名称、路径都不变
   - ✅ **无需迁移对象存储数据**

3. **Pod 本地存储 (`/data/.fastclaw`)**
   - ✅ 使用 `emptyDir`，每次 pod 重启自动清空
   - ✅ 只存临时缓存：skills cache、agent identity cache
   - ✅ **会自动重建，无需迁移**

### ⚠️ **需要更新的配置**

1. **K8s ConfigMap** - 环境变量名称
2. **K8s Secret** - 环境变量名称（如果有引用）
3. **前端用户** - 需要重新登录（token 名称变更）

---

## 📋 迁移步骤

### 步骤 1：备份当前配置

```bash
# 备份当前的 ConfigMap 和 Secret
kubectl -n fastclaw get configmap fastclaw-config -o yaml > backup-configmap.yaml
kubectl -n fastclaw get secret fastclaw-secrets -o yaml > backup-secret.yaml

# 备份当前的 Deployment
kubectl -n fastclaw get deployment fastclaw -o yaml > backup-deployment.yaml
```

### 步骤 2：更新代码并构建新镜像

```bash
# 在本地执行重命名
./scripts/apply-rename-to-fastagent.sh

# 提交代码
git add .
git commit -m "refactor: rename FASTCLAW_* to FASTAGENT_*"
git push origin fastagent

# 构建新镜像（假设使用 GitHub Actions 自动构建）
# 或手动构建：
docker build -t your-registry/fastclaw:fastagent .
docker push your-registry/fastclaw:fastagent
```

### 步骤 3：更新 K8s ConfigMap

```bash
# 编辑 ConfigMap
kubectl -n fastclaw edit configmap fastclaw-config
```

**修改内容：**
```yaml
data:
  # 旧的
  FASTCLAW_PORT: "18953"
  FASTCLAW_BIND: "all"
  FASTCLAW_STORAGE_TYPE: "postgres"
  FASTCLAW_STORAGE_AUTO_MIGRATE: "true"
  FASTCLAW_OBJECT_STORE_TYPE: "aliyun-oss"
  FASTCLAW_OBJECT_STORE_REGION: "cn-hangzhou"
  FASTCLAW_OBJECT_STORE_BUCKET: "your-bucket"
  FASTCLAW_OBJECT_STORE_ALIYUN_INTERNAL: "true"

  # 改为
  FASTAGENT_PORT: "18953"
  FASTAGENT_BIND: "all"
  FASTAGENT_STORAGE_TYPE: "postgres"
  FASTAGENT_STORAGE_AUTO_MIGRATE: "true"
  FASTAGENT_OBJECT_STORE_TYPE: "aliyun-oss"
  FASTAGENT_OBJECT_STORE_REGION: "cn-hangzhou"
  FASTAGENT_OBJECT_STORE_BUCKET: "your-bucket"
  FASTAGENT_OBJECT_STORE_ALIYUN_INTERNAL: "true"
```

### 步骤 4：更新 K8s Secret（如果使用 Helm）

如果你使用 Helm 部署，需要更新 `gateway.yaml` 中的环境变量引用：

```bash
# 编辑 Deployment
kubectl -n fastclaw edit deployment fastclaw
```

**修改 env 部分：**
```yaml
env:
  # 旧的
  - name: FASTCLAW_HOME
    value: "/data/.fastclaw"
  - name: FASTCLAW_STORAGE_DSN
    valueFrom:
      secretKeyRef:
        name: fastclaw-secrets
        key: STORAGE_DSN
  - name: FASTCLAW_OBJECT_STORE_ACCESSKEY
    valueFrom:
      secretKeyRef:
        name: fastclaw-secrets
        key: OBJECT_STORE_ACCESSKEY
  - name: FASTCLAW_OBJECT_STORE_SECRETKEY
    valueFrom:
      secretKeyRef:
        name: fastclaw-secrets
        key: OBJECT_STORE_SECRETKEY

  # 改为
  - name: FASTAGENT_HOME
    value: "/data/.fastagent"
  - name: FASTAGENT_STORAGE_DSN
    valueFrom:
      secretKeyRef:
        name: fastclaw-secrets
        key: STORAGE_DSN
  - name: FASTAGENT_OBJECT_STORE_ACCESSKEY
    valueFrom:
      secretKeyRef:
        name: fastclaw-secrets
        key: OBJECT_STORE_ACCESSKEY
  - name: FASTAGENT_OBJECT_STORE_SECRETKEY
    valueFrom:
      secretKeyRef:
        name: fastclaw-secrets
        key: OBJECT_STORE_SECRETKEY
```

**修改 volumeMounts：**
```yaml
volumeMounts:
  # 旧的
  - name: data
    mountPath: /data/.fastclaw

  # 改为
  - name: data
    mountPath: /data/.fastagent
```

### 步骤 5：更新镜像并重启

```bash
# 更新镜像版本
kubectl -n fastclaw set image deployment/fastclaw \
  gateway=your-registry/fastclaw:fastagent

# 或者直接重启（如果镜像 tag 没变）
kubectl -n fastclaw rollout restart deployment/fastclaw

# 查看滚动更新状态
kubectl -n fastclaw rollout status deployment/fastclaw

# 查看新 pod 日志
kubectl -n fastclaw logs -f deployment/fastclaw
```

### 步骤 6：验证

```bash
# 检查 pod 是否正常运行
kubectl -n fastclaw get pods

# 检查环境变量是否正确
kubectl -n fastclaw exec deployment/fastclaw -- env | grep FASTAGENT

# 检查应用日志
kubectl -n fastclaw logs -f deployment/fastclaw

# 测试 API
kubectl -n fastclaw port-forward svc/fastclaw-gateway 18953:18953
curl http://localhost:18953/readyz
```

---

## 🔍 详细说明

### 为什么不需要迁移数据？

#### 1. **数据库（PostgreSQL）**
```go
// 代码中只是读取环境变量名称变了
// 旧代码：
dsn := os.Getenv("FASTCLAW_STORAGE_DSN")

// 新代码：
dsn := os.Getenv("FASTAGENT_STORAGE_DSN")

// 但是 DSN 的值（连接字符串）完全一样：
// postgres://user:pass@host:5432/fastclaw?sslmode=require
```

**结论**：数据库表结构、数据内容完全不变，只是读取配置的环境变量名称变了。

#### 2. **对象存储（S3/OSS）**
```go
// 对象存储的配置也只是环境变量名称变了
// 旧代码：
bucket := os.Getenv("FASTCLAW_OBJECT_STORE_BUCKET")

// 新代码：
bucket := os.Getenv("FASTAGENT_OBJECT_STORE_BUCKET")

// 但是 bucket 名称还是一样的：
// "your-fastclaw-bucket"
```

**结论**：对象存储的 bucket、路径、文件内容完全不变。

#### 3. **Pod 本地存储**
```yaml
# K8s 配置中使用 emptyDir
volumes:
  - name: data
    emptyDir: {}  # 每次 pod 重启都会清空
```

从 `gateway.yaml` 注释可以看到：
```yaml
# /data/.fastclaw only holds pod-local ephemeral state; workspace
# artifacts go to the object store.
```

**存储内容**：
- SQLite DB（但你用的是 Postgres，所以不在这里）
- Skills cache（会自动从对象存储重新下载）
- Agent identity files cache（会自动从数据库重建）
- Sandbox roots（临时文件）

**结论**：这些都是临时缓存，pod 重启后会自动重建，无需迁移。

---

## ⚠️ 注意事项

### 1. **用户需要重新登录**

前端 token 名称变更：
- `fastclaw_session` → `fastagent_session`
- `fastclaw_token` → `fastagent_token`

**影响**：所有已登录用户的 session 会失效，需要重新登录。

### 2. **滚动更新期间的兼容性**

在滚动更新期间，可能同时存在旧 pod 和新 pod：
- 旧 pod：读取 `FASTCLAW_*` 环境变量
- 新 pod：读取 `FASTAGENT_*` 环境变量

**解决方案**：
- 方案 1：先更新 ConfigMap 添加两套环境变量（兼容期）
- 方案 2：使用蓝绿部署，一次性切换

**推荐方案 1（兼容期）：**

```yaml
# 第一步：ConfigMap 同时包含两套变量
data:
  # 旧的（兼容旧 pod）
  FASTCLAW_PORT: "18953"
  FASTCLAW_STORAGE_TYPE: "postgres"
  # ... 其他旧变量

  # 新的（给新 pod 用）
  FASTAGENT_PORT: "18953"
  FASTAGENT_STORAGE_TYPE: "postgres"
  # ... 其他新变量

# 第二步：部署新镜像，滚动更新
# 第三步：确认所有 pod 都是新版本后，删除旧的 FASTCLAW_* 变量
```

### 3. **Helm Chart 更新**

如果使用 Helm 部署，需要：

```bash
# 更新 Helm chart
cd deploy/helm/fastclaw

# 修改 templates/configmap.yaml 和 templates/gateway.yaml
# 然后升级
helm upgrade fastclaw . -n fastclaw
```

---

## 🔄 回滚方案

如果出现问题需要回滚：

```bash
# 回滚到上一个版本
kubectl -n fastclaw rollout undo deployment/fastclaw

# 或回滚到指定版本
kubectl -n fastclaw rollout history deployment/fastclaw
kubectl -n fastclaw rollout undo deployment/fastclaw --to-revision=2

# 恢复旧的 ConfigMap
kubectl apply -f backup-configmap.yaml
```

---

## ✅ 迁移检查清单

- [ ] 备份当前 K8s 配置（ConfigMap, Secret, Deployment）
- [ ] 执行代码重命名脚本
- [ ] 构建并推送新镜像
- [ ] 更新 ConfigMap（添加 FASTAGENT_* 变量）
- [ ] 更新 Deployment（环境变量引用和 volumeMounts）
- [ ] 部署新镜像，执行滚动更新
- [ ] 验证新 pod 正常运行
- [ ] 验证数据库连接正常
- [ ] 验证对象存储访问正常
- [ ] 验证前端功能正常（用户需要重新登录）
- [ ] 清理旧的 FASTCLAW_* 环境变量（可选）
- [ ] 通知用户重新登录

---

## 📞 常见问题

### Q: 会丢失数据吗？
**A**: 不会。所有持久化数据都在 Postgres 和对象存储中，这些都不受影响。

### Q: 需要停机维护吗？
**A**: 不需要。使用滚动更新，服务不会中断。但用户需要重新登录。

### Q: 多久能完成迁移？
**A**:
- 更新配置：5 分钟
- 滚动更新：2-5 分钟（取决于 pod 数量）
- 总计：10-15 分钟

### Q: 如果出错怎么办？
**A**: 使用 `kubectl rollout undo` 立即回滚到上一个版本。

### Q: 需要通知用户吗？
**A**: 需要。用户的 session 会失效，需要重新登录。建议提前通知。
