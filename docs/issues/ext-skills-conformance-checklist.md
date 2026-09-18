# 对照清单：ext-skills 规范 ↔ 我们的出口实现

**状态**：清单（实现未开始） · **日期**：2026-09-18
**规范来源**：`~/Project/tokenaissance/ext-skills`（`specification/stable/skills.mdx`，扩展 ID
`io.modelcontextprotocol/skills`，对应 SEP-2640，基线协议 `2026-07-28`）
**用途**：cloud MCP 出口实现"兼容协议"时的验收条目。每行都能在规范里找到出处，
最后一列写清我们自己的验收方式，避免开工时再翻规范。

**前置**：这份清单只覆盖 **Server 侧**（我们把云端 skill 发出去）。host 侧（消费别人的 server）
是另一份提案 `mcp-skills-fastagent.md`。

## A. 协商与声明

| # | 要求（规范小节） | 我们要做的 | 验收 | 现状 |
| :-- | :--- | :--- | :--- | :--- |
| A1 | 必须同时声明 `resources` 能力与 `capabilities.extensions["io.modelcontextprotocol.skills"]`；`directoryRead: true` 才可实现目录读取（Capability Negotiation） | 在 `server/discover` 与 `initialize` 两条握手里都带上（我们已经带上 capabilities.extensions） | 客户端握手应答里能看到该键 | ✅ 已声明 |
| A2 | 声明了扩展就**必须**实现 `skills/list` 与 `skills/get` | 实现两个方法 | conformance `sep-2640-skills-enumeration` | ⏳ 待做 |
| A3 | 声明 `directoryRead: true` 就必须实现 `resources/directory/read`；没声明时按未知方法处理 | 实现或先不声明 | conformance `sep-2640-skills-directory` | ⏳ 待做 |

## B. 条目构造（`Skill` entry）

| # | 要求 | 我们要做的 | 验收 | 现状 |
| :-- | :--- | :--- | :--- | :--- |
| B1 | `resources` **完整**列出该 skill 所有文件（含 `SKILL.md` 自身），每项带 `size`；或字符串 `"dynamic"`（Skill Entries / Resources） | 逐文件 walk + sha256 + size | `sep-2640-skills-enumeration` 的完整性检查 | ✅ 实测（refunds 双文件、平台库多文件） |
| B2 | `digest` = `sha256:{64 位小写 hex}`，覆盖原始字节（Integrity） | pod 读原始字节算哈希（不做 `{baseDir}` 替换——那会破坏 digest）；**cloud 直接用 pod 的 digest，不重算也不在列表里传字节** | 篡改文件后必须校验失败 | ⏳ 单测已覆盖（篡改 → `verification_failed`）；实测 digest 形状与磁盘一致 |
| B3 | `frontmatter` 必须与 SKILL.md 逐字段一致，原样透传（Frontmatter） | 解析后原样序列化，不注入任何字段 | `sep-2640-skills-manifest` | ⏳ |
| B4 | 末段等于 frontmatter `name`，`SKILL.md` 显式出现在 URI 里（Resource Mapping） | 走 D4 的"入口治理 + 单一源命名" | `sep-2640-final-segment-equals-name` | ⏳ |
| B5 | 单 skill ≤ 512 文件 / 16 MiB；超限要说明原因（Limits） | entry 构造时预检，超限不进 listing 并进诊断 | 构造超限 fixture，确认被拒且有原因 | ⏳ |
| B6 | 结果带 `resultType: "complete"`，并给 `ttlMs` / `cacheScope`（Listing / Getting） | 与 D9/SEP-2549 同一处理（`tools/list` 已做） | 应答字段检查 | ✅ 实测（list: `resultType/skills/ttlMs/cacheScope`；get: `resultType/skill/ttlMs/cacheScope`） |
| B7 | `skills/list` 可为空或部分；`skills/get` 必须能回答它服务的每一个 skill（对方按 URI 取） | `get` 不依赖 listing | 取一个"不在 listing 里"的 skill | ✅ 实测（未知 URI → `-32602`，`get` 不依赖调用方先 list） |
| B8 | 嵌套 skill 的文件也算外层 skill 的 supporting files（Nested Skills） | walk 到底，不去重 | 构造嵌套 fixture | ⏳ |

