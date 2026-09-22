# 对照清单：ext-skills 规范 ↔ 我们的出口实现

**状态**：清单（实现未开始） · **日期**：2026-09-18
**规范来源**：[tokenaissance/ext-skills › specification/stable/skills.mdx](https://github.com/tokenaissance/ext-skills/blob/main/specification/stable/skills.mdx)（本地检出常见于 `~/Project/tokenaissance/ext-skills`；扩展 ID
`io.modelcontextprotocol/skills`，对应 SEP-2640，基线协议 `2026-07-28`）
**用途**：cloud MCP 出口实现"兼容协议"时的验收条目。每行都能在规范里找到出处，
最后一列写清我们自己的验收方式，避免开工时再翻规范。

**前置**：这份清单只覆盖 **Server 侧**（我们把云端 skill 发出去）。host 侧（消费别人的 server）
是另一份提案 `mcp-skills-fastagent.md`。

## A. 协商与声明

| # | 要求（规范小节） | 我们要做的 | 验收 | 现状 |
| :-- | :--- | :--- | :--- | :--- |
| A1 | 必须同时声明 `resources` 能力与 `capabilities.extensions["io.modelcontextprotocol.skills"]`；`directoryRead: true` 才可实现目录读取（Capability Negotiation） | 在 `server/discover` 与 `initialize` 两条握手里都带上（我们已经带上 capabilities.extensions） | 客户端握手应答里能看到该键 | ⚠️ 2026-09-22 修正：原来"✅ 实测（握手返回 `resources: {}` + 扩展键）"是用**能容忍信封形状**的工具看的——真客户端根本不接受那个信封（见 A5）。扩展键本身仍在，**要求改成：能力键 + 合法信封一起才算过** |
| A5 | 两条握手的信封不同：`server/discover`（2026-07-28，SEP-2575）是 `resultType: 'complete'`；legacy 客户端的 `initialize` 必须回一个合法 `InitializeResult`，其中 **`protocolVersion` 是必填** | 两条握手各自成形；声明的版本要能被客户端在后续请求的 `MCP-Protocol-Version` 里引用（2025-11-25 起该 header 强制） | 真客户端能完成握手并调到工具；单测断言 `protocolVersion` 存在且 `resultType` 不在 `initialize` 的结果里 | ✅ 2026-09-22：真机先红（codex-cli 0.154.0-alpha.6.2：`expect initialized result, but received …`，一条工具都调不到），修后钉住——cloud `mcp-surface-e2e.test.ts` 驱动真实 dispatcher 走 challenge → 两份 discovery → initialize → notification → tools/list → 两个 tools/call → resources/list → resources/read；反证：握手改回共用形状 ⇒ 3 条红。可重复的真机检查：cloud `scripts/mcp-egress-live-check.sh` |
| A2 | 声明了扩展就**必须**实现 `skills/list` 与 `skills/get` | 实现两个方法 | conformance `sep-2640-skills-enumeration` | ✅ 已实现（该套件无此场景，见 E1） |
| A3 | 声明 `directoryRead: true` 就必须实现 `resources/directory/read`；没声明时按未知方法处理 | **本轮改为不声明**：方法未实现，声明了就等于给客户端一个假承诺；实现目录读取时再把 flag 加回来 | `resources/directory/read` 必须落到未知方法分支 | ✅ 已撤回声明（实测扩展键为 `{}`）+ 单测锁住"声明 ↔ 实现" |
| A4 | 声明 `resources` 能力就必须响应 `resources/list`（base Resources："Servers that declare the `resources` capability **MUST** respond to `resources/list`"）；扩展又强制我们必须声明该能力 | 由已发布 entry 投影出资源列表（`resourcesFromEntries`），不第二次扫盘 | `resources/list` 的 URI 集合 == pod 的文件集合 | ✅ 实测（27 条，与 pod 文件集合完全相等；无 token → 401；带 `resultType/resources/ttlMs/cacheScope`） |

## B. 条目构造（`Skill` entry）

| # | 要求 | 我们要做的 | 验收 | 现状 |
| :-- | :--- | :--- | :--- | :--- |
| B1 | `resources` **完整**列出该 skill 所有文件（含 `SKILL.md` 自身），每项带 `size`；或字符串 `"dynamic"`（Skill Entries / Resources） | 逐文件 walk + sha256 + size | `sep-2640-skills-enumeration` 的完整性检查 | ✅ 实测（refunds 双文件、平台库多文件） |
| B2 | `digest` = `sha256:{64 位小写 hex}`，覆盖原始字节（Integrity） | pod 读原始字节算哈希（不做 `{baseDir}` 替换——那会破坏 digest）；**cloud 直接用 pod 的 digest，不重算也不在列表里传字节** | 篡改文件后必须校验失败 | ⏳ 单测已覆盖（篡改 → `verification_failed`）；实测 digest 形状与磁盘一致 |
| B3 | `frontmatter` 必须与 SKILL.md 逐字段一致，原样透传（Frontmatter） | 解析后原样序列化，不注入任何字段 | `sep-2640-skills-manifest` | ⏳ |
| B4 | 末段等于 frontmatter `name`，`SKILL.md` 显式出现在 URI 里（Resource Mapping） | 走 D4 的"入口治理 + 单一源命名" | `sep-2640-final-segment-equals-name` | ⏳ |
| B5 | 单 skill ≤ 512 文件 / 16 MiB；超限要说明原因（Limits） | entry 构造时预检，超限不进 listing 并进诊断 | 构造超限 fixture，确认被拒且有原因 | ✅ 2026-09-21：规则在 `buildSkillEntry`（cloud），**两个消费面各一条投递点见证**——MCP 侧 `skills-list-chain.test.ts`，面板侧 cloud `fastagent-proxy-route.test.ts`（pod 夹具里放一个 20 MiB 的 skill ⇒ `egress_too_large` 进 `unpublishable`、该 skill 不再出现在 `skills`）。此前面板这一格是空的：它读的是 pod 的目录，而 pod 不知道这两条限制（登记册第 40 行）。句子里的数字与常量由 `skill-diagnostics.test.tsx` 对齐（写成 1024 会红）。**够得着吗（实测）**：pod 上传侧的上限是"压缩包 64 MiB"（`internal/setup/skill_install.go` 的 `maxUploadSize`，只比较压缩后的大小），既不校验解压后大小、也没有文件数上限 ⇒ 一个合法上传的包就能超过这两条出口限额（注册表安装、git 克隆那条路更是完全没有上限），所以这不是"假设 pod 违约"的防御路径 |
| B6 | 结果带 `resultType: "complete"`，并给 `ttlMs` / `cacheScope`（Listing / Getting） | 与 D9/SEP-2549 同一处理（`tools/list` 已做） | 应答字段检查 | ✅ 实测（list: `resultType/skills/ttlMs/cacheScope`；get: `resultType/skill/ttlMs/cacheScope`） |
| B7 | `skills/list` 可为空或部分；`skills/get` 必须能回答它服务的每一个 skill（对方按 URI 取） | `get` 不依赖 listing | 取一个"不在 listing 里"的 skill | ✅ 实测（未知 URI → `-32602`，`get` 不依赖调用方先 list） |
| B8 | 嵌套 skill：文件算外层的 supporting files，同时它自己是独立 entry，URI 带外层前缀（Nested Skills） | pod 递归扫描、`Path = 相对层的路径`；cloud 从 entry 反解 skill 路径与文件路径（不再按斜杠猜） | 构造嵌套 fixture，两侧都读一遍 | ✅ 已实现并实测：`acme/billing` 作为独立 entry 出现在 listing/resources；外层仍把嵌套的 SKILL.md 列为 supporting file；`skill=acme/billing` 可取文件、`skill=billing` 与 `../` 一律 404 |

## C. 读取与错误

| # | 要求 | 我们要做的 | 验收 | 现状 |
| :-- | :--- | :--- | :--- | :--- |
| C1 | 每个文件通过 `resources/read` 可读；本扩展不定义打包形式（Getting / Resources） | 复用现有读取路径，按 mimeType 区分文本与 blob | Inspector 手动读一次 | ✅ 实测（`refunds/notes.md` 49 字节，与磁盘逐字节相等且对上 entry 的 digest/size） |
| C2 | `skills/get` 的未知 URI、`resources/directory/read` 的非目录/不存在一律 `-32602`；内部错误 `-32603`（Error Handling） | 错误码按规范，不改造成 404/500 | 三个负例请求 | ✅ 实测（未知 skill / 未列出文件 → `-32602`；上游不可用 → `-32603` + 502，且**永不 401**） |
| C3 | 未列出的文件在被加载期间读到 = 校验失败（Acting Window 属 host；server 侧只需保证 listing 完整） | 由 B1 保证 | — | ⏳ |

## D. 我们自己的额外约定（规范之外，但已写进决策）

| # | 约定 | 出处 | 验收 |
| :-- | :--- | :--- | :--- |
| D1 | **诊断必须发声**：被拒发布的 skill（无名、超限、名字冲突）要出现在客户端能读到的应答里 | 出口设计 §6.2 的 O6 | 构造三类坏 fixture，确认有诊断。**第二面**：dashboard 面板也要有（设计 §6.2 的要求与 O7 同源——每个消费者各需一份见证）；面板由 cloud 的 `skillCatalogView` 作答，与 MCP 侧出自同一份分区，因此两侧不可能对"客户端能拿到哪些 skill"给出不同答案 |
| D1a | 诊断的承载位置：`ListSkillsResult` 按规范只有 `skills`，所以诊断走 `result._meta["com.tokenaissance/skills/unpublishable"]`（规范：附加信息用 `_meta` + 自己的反域名前缀）；**每条拒绝连同稳定 `code` 一起送**（pod 的 `no_name` / `name_mismatch` / `no_files` / `shadowed_by_layer`，出口自己那几条 `egress_` 前缀），句子仍是主载荷 | 本轮实测修正（原方案放在顶层成员上） | ✅ 实测：`no-frontmatter` / `orphan-dir` 两条带原因出现在 `_meta` 里，顶层成员仍只有规范定义的那四个；2026-09-21 起 code 也随行（cloud 侧投递点见证 `skills-list-chain.test.ts`） |
| D2 | **工具兜底层先于扩展**：`list_skills` / `read_skill` 对协商到 2025-xx 的客户端可用 | 决策 D10（实测两个客户端只调 `tools/list`） | Codex/Claude Code 真机调用 | ✅ 已实现并实测（`tools/list` 两个工具、`list_skills` 文本 + 拒绝原因、`read_skill` 字节与磁盘逐字节一致、未知工具 `-32602`） |
| D3 | **只发原始字节**：不做 `{baseDir}` 替换；**manifest 里**依赖它的 skill **照发**但必须进诊断，且两个消费面都要——dashboard 面板与 MCP 客户端的应答（`_meta["com.tokenaissance/skills/warnings"]` + `list_skills` 文本），因为读到字面量的那个读者看不见面板（形式化：**O7**，08 §10.7）。触发条件是 manifest 带 token（只有 `SKILL.md` 被替换，别处没有读者差异）；其余携带者只作为 `files` 证据列出 | 出口设计 §4 | 含 `{baseDir}` 的 fixture（manifest 一条 + 只落在随包脚本的一条）；2026-09-21：客户端面此前整段缺失（cloud 适配器把 `warnings` 与 `code` 一起丢了），已修 + 反证；触发条件收窄 + 反证见登记册第 39 行 |
| D4 | **平台公共 skill 默认包含**，`Gated` 照发（`requires` 随 frontmatter 透传） | 决策 D2 / D3 | listing 里能看到这两类 |

## E. 验收方式汇总

1. **规范一致性**：`npx @modelcontextprotocol/conformance server --url <endpoint> --scenario sep-2640-skills-manifest | -enumeration | -directory`
   —— **2026-09-18 实测：该套件（0.1.16）尚无任何 SEP-2640 skills 场景**（`list` 只有 base 协议到 2025-11-25 的场景），且 server 模式无法携带 bearer（不会走 401→OAuth 发现），所以这一条现在**跑不了**。在套件补上场景之前，规范一致性只能靠"实测 + 单测"两条腿，见下。
2. **真机**：MCP Inspector（人工读一次）+ Codex CLI / Claude Code（工具兜底路径）。2026-09-22 起，真机这一步有可重复脚本：cloud `scripts/mcp-egress-live-check.sh`（它先断言 401 + PRM，再真跑 `codex mcp add`/`login` 与一次 `list_skills`，握手被拒就报红并指出缺 `protocolVersion`）。**教训**：这套清单原来把"握手应答里有扩展键"记成实测通过，而真客户端拒绝的是信封形状——凡是"实测"，必须写明**用什么客户端**测的。
3. **诊断**：D1 的三类坏 fixture
4. **回归**：现有 UT 全绿 + e2e 与基线失败集合一致

## F. 本轮（2026-09-18）cloud ↔ 真实 pod 的实测记录

做法：本机用 `feat/mcp-skills-egress` 构建 fastagent（隔离 `FASTAGENT_HOME`，agent 放一个正常 skill + 两个坏 fixture），cloud dev 指向它，凭据临时换成本地 key（用完按密文快照还原）。

- `skills/list` 200：平台库 + agent skill 都在；entry 的 digest/size 与磁盘一致
- `skills/get` 200；`resources/read` 字节与磁盘逐字节相等
- 负例：未列出文件、未知 skill → `-32602`；上游不是 fastagent API（200 + `text/html`）→ **502**，此前是框架的 HTML 500
- 三个真实缺陷正是这一步发现的，均已修：skills 读取没有错误边界；cloud 适配器仍按"列表带 base64 字节"解析（pod 早已改成只发 digest，见 `internal/skills/catalog.go`）；pod 的 `unpublishable` 被整段丢弃

同一套栈上补测工具兜底层（真实客户端唯一会走的那条路）：

- `tools/list` 200 → `list_skills` / `read_skill`（都带 `readOnlyHint`）；无 token → 401 + 挑战
- `tools/call list_skills` → 4 个已发布 skill 的文本清单 + 2 条拒绝原因（D1 在工具面同样发声）
- `tools/call read_skill` → `SKILL.md` 与磁盘逐字节一致；`path: notes.md` 读到支持文件
- 未知 skill → `isError: true` 且列出已发布名字；未知工具名 → `-32602`（这是请求错，不是读取失败）
- `resources/list` → 27 条，URI 集合与 pod 目录**完全相等**（同一份 entry 投影，不第二次扫盘）

嵌套 skill 的双端实测（同一套栈，pod 侧加一个 `acme/billing` fixture）：

- pod：catalog 里 `acme/billing` 是独立 entry（`acme` 本身没有 SKILL.md，所以不发布）；`skill=acme/billing&path=policy.md` → 200；`skill=billing` → 404（叶子名不再是键）
- cloud：`skills/list` 与 `resources/list` 都带 `acme/billing`；`read_skill billing` 的字节与磁盘逐字节一致；`skills/get skill://acme/billing/SKILL.md` 返回 2 个 resource；叶子名 URI `skill://billing/SKILL.md` → `-32602`（没有任何 listing 发布过它）

一句提醒（避免下次再踩）：`MCP_SPEC` 那组引用里原本有两个 404（`/basic/utilities/caching`、`/basic/extensions`），
handshake 那条还指到会 308 的页。2026-07-28 的实际布局是 `server/discover`、`server/utilities/caching`、
`server/resources`、`server/tools`，扩展在 `/docs/extensions/overview`（不在版本路径下）。引用注释只有在**打得开**时才起作用。
