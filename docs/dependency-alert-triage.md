# Dependency alerts: what the 164 actually are

**日期**: 2026-09-15 · **状态**: 🚧 第一批已修，第二批待做 · **触发**: push 时 GitHub 报 default branch 164 条（11 critical / 57 high）

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
* **Go 侧未动**：`go.mod`/`go.sum` 当时在工作区里已被改动（`slack-go/slack` 0.19→0.29、
  `golang.org/x/crypto` 0.46→0.55），来源不是本次改动，未被提交也未被回退。

## 5. 门

web 侧改动仍受 `web-test.yml`（typecheck + lint --max-warnings=0 + test）与
`.githooks/pre-commit` 约束 —— 升级依赖也不例外。
