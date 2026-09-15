# Dependency alerts: what the 164 actually are

**日期**: 2026-09-15（§5 于 09-16 补） · **状态**: 🚧 web 第一批已修、Go 侧已修，`next` / `@streamdown/mermaid` 待做 · **触发**: push 时 GitHub 报 default branch 164 条（11 critical / 57 high）

---

## 1. 先说口径：三个数字不是一回事

| 来源 | 数字 | 口径 |
|---|---|---|
| GitHub Dependabot | 164（11 critical / 57 high） | 扫描**所有被识别的 lockfile**，含 dev |
| `pnpm audit --prod`（web/） | 117（2 critical / 41 high） | 只算**生产**依赖树 |
| 修完之后的生产口径 | **52**（2 critical / 21 high） | 同上 |

## 2. 两条根因（都已修）

### 2.1 `web/bun.lock` 是死的，但 Dependabot 照样扫

最后写入 **2026-05-06**；Dockerfile / Makefile / 三个 workflow **没有任何一处用 bun**
（镜像走 pnpm 10.15.0 + `pnpm-lock.yaml --frozen-lockfile`）。一个四月前的第二把锁，
不只是死重量：它会被当成独立生态扫描，把项目从不构建的依赖树算进告警。

**已修**：删除 `web/bun.lock`。

### 2.2 `shadcn` 是脚手架 CLI，却挂在 `dependencies`

生产口径 117 条里，**65 条收敛到同一条路径**：`shadcn > @modelcontextprotocol/sdk > hono`
（外加 `postcss-selector-parser` 等）。而 `src/` 里**没有任何 import 引用 shadcn** ——
它是手工 `npx shadcn add` 用的。

**已修**：移入 `devDependencies`。生产口径 **117 → 52**，构建、测试（9 passed）、lint（0/0）、
`pnpm install --frozen-lockfile` 全部照旧通过。

## 3. 还剩什么（按包聚合，生产口径）

| 顶层包 | 条数 | 性质与下一步 |
|---|---|---|
| `next` | 43 | 需要**在 16.x 线内**升到修补版。要过完整门（typecheck + lint + test + build），单独一次提交 |
| `@streamdown/mermaid` | 9 | 走 streamdown 家族的 mermaid 依赖；先查该家族的修补范围，再决定是升 streamdown 还是覆盖 mermaid |

dev 侧（Dependabot 计入、`--prod` 不计入）另有若干，随上两条一起收。

## 4. 明确没做，以及为什么

* **没有做整锁升级**：在一次性会话的尾段跑 `pnpm up --latest` 是「把构建弄挂」的经典方式。
  上面两条要各自一次提交，各自过门。
* **没有逐条评估可利用性**：本文件是「声明依赖树」的分诊，不是 per-advisory 的风险判定。
  数字降下来不等于风险清零；`next` 那 43 条在升完之前仍是真实暴露面。
* **没有逐条评估「可达性」再决定要不要升**：Go 侧试过一次（见 §5），结论是照升不误 ——
  x/crypto 那 7 条 critical 全是 `x/crypto/ssh` 的 advisory，而本仓库只用 `bcrypt` 一个包，
  一条都够不着。可达性可以用来排优先级，不能用来把版本钉在旧线上。

## 5. Go 侧：一次升级，和它自己的证据链（2026-09-16）

### 5.1 升了什么，以及为什么是这个版本

| 模块 | 改动 | 取值理由 |
|---|---|---|
| `golang.org/x/crypto` | 0.46.0 → **0.55.0** | 告警门槛是 0.52.0（7 条 critical 收敛到这一个版本）；**0.56.0 起 `requires go >= 1.26.0`**，而 Dockerfile 是 `golang:1.25-alpine`、CI 是 `go-version: "1.25"` |
| `golang.org/x/net` | 0.48.0 → **0.58.0** | 门槛 0.55.0；0.59.0 同样要 1.26 |
| `github.com/slack-go/slack` | 0.19.0 → **0.29.0** | 安全门槛是 0.23.1（`SecretsVerifier` 接受空签名密钥）。用到的那几个符号（`slack.New` / `MsgOptionText` / `UploadFileParameters` / `socketmode` / `slackevents`）避开 0.24.0 与 0.25.0 两次 block-kit breaking，也避开了 0.23.1 里 `NewSecretsVerifier` 的行为变更 |
| `github.com/klauspost/compress` | 1.18.2 → **1.20.0** | **Dependabot 没报这条**：是 govulncheck 报的（GO-2026-5841，`s2` 越界读，且在 import 图里**可达**）。修在 1.18.7 |