## C. 读取与错误

| # | 要求 | 我们要做的 | 验收 | 现状 |
| :-- | :--- | :--- | :--- | :--- |
| C1 | 每个文件通过 `resources/read` 可读；本扩展不定义打包形式（Getting / Resources） | 复用现有读取路径，按 mimeType 区分文本与 blob | Inspector 手动读一次 | ✅ 实测（`refunds/notes.md` 49 字节，与磁盘逐字节相等且对上 entry 的 digest/size） |
| C2 | `skills/get` 的未知 URI、`resources/directory/read` 的非目录/不存在一律 `-32602`；内部错误 `-32603`（Error Handling） | 错误码按规范，不改造成 404/500 | 三个负例请求 | ✅ 实测（未知 skill / 未列出文件 → `-32602`；上游不可用 → `-32603` + 502，且**永不 401**） |
| C3 | 未列出的文件在被加载期间读到 = 校验失败（Acting Window 属 host；server 侧只需保证 listing 完整） | 由 B1 保证 | — | ⏳ |

## D. 我们自己的额外约定（规范之外，但已写进决策）

| # | 约定 | 出处 | 验收 |
| :-- | :--- | :--- | :--- |
| D1 | **诊断必须发声**：被拒发布的 skill（无名、超限、名字冲突）要出现在客户端能读到的应答里 | 出口设计 §6.2 的 O6 | 构造三类坏 fixture，确认有诊断 |
| D1a | 诊断的承载位置：`ListSkillsResult` 按规范只有 `skills`，所以诊断走 `result._meta["com.tokenaissance/skills/unpublishable"]`（规范：附加信息用 `_meta` + 自己的反域名前缀） | 本轮实测修正（原方案放在顶层成员上） | ✅ 实测：`no-frontmatter` / `orphan-dir` 两条带原因出现在 `_meta` 里，顶层成员仍只有规范定义的那四个 |
| D2 | **工具兜底层先于扩展**：`list_skills` / `read_skill` 对协商到 2025-xx 的客户端可用 | 决策 D10（实测两个客户端只调 `tools/list`） | Codex/Claude Code 真机调用 |
| D3 | **只发原始字节**：不做 `{baseDir}` 替换；依赖它的 skill 进诊断 | 出口设计 §4 | 含 `{baseDir}` 的 fixture |
| D4 | **平台公共 skill 默认包含**，`Gated` 照发（`requires` 随 frontmatter 透传） | 决策 D2 / D3 | listing 里能看到这两类 |

## E. 验收方式汇总

1. **规范一致性**：`npx @modelcontextprotocol/conformance server --url <endpoint> --scenario sep-2640-skills-manifest | -enumeration | -directory`
   —— **2026-09-18 实测：该套件（0.1.16）尚无任何 SEP-2640 skills 场景**（`list` 只有 base 协议到 2025-11-25 的场景），且 server 模式无法携带 bearer（不会走 401→OAuth 发现），所以这一条现在**跑不了**。在套件补上场景之前，规范一致性只能靠"实测 + 单测"两条腿，见下。
2. **真机**：MCP Inspector（人工读一次）+ Codex CLI / Claude Code（工具兜底路径）
3. **诊断**：D1 的三类坏 fixture
4. **回归**：现有 UT 全绿 + e2e 与基线失败集合一致

## F. 本轮（2026-09-18）cloud ↔ 真实 pod 的实测记录

做法：本机用 `feat/mcp-skills-egress` 构建 fastagent（隔离 `FASTAGENT_HOME`，agent 放一个正常 skill + 两个坏 fixture），cloud dev 指向它，凭据临时换成本地 key（用完按密文快照还原）。

- `skills/list` 200：平台库 + agent skill 都在；entry 的 digest/size 与磁盘一致
- `skills/get` 200；`resources/read` 字节与磁盘逐字节相等
- 负例：未列出文件、未知 skill → `-32602`；上游不是 fastagent API（200 + `text/html`）→ **502**，此前是框架的 HTML 500
- 三个真实缺陷正是这一步发现的，均已修：skills 读取没有错误边界；cloud 适配器仍按"列表带 base64 字节"解析（pod 早已改成只发 digest，见 `internal/skills/catalog.go`）；pod 的 `unpublishable` 被整段丢弃
