# Dependency alerts: what the 164 actually are

**日期**: 2026-09-15（§5–§9 于 09-16 补） · **状态**: ✅ `pnpm audit` 生产口径与全口径均为 0；Go 侧 0 可达 · **触发**: push 时 GitHub 报 default branch 164 条（11 critical / 57 high）

---

## 1. 先说口径：三个数字不是一回事

| 来源 | 数字 | 口径 |
|---|---|---|
| GitHub Dependabot | 164（11 critical / 57 high） | 扫描**所有被识别的 lockfile**，含 dev |
| `pnpm audit --prod`（web/） | 117（2 critical / 41 high） | 只算**生产**依赖树 |
| `shadcn` 挪走之后 | **52**（2 critical / 21 high） | 同上 |
| `next` 升到 16.3.5 之后 | **13**（0 critical / 2 high） | 同上，见 §6 |
| overrides 顶掉 mermaid / dompurify 一族（§7） | **0** | `pnpm audit --prod` 报 "No known vulnerabilities found" |
| dev 侧 16 条 overrides（§9） | **0**（全口径也是 0） | `pnpm audit` 与 `pnpm audit --prod` 双双 "No known vulnerabilities found" |

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
| ~~`next`~~ | ~~43~~ | **已修**：16.1.6 → 16.3.5，见 §6 |
| ~~`@streamdown/mermaid`~~ | ~~9~~ | **已修**：上游没有新版可升，改用 pnpm overrides 顶 mermaid 与 dompurify，见 §7 |

dev 侧（Dependabot 计入、`--prod` 不计入）另有若干。`next` 修完后 `--prod` 曾剩 13 条，
收敛成 5 个包：`mermaid`（low/moderate 5）、`dompurify`（low/moderate 4）、`browserslist`（high 2）、
`baseline-browser-mapping`（moderate 1）、`@babel/core`（low 1）—— 这 5 个已由 §7 的 overrides 收掉，
生产口径归零。

## 4. 明确没做，以及为什么

* **没有做整锁升级**：在一次性会话的尾段跑 `pnpm up --latest` 是「把构建弄挂」的经典方式。
  分开升的代价是两个提交，收益是 `next` 那一步出事时能一眼看出是它（§6.3 的两条就是这么抓到的）；
  剩下的 streamdown 一族同样要单独一次提交、单独过门。
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
只能把工具链升到 Go 1.26 —— **那不代表要升，这是明确不在计划内的**：

> **决定（2026-09-16）**：Go 版本线留在 1.25.0，不随依赖升级而动。
> `go.mod` 的 `go` 指令、Dockerfile 的 `golang:1.25-alpine`、CI 的 `go-version: "1.25"`
> 三处必须一起改，且必须由人明确决定，不由任何一次依赖升级顺带触发。
> 上面那 3 条模块层 advisory 因此**长期保留**：它们在代码不可达的包上，是已知且被接受的。
> 需要它们消失时再谈工具链，而不是反过来。

判断标准（下次直接套用）：升级依赖时，**版本取"能修掉告警、又留在 Go 1.25 上"的最高者**，
而不是取 latest。x/crypto 从 0.57.0 退回 0.55.0 就是这么定的（0.56.0 起要求 Go ≥ 1.26）。

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

## 6. web：`next` 16.1.6 → 16.3.5（2026-09-16）

43 条 next 告警里，所有 advisory 点名的最高修补版本是 **16.3.3**（两条 critical —— AVIF 图像优化的
RCE 与 Windows 主机上的 RCE —— 都修在那里），所以取 16.3.5；`eslint-config-next` 与它同步钉。

生产口径随之从 **52 降到 13**，critical 归零，剩下 5 个包（mermaid / dompurify / browserslist /
baseline-browser-mapping / @babel/core），全在 §3。

### 6.1 门

改前 / 改后各跑一遍，pnpm 用 **10.15.0**（与 Dockerfile、`web-test.yml` 同钉，
本地默认的 9.15.0 不算数）：`install --frozen-lockfile`、`typecheck`、`lint --max-warnings=0`、
`test`（11 passed）、`build` —— 两边都过。

### 6.2 但真正验证这次升级的是产物对拍

web 是静态导出、再嵌进 Go 二进制，所以"构建成功"说明不了用户看到的东西没变。两边各构建一次，
逐页比对 `out/`：