`go.mod` 的 `go` 指令保持 **1.25.0**，`GOTOOLCHAIN=local go build ./...` 通过 ——
镜像与 CI 都不必跟着换工具链。代价是 x/crypto 停在 0.55.0，于是留下 3 条模块层告警：
两条 `x/crypto/ssh` 的死锁 DoS（修在 0.56.0，被 1.26 那道门槛挡住）与一条 openpgp 的
「无修复、建议弃用」。三条都在代码不可达的包上（本仓库只用 `bcrypt`）。要收掉前两条，
得单独做一次「Go 1.26 迁移」，那是另一件事。

### 5.2 为什么"测试还绿"不算验证

依赖升级最危险的失败模式是全绿：测试跑的是当前代码，不是旧数据的兼容性。所以两边各跑一次
**改前 / 改后**同一条命令（同一台机器、同一份 embed 产物）：

| 检查 | 改前 | 改后 |
|---|---|---|
| `go test ./... -count=1 -race` | 34 包 `ok` / 0 FAIL | 34 包 `ok` / 0 FAIL（逐包 diff 无差异） |
| `go vet ./...` | 干净 | 干净 |
| `govulncheck ./...` | 1 条**可达**（compress 那条） | **0 条可达**；剩 3 条模块层、代码不可达 |
| `GOTOOLCHAIN=local` 构建 | — | 通过（工具链未被顶到 1.26） |

前两项只说明"没弄坏"。真正的问题是**旧数据还认不认**，那要跨版本对拍：同一份探针分别用改前
和改后的依赖集构建，输出逐格对比。

| 面 | 结果 |
|---|---|
| bcrypt：v0.46.0 写出的哈希（ASCII / 中文 / 66 字节 / 恰好 72 字节） | 0.55.0 下**全部仍然通过**；错密码仍然失败 |
| bcrypt：反向（新版本写、旧版本验） | 也通过 —— 回滚不会把已存用户锁在外面 |
| bcrypt：72 字节边界 | 两边一致：72 字节可生成、73 字节报 `ErrPasswordTooLong`；**验密码侧只读前 72 字节**（生成拒绝、比较截断），中文 24 字 = 72 字节在内 |
| s2 / zstd | 双向可解、字节完全一致（S3 workspace 路径的线上格式） |
| Slack `events_api` 载荷 | 适配器读的字段逐个一致；唯一 delta 是解码后的 `MessageEvent` 多一个 `"blocks": null`，而适配器只读不写 |

结论：这四条升级**零行为变更**。两个不变量随后固化成仓库用例 ——
`internal/users/bcrypt_compat_test.go`（旧版本写出的哈希写死在 fixture 里）与
`internal/channels/slack_wire_test.go`（原始 JSON 载荷 → 总线字段）。同一份用例在**旧依赖集**
上也全绿（说明钉的是产品契约而不是库版本），把 fixture 改一个字符就立刻失败（说明它们不是空转）。

### 5.3 顺带记下的两件事

* **`GenerateFromPassword` 与 `CompareHashAndPassword` 的长密码行为不对称**：前者对 >72 字节
  直接报错（所以 `users.Create` / `SetPassword` 根本存不进超长密码），后者只读前 72 字节
  （所以用户提交超长密码时，前 72 字节对得上就能登录）。这是 bcrypt 的既有语义、两个版本
  完全相同，不是本次升级引入的；但 API 层面对超长密码给出的报错是否友好，值得单独看一眼。
* **Dependabot 漏了一条可达漏洞**：compress 那条既不在 164 条里，也不在 `pnpm audit` 里。
  数字降下来 ≠ 风险清零，反过来也成立 —— 数量口径的分诊替代不了 govulncheck。

## 6. 门

web 侧改动仍受 `web-test.yml`（typecheck + lint --max-warnings=0 + test）与
`.githooks/pre-commit` 约束 —— 升级依赖也不例外。Go 侧对应 `go-test.yml`（含 `-race`）；
目前**没有**把 `govulncheck` 接进 CI，所以 §5.3 那条漏报还会再发生一次。