| 检查 | 结果 |
|---|---|
| HTML 页面可见文本（去 script/style/标签后逐页比对） | **42 / 42 完全一致** |
| HTML 里引用的 `/_next/` 资源是否存在 | 改前 1266 条、改后 1224 条，**两边都 0 悬空** |
| CSS / chunk / manifest 文件数 | 68 / 417→420 / 一致 |
| 总文件数 | 862 → 705 —— 差额全部是 RSC `.txt` 边车（**319 → 160**，16.3 去掉了 `_head`/`_index` 那批重复件、换成 `index.txt`） |

### 6.3 两个会咬人的地方

* **新 lint 规则**：`eslint-config-next` 16.3 带来 `@next/next/no-location-assign-relative-destination`，
  在本树触发 4 处。它们不是同一类问题，所以没有同一个答案：`/agents` 上点卡片进聊天是普通内部导航
  → 改成 `router.push`（与树里其它导航一致）；登出与改密码后的整页刷新是**故意**要丢掉客户端缓存
  （避免换身份后复用上一个身份的 RSC 载荷）→ 保留刷新，就地写清理由后禁用该规则。
  不处理的话，这个仓库 `--max-warnings=0` 的门会在下一次 bump 上直接红掉。
* **构建日志的格式变了**：16.3 把动态路由打印成"父行 `[id]` + 缩进子行 `default`"的树，16.1 只打印
  模式行。拿两个版本的构建日志直接 diff，看起来像整棵 `/agents/[id]`（16 条路由）消失了；产物里
  一条不少。**日志 diff 不能当产物 diff 用**，这就是 §6.2 存在的原因。

## 7. web：overrides 收掉 mermaid / dompurify 一族（2026-09-16）

`next` 修完后，生产口径剩下的 13 条全部集中在这里：`mermaid` 5、`dompurify` 4、
`browserslist` 2（high）、`baseline-browser-mapping` 1、`@babel/core` 1（low）。其中
dompurify 那条是 **XSS**（`IN_PLACE` 钩子移除后留下可执行的游离子树），而聊天界面正是拿它
过滤模型输出的；mermaid 那几条是配置 API 的原型污染、CSS 注入与 XY/radar 图的 DoS。

**为什么不能靠升级**：`@streamdown/mermaid@1.0.2` 已经是该包最新版，而它把 `mermaid` 钉成
**精确的 11.15.0**；`mermaid` 又精确钉 `dompurify@3.4.8`，`next@16.3.5` 精确钉
`baseline-browser-mapping@2.10.8`，`@babel/helper-compilation-targets` 精确钉
`browserslist@4.28.1`。四条链全是精确钉版，范围更新够不到，所以用 `pnpm.overrides`：

| 包 | 被顶到 | 修补门槛 |
|---|---|---|
| `mermaid` | 11.15.0 → **11.17.2** | 11.16.1 —— 留在 11.x 线内，mermaid 12 是 breaking |
| `dompurify` | 3.4.8 → **3.4.15** | 3.4.13 |
| `browserslist` | 4.28.1 → **4.29.0** | 4.28.7 |
| `baseline-browser-mapping` | 2.10.8 → **2.11.23** | 2.11.0 |
| `@babel/core` | 7.29.0 → **7.29.7** | 7.29.6 |

**门**：`typecheck` / `lint --max-warnings=0` / `test`（20 passed）/ `build` 全过，
`install --frozen-lockfile` 与 Dockerfile、CI 同钉 pnpm 10.15.0。
收完之后 `pnpm audit --prod` 报 **No known vulnerabilities found** —— 生产依赖树归零。

**产物对拍**（对着 `next` 那一步的产物比）：42 个 HTML 逐页可见文本 **0 处不同**、
**0 悬空引用**。代价是明确量出来的：`/` 与 `/settings` 的脚本字节数不变，
`/chat` 与 `/agents/[id]/chat` 各 **+77.3 KiB**（+3 个脚本）——mermaid 与 dompurify 就在这条链路上。

**两条用例**，因为它们防的不是同一件事：

1. `src/__tests__/mermaid-render.test.tsx` 用聊天真正用的 `<ChatMarkdown>` 渲染 flowchart 与
   sequenceDiagram，断言确实产出 `<svg>`。强制版本最典型的失败模式是"编译得过、打包得过、
   chat 里变成一个空框"，构建看不出来，这条能。
2. 同一文件里断言锁文件里这五个包都在修补门槛之上。第 1 条在旧版本上也会绿，只有第 2 条能
   抓住"override 被删掉"——它同时对着**改过版本的锁文件文本**自检一次，证明自己不是空转。

### 7.1 一个自己撞上的坑

本机基础工具链是 **go1.25.0**，落后 14 个补丁版；`govulncheck` 在它下面报 **31 条标准库可达漏洞**，
而 CI 用的 `go-version: "1.25"` 会装到 1.25.14，同一份代码报 **0 条**。所以这类红灯要先看清它
怪的是依赖还是工具链——这也是 §8 把"工具链版本"写进 job 注释的原因。

## 8. 两个口子（2026-09-16）

### 8.1 `govulncheck` 进 CI

`go-test.yml` 新增 `vulncheck` job：`go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...`。

* 钉 **v1.7.0** 而不是 `@latest`：v1.8.0 要 Go 1.26，而本仓库（含 Dockerfile）钉 1.25。
* 它只对**可达**漏洞返回非零——这正是要卡的那条线。它今天仍会列出 3 条 x/crypto 的 ssh advisory，
  但不会因此变红（那三条在只用 bcrypt 的树上够不到）。
* 加它的理由写进了 job 注释：那 164 条里**没有**真正可达的 compress 漏洞，是 govulncheck 找出来的。

### 8.2 Dependabot 从"只报警"变成"会开 PR"

之前的状态是：告警开着（那是仓库设置，默认就有），但既没有 `.github/dependabot.yml`，
`automated-security-fixes` 也是 `false` —— 所以 164 条只会堆着，没有任何机制往下压。

现在：

* 新增 `.github/dependabot.yml`：`gomod`（`/`）与 `npm`（`/web`）周更，**minor/patch 分组**、
  major 单独一个 PR（major 是决定，不是杂务）。
* **安全更新也分组**（`applies-to: security-updates`）。这条是开启安全更新的前提：不解组就是
  87 个 PR，而读不过来的队列等于没人读。
* 仓库设置 `automated-security-fixes` 由 `false` 改为 `true`。

当时剩下的 87 条（`hono` 30、`brace-expansion` 9、`fast-uri` 7 等）本来是打算交给它的，
结果发现它们 Dependabot 也修不动（§8.3），最后由 §9 的 overrides 收掉 —— 这条例子的结论是：
**Dependabot 负责"知道"，override 负责"改掉"**，两者缺一不可。

### 8.3 第一次运行的结果：能报警 ≠ 能修

分组配置生效了（job 定义里能看到 `web-security` 组，且安全更新确实按组合并成一次运行），
但第一次安全更新**跑失败了**，原因不是配置写错，而是 Dependabot 自己给出的
`security_update_not_possible`：父包把它们钉在修补版本之下，它想升也升不上去。完整清单 15 条：

| 包 | 被钉在 | 需要 |
|---|---|---|
| `hono` | 4.12.8 | 4.13.5 |
| `@hono/node-server` | 1.19.11 | 1.19.15 |
| `nanoid` | 3.3.11 | 3.3.18 |
| `js-yaml` | 4.1.1 | 4.3.2 |
| `postcss` | 8.5.8 | 8.5.23 |
| `postcss-selector-parser` | 6.0.10 | 6.0.11 |
| `brace-expansion` | 1.1.12 | 1.1.18 |
| `fast-uri` | 3.1.0 | 3.1.6 |
| `picomatch` | 2.3.1 | 2.3.2 |
| `qs` | 6.15.0 | 6.16.0 |
| `ip-address` | 10.1.0 | 10.3.1 |
| `body-parser` | 2.2.2 | 2.3.0 |
| `@humanfs/node` | 0.16.7 | 0.16.8 |
| `flatted` | 3.4.1 | 3.4.2 |
| `path-to-regexp` | 6.3.0 | 7.0.0 —— **跨大版本**，不是 override 能随手顶的 |

结论和 §7 是同一个：这棵树的传递依赖被上游精确钉版，**告警会到，PR 不会到**。
这 15 条已于 §9 用同一把扳手收掉。
这 15 条要收，得走 §7 那条路（逐个 override），而它们全是 **dev / 构建期**依赖
（生产口径已经是 0）—— 影响面是本地与 CI 的工具链，不是用户请求路径。这值得单独一次，
不该混在这次里顺手做完。

另外：安全更新跑失败会在 Actions 里留下红色的 Dependabot 运行（它不影响 `go-test` /
`web-test`，那两条是绿的），别把它当成 CI 挂了。

## 9. dev 侧扫尾：同一把扳手，全口径归零（2026-09-16）

§8.3 那 15 条"Dependabot 生不出更新"的链，最后是同一把扳手收掉的，但过程里有两个判断值得记。

**一、`shadcn` 不能删。** §2.2 把它挪进 `devDependencies` 时，理由是"`src/` 里没有 import"——
这句今天仍然成立，但它漏了一件事：`src/app/globals.css` 第 3 行是
`@import "shadcn/tailwind.css";`，构建期真的要读这个包里的样式表。所以它不是纯 CLI，
它那棵子树（`@modelcontextprotocol/sdk` → `hono` / `@hono/node-server` / `ajv` / `express` /
`body-parser` / `qs` / `ip-address` / `router` / `path-to-regexp`）只能治，不能去。

**二、那些"精确钉版"多半是锁文件的粘性解析，不是上游硬钉。** MCP SDK 对 hono 声明的是
`^4.11.4`、express 对 qs 声明 `^6.14.0`、body-parser 声明 `^2.2.1` —— 锁文件里那个裸版本号
是 pnpm 写下的解析结果，不是它的要求。所以同 major 内 override 是安全的；
真正越不过去的是 `path-to-regexp@6.3.0`（见下）。

**做法**：`pnpm.overrides` 再加 16 条，多 major 的分开写 —— `brace-expansion@1` / `@5`、
`picomatch@2` / `@4`、`nanoid@3`、`postcss-selector-parser@6` / `@7`、`path-to-regexp@8`；
单 major 的（`hono`、`@hono/node-server`、`fast-uri`、`qs`、`body-parser`、`ip-address`、
`js-yaml`、`postcss`、`flatted`、`@humanfs/node`）直接顶。

**结果**：

| 检查 | 结果 |
|---|---|
| `pnpm audit --prod` | **No known vulnerabilities found** |
| `pnpm audit`（全口径，含 dev） | **No known vulnerabilities found** |
| `typecheck` / `lint --max-warnings=0` / `test` / `build` | 全过（20 tests） |
| 产物对拍 | 42/42 页可见文本一致、引用 1275 条 0 悬空、**CSS 字节完全相同**（195054 字节 / 2 个文件） |

最后一行是这次的重点：改了 postcss、brace-expansion 这些构建期工具之后，**发出去的产物一个字节没变**，
说明这批 override 只是把工具链换到修补版，没有连带改动 UI。

**唯一留着的一条**：`path-to-regexp@6.3.0`，来自 `msw`（它声明 `^6.3.0`，而 6.x 线到 6.3.0 就断了、
修补要从 7.0.0 起，跨大版本会改掉 msw 的路由匹配语义）。msw 本地最新版 2.15.0 仍然钉 `^6.3.0`，
所以这条路只有两个出口：等 msw 换依赖，或者接受它——注意它是 **dev-only**，`pnpm audit`
甚至不为它报任何东西（`msw` 在本仓库 `src/` 里零引用，它进树是因为 shadcn 真的依赖它、
外加 `@vitest/mocker` 把它当 peer）。

## 10. overrides 的家在 `pnpm-workspace.yaml`，不在 `package.json`（2026-09-16）

§7–§9 的 overrides 一开始写在 `package.json` 的 `pnpm.overrides` 里。它本地完全有效——
仓库钉的 pnpm **10.15.0** 照读不误，锁文件里也老老实实记着那 23 条。但 Dependabot 的 web
版本更新组**连续失败**，16 个直接依赖全部报 `unknown_error`，日志里是
`Dependabot::SharedHelpers::HelperSubprocessFailed`。

根因在它自己的输出里：

> `[WARN] The "pnpm" field in package.json is no longer read by pnpm. The following keys were ignored: "pnpm.overrides".`

更新器里的 pnpm 比仓库钉的这个新。它按"没有 overrides"重算锁文件，而锁文件里带着那 23 条，
两边对不上，整组就崩了——于是"把告警交给 Dependabot"这件事被我自己的配置反手掐掉了。

**处置**：把 overrides 从 `package.json` 的 `pnpm.overrides` 搬到 `pnpm-workspace.yaml` 的
`overrides:`（v10.6 起的新家，这个文件本来就放着 `onlyBuiltDependencies`），两个版本都读这里。

**证据**：搬完之后 `pnpm install` 与 `install --frozen-lockfile` 都只说
`Lockfile is up to date`，**锁文件一个字节没变**——是等价的搬家，不是改解析；
解析结果仍是 `hono@4.13.8` / `mermaid@11.17.2` / `dompurify@3.4.15` / `fast-uri@3.1.8`。

**教训**：这个仓库钉的 pnpm 版本不是唯一的消费者，Dependabot 容器里那个也算一个。
配置文件要写在**两端都认**的位置，否则会出现"本地绿、机器人崩"这种最难查的组合。

## 11. 门

web 侧改动仍受 `web-test.yml`（typecheck + lint --max-warnings=0 + test）与
`.githooks/pre-commit` 约束 —— 升级依赖也不例外。Go 侧对应 `go-test.yml`（含 `-race`）；
`govulncheck` 已按 §8.1 接上。
