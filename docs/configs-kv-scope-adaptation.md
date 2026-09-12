# configs 数据域：blob / configs_kv 双写与四层 scope 适配

> 状态：fork 已落地。本文档是这个数据域的**设计记录 + 实现对照**：第一节
> 「现状」描述当前代码实际在做的事（改代码先改这一节），后面按时间保留每轮
> review 的推理过程，最后一节是给**上游 fastclaw** 的 PR 提案。历史小节里的
> 结论如果与「现状」冲突，以「现状」为准。

## 现状：实现对照（权威）

### 四层映射与依赖方向

| 层 | 本仓库的落点 | 谁依赖它 |
|---|---|---|
| Entities | `internal/config` 的 typed 结构体（`Config` / `ProviderConfig` / `ChannelConfig` / `AgentDefaults` …）与四层所有权语义 | 被所有层依赖，自己不依赖任何 IO |
| Use Cases | `internal/scope`：解析与合并（`Setting` / `ExactSetting` / `Providers` / `Channels` / `GetValues` / `SettingInto`）——「哪一层的行胜出、值怎么投影」 | `setup` / `gateway` / `agentcli` |
| Interface Adapters | `internal/kvkeys`（键形状编解码）、`store.ConfigValue`（value + value_kind 编解码）、`store.JSONToMap`（blob 解码）、`scope.Save*` 的双写编排、`setup` 的 `scope` 字符串 ↔ `(user_id, agent_id)` 转换 | `scope` / `setup` |
| Frameworks & Drivers | `internal/store/database.go` 的 SQLite / PostgreSQL、表结构、迁移；HTTP / CLI 入口 | 最外层，随时可换 |

依赖方向只有向内一条：`setup → scope → store → database/sql`，`config` 在最里
层不依赖任何人。**store 不 import scope**（否则成环），所以「所有权 → KV
scope」这条映射规则放在 store（`store.KVScopeFromOwnership`），由 scope 复用。

### 存储面

```
configs             id, kind, scope(标签), user_id, agent_id, name,
                    enabled, credential_key, data(TEXT/JSON), 时间戳
                    UNIQUE(kind, user_id, agent_id, name)
                    INDEX(kind, user_id, agent_id), INDEX(kind, credential_key)

configs_kv          kind, scope, scope_id, name, value(TEXT), value_kind(TEXT)
                    PRIMARY KEY(kind, scope, scope_id, name)
                    INDEX(kind, scope, scope_id)
```

- **kinds**：`provider` / `setting` / `channel` / `plugin_enabled`（`store.KindPluginEnabled`）。
- **scopes**：`system` / `user` / `agent` / `user-agent`；`scope_id` 分别是
  `""` / `userID` / `agentID` / `"<userID>/<agentID>"`。格式由
  `store.KVUserAgentScopeID` 定义——删除路径用 `<user>/%` 与 `%/<agent>` 的
  LIKE 模式匹配它，所以分隔符是存储契约。
- **谁进 configs_kv**：`provider`、`setting`、`plugin_enabled` 双写；
  **`channel` 不进**（它有自己的 `channels` 表，`migrateChannelsFromConfigs`
  负责搬迁）。迁移 `migrateConfigsToKV` 只回填 `provider` + `setting`。

### 写路径

```
SaveSetting / SaveProvider / SaveAgentPluginEnabled       ← 唯一写入口
  ├─ blob 侧：ConfigRecord{Data map[string]interface{}} → SaveConfig → configs.data
  └─ KV 侧：flattenJSONToKV → store.EncodeConfigValue(leaf) → SetConfigValue
              └─ 先 DeleteConfigPrefix，再逐行 UPSERT（非事务）
```

值的类型在**写侧唯一确定一次**（`EncodeConfigValue`），此后读侧不再推断：

```go
// 写：Go 值 → (文本, tag)。数字保持字面量（json.Number）而不是 float64。
case json.Number: return ConfigValue{Value: t.String(), Kind: ValueKindNumber}
case float64:     return encodeFloat(t, 64)   // json.Marshal 的字节，不是 %g

// 读：tag → JSON 值，数字还原成 json.Number，由最后一跳按目标字段解析。
case ValueKindNumber: return json.Number(v.Value)
```

blob 侧同理走 `store.JSONToMap` / `store.ValueToMap`（`Decoder.UseNumber()`），
所以 `configs.data` 里的数字在 map 里也是字面量。两条路径的保真度因此一致。

写侧还挡掉一种**结构上无法表示**的输入：provider 名必须匹配
`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`（`scope.ValidateProviderName`，`SaveProvider`
入口校验，HTTP 回 400）。理由见「key 正确性」与「review 第三轮修复」。

### 读路径

**优先级：blob 权威，KV 兜底。** 只有当链路上四层都没有 blob 行时，才去读
镜像（镜像可能被直接写过、也可能是历史行）。

| 入口 | 合并层 | KV 兜底 |
|---|---|---|
| `Setting` → `SettingInto` | system → user → agent → user-agent，顶层 key 字段级合并 | 有（`GetValues` + `kvToSettingMap`，规则 = `ConfigValue.Decode`） |
| `ExactSetting` / `UserScopeSetting` | 单层，不合并 | 有（同上） |
| `Providers` | 四层，内层整行替换 | 有（`kvValsToProviders`，规则 = `kvFieldMap`） |
| `AgentScopeProviders` / `UserScopeProviders` | 单层 | 有 |
| `AgentPluginEnabled` | agent 层单行 | 有 |
| `BatchSettings` | 仅 system + user 两层 | 有，但只对**没有 blob 行**的 namespace 逐个走 `Setting`（面板专用，N 个 namespace 合并成 2 次查询；补兜底只影响 blob 缺席的 namespace） |
| `Channels` | 四层，disabled 行擦除外层 | **无，且刻意如此**：channel 行从不进 KV（见「存储面」），`Channels` 没有可回落的镜像。`TestChannelsAreNotMirroredInKV` 钉住这个前提 |

未标注行（`value_kind = ''`，即 value_kind 之前写下的历史行）在两条读路径上
的规则**不同**，这是历史行为，不是遗漏：

| 读路径 | 未标注行的规则 | 为什么 |
|---|---|---|
| settings（`kvToSettingMap`） | `ConfigValue.Decode` → `decodeLegacyValue`：整串数字→number、`true/false`→bool、`{…}`/`[…]`→结构 | 沿用 tag 之前的猜测，行为与改动前一致 |
| providers（`kvFieldMap`） | `ConfigValue.DecodeLegacyStructure`：只解 `{…}`/`[…]` 结构，标量一律按文本 | provider 字段更常是自由文本（全数字的 `api_key` 猜成 number 会让字段消失） |

带 tag 的行两条路径都只认 tag，不再猜测。

### key 正确性

`configs_kv` 的行名是「命名空间前缀 + 点号段」，段分两类，规则集中在
`internal/kvkeys`：

- **结构体字段段**：写 snake_case、读 camelCase（`memory.auto_persist` ↔
  `memory.autoPersist`）。这对转换只对字段名成立。
- **数据 key 段**（分类 id、provider 名、skill id、team id、env 变量名）：两个
  方向都原样保留。两条机制覆盖它们：`dataPaths` 精确匹配一段
  （`tools.categories.*`、`tools.providers.*`、`skills.entries.*`、
  `plugins.entries.*`、`plugins.enabled.*`、`teams.*`），`openPaths` 是前缀规则、
  任意深度（`tools.providers.*.options`、`skills.entries.*.env`、
  `plugins.entries.*.config` 之下的所有段）。后者必需：精确表只能覆盖第一层，
  嵌套的 `{"headers":{"X_API_Key":…}}` 会被折叠成 `xAPIKey`。

前缀约定：provider 行是 `<provider 名>.`，setting 是 `<namespace>.`，唯一的
例外是 `agents.defaults` → `agent.`（历史遗留，`kvPrefixForNamespace` 是唯一
定义处）。所有前缀匹配走 `likePrefixPattern`（转义 `\`、`%`、`_` + `ESCAPE '\'`）
——`_` 在 LIKE 里是通配符，provider 名里的下划线曾扫到别人的行。

`kvValsToProviders` 用**第一个点**切 provider 名，所以带点的 provider 名会被
切错。写侧因此不接受这种名字：`scope.ValidateProviderName`（`SaveProvider`
的入口检查，HTTP 侧回 400）只允许 `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`，理由见
「review 第三轮修复」。

### 不变式

1. 值类型在写侧确定一次；读侧不猜（未标注行除外，规则见上表）。
2. blob 是权威、KV 是兜底；任一读入口只有在链条上没有 blob 行时才碰镜像。
3. 数据 key 段两个方向都不转换，`kvkeys` 是唯一规则来源。
4. 四层所有权 → (scope, scope_id) 只有 `store.KVScopeFromOwnership` 一处定义。
5. 新写入的 configs_kv 行必带 `value_kind`（生产路径只经 `EncodeConfigValue`）。

违反 1 的症状是「值变形/丢字段」，违反 2 的症状是「面板正常、运行时不生效」，
违反 3 的症状是「配置写进去了但运行时查不到」（`web_search` 事件），
违反 4 的症状是「另一个 agent 读到了别人的 key」。四类都各有一组回归测试。

## 背景：fork 的四层 scope 模型

fork 的配置按 `(user_id, agent_id)` 所有权分成四层（见 `internal/scope/scope.go`
头注释），从全局到局部：

```
system          (user='',  agent='')   全局默认
user            (user=X,  agent='')    某用户的个人配置
agent           (user='', agent=Y)     某 agent 的配置
per-(user,agent) (user=X,  agent=Y)    某用户在某 agent 上的专属配置
```

读路径按 `system → user → agent → per-(user,agent)` 逐层覆盖，**最内层 wins**。
这四层是 fork 多租户架构的基础：一个用户可以用多个 agent，每个 agent 有独立
的 provider key / 模型 / 渠道绑定，互不泄漏。

## 问题：上游 configs_kv 只实现三层

上游 `refactor(configs): flatten JSON blobs into configs_kv`（fcb63be）新增的
`kvScopeFromOwnership` 把 per-(user, agent) 折叠到 user 层：

```go
// 上游（3 层）
func kvScopeFromOwnership(userID, agentID string) (scope, scopeID string) {
    switch {
    case userID != "" && agentID != "":
        return "user", userID   // ← 折叠：丢 agent 维度
    case userID != "":
        return "user", userID
    case agentID != "":
        return "agent", agentID
    default:
        return "system", ""
    }
}
```

`(X, Y)` 与 `(X, '')` 都落 `(user, X)`，带来两个具体故障：

### 故障 1：跨 agent key 泄漏

一个用户有 agent A、B。为 A 绑定 provider key（per-(user,A) 层）→ 落到
`(user, X)`。之后**任何**该用户的 agent（含 B）在 `GetValue` 读路径都会命中
这个 key。**A 的私有 key 泄漏给 B**——多租户下这是数据泄露。

### 故障 2：迁移时同用户多 agent key 碰撞丢数据

`migrateConfigsToKV` 把存量四层 configs 行扁平化到 configs_kv。同一用户下
agent A、B 各自有 `openai.api_key`（per-(user,A) 与 per-(user,B)）→ 扁平化后
**都写到 `(user, X)` 同一行**，last-write-wins，先写的一个静默丢失。

## fork 的适配（最小改动）

保留 per-(user,agent) 作为 configs_kv 的独立 scope，`scope_id = userID + "/" + agentID`。

### 1. 新常量 `internal/scope/scope.go`

```go
const (
    System    = "system"
    User      = "user"
    Agent     = "agent"
    UserAgent = "user-agent"   // 新增
)
```

### 2. `kvScopeFromOwnership` 加第四层

上游那个函数在 fork 里搬进了 store 并改名 `KVScopeFromOwnership`（迁移要用，
而 store 不能 import scope），`scope.kvScopeFromOwnership` 现在只是转发：

```go
func KVScopeFromOwnership(userID, agentID string) (scope, scopeID string) {
    switch {
    case userID != "" && agentID != "":
        return "user-agent", KVUserAgentScopeID(userID, agentID) // 新增，不再折叠
    case userID != "":
        return "user", userID
    case agentID != "":
        return "agent", agentID
    default:
        return "system", ""
    }
}
```

### 3. 读路径加最内层

`GetValues`（前缀扫描）在 `agent` 层之后、`user` 层之内增加 per-(user,agent)
层查询，语义 = innermost wins。当时还有一个单值的 `GetValue` 做同样的四层
点查，它后来随「blob 权威」那一轮退场了（现在四层点查走 `GetConfigByName`，
前缀扫描走 `GetValues`；见「现状」的读路径表）：

```go
// GetValues：system → user → agent → user-agent，后者覆盖前者的同名行
if userID != "" && agentID != "" {
    if err := merge(UserAgent, store.KVUserAgentScopeID(userID, agentID)); err != nil {
        return nil, err
    }
}
```

### 4. 迁移映射 `internal/store/database.go`

这一层映射现在只有一处定义（`store.KVScopeFromOwnership`，`scope` 与迁移共用）：

```go
func KVScopeFromOwnership(userID, agentID string) (scope, scopeID string) {
    switch {
    case userID != "" && agentID != "":
        return "user-agent", KVUserAgentScopeID(userID, agentID) // 不再折叠到 user 层
    case userID != "":
        return "user", userID
    case agentID != "":
        return "agent", agentID
    default:
        return "system", ""
    }
}
```

## 回归护栏（测试）

- `internal/scope/configs_kv_test.go` → `TestProviderPerUserAgentIsolation`：
  绑定 per-(user,A) provider key，断言 B（兄弟 agent）读不到、不落 user 层。
- `internal/store/configs_kv_test.go` → `TestMigrateConfigsToKV`：四层迁移后
  per-(user,agent) 落 `(user-agent, X/Y)` 独立行，不碰撞。
- `internal/setup/configs_kv_e2e_test.go` → `TestProviders_CloudPathE2E`：
  真实 handler 路径下兄弟 agent 不继承 agent-scope key。

## key 形状规则（数据 key vs 结构体字段）

写侧 `kvkeys.CamelToSnake` 只把 camelCase 折成 snake_case，已经是 snake_case 的
key 原样保留；但读侧 `kvkeys.SnakeToCamel` 对这类 key **不是它的逆**：
`CamelToSnake("webSearch")` 和 `CamelToSnake("web_search")` 都落到 `web_search`。
因此两个方向都不能对每个
点号段一律转换，否则会改写「数据 key」（分类 id、provider 名、skill id、team id、
env 变量名），而改写后的 key 静默失配运行时查询 ——
`gateway.registerAgentToolChains` 查 `cfg.Tools["web_search"]` 查不到，就只是
**不注册 web_search 工具**，全程没有任何报错。

规则集中在 `internal/kvkeys`（读 `RestoredSegment`、写 `StoredSegment`、
白名单 `dataPaths` + 任意深度前缀规则 `openPaths`），
`scope.dualWriteSettingKV` / `scope.kvToSettingMap` /
`store.migrateConfigsToKV` 共用同一份，不再各持一份实现：

- 结构体字段段（`memory.auto_persist` → `memory.autoPersist`、
  `objectstore.s3.access_key` → `.accessKey`）继续 snake_case→camelCase；
- 数据 key 段**两个方向都原样保留**，由两条机制覆盖：`dataPaths` 精确匹配一段
  （`tools.categories.*`、`tools.providers.*`、`skills.entries.*`、
  `plugins.entries.*`、`plugins.enabled.*`（每 agent 的插件开关行）、`teams.*`），
  `openPaths` 是前缀规则、覆盖任意深度
  （`tools.providers.*.options`、`skills.entries.*.env`、
  `plugins.entries.*.config` 之下）。

白名单需与带 map 字段的 config 结构体同步（`config.Config` /
`ToolProviderCfg.Options` / `SkillEntryCfg.Env` / `PluginsCfg.Entries` /
`PluginEntryCfg.Config`）。新增带 map 的 setting namespace 时要补一行，
`internal/kvkeys/kvkeys_test.go` 的 round-trip 表也要加一条。

回归测试：`internal/scope/configs_kv_test.go` → `TestKvToSettingMapPreservesDataKeys`
（数据 key 原样保留）、`TestKvToSettingMapCamelCasesFieldSegments`
（字段段仍转 camelCase）、`TestSettingCategoryDataKeyRoundTrip`
（dev 场景端到端：`SaveSetting` → `tools.categories.web_search.primary` →
`SettingInto` 得到 `web_search`）、`TestSettingAllCapsEnvKeyRoundTrip`
（`REPLICATE_API_TOKEN` 经 KV 往返不再丢大小写）；
`internal/store/configs_kv_test.go` → `TestMigrateConfigsToKV`
（迁移路径同样保留数据 key）。

修复前的历史行：数据 key 曾被写成小写蛇形（`…env.replicate_api_token`），
读出来就是 `replicate_api_token`——已是现有数据下最好的还原；重新保存一次
该 namespace 即会写回正确拼写（`dualWriteSettingKV` 先删前缀再写）。
`agents.defaults`（`agent.` 前缀）不是数据 key 路径，全大写 key 仍按
`TestSettings_AllCapsKeyDualWriteE2E` 的既有约定小写化，未改。

## EnsureAgent 的 agent 级工具链（云端 lazy-attach 路径）

`registerAgentToolChains` 只在 `loadUserSpace` 里调用过；`UserSpace.EnsureAgent`
（channel binding / public link / apikey 共享用户 / super_admin 浏览 —— 即云端
chatter 走的那条路）建完 agent 后从没调用它。web_search / image_gen / tts 都只在
有 chain 时才注册（`internal/agent/tools/*_chain`），所以这些 agent 上根本没有
web_search，模型只能回 “Unknown tool: web_search”，而 owner 自己的 web chat 却
能搜。修复：EnsureAgent 加 `registerAgentToolChains(toolConfigForAgent(...))`，
并按 `isForeign && shareModelConfig` 叠加 owner **user-scope** 的
`tools.providers` / `tools.categories`（用 `scope.UserScopeSetting`，只取用户层，
不把 owner 的 system 行盖到 viewer 的 user 行上）。

回归测试：`internal/gateway/tools_overlay_test.go` →
`TestEnsureAgentRegistersToolChains`（chatter 非 owner，仍注册到
`web_search`；未配置的 `tts` 不注册）、`TestToolConfigForAgentOwnerOverlay`
（owner user 行覆盖、system 行保留、调用方快照不被改写）。

## review 补充修复：bindings 信封 / 投影回退 / LIKE 前缀

同一轮 review 又找出三处「KV 影子表与 legacy blob 形状不一致」的爆点，已修：

1. **bindings 行形状 vs 运行时字段**。`bindings` namespace 存的是
   `{"list":[…]}`（`settingNamespaces.collect`），而 `config.Config.Bindings`
   是扁平切片。把整行 map 直接投影到 `&cfg.Bindings` 会报
   `cannot unmarshal object into Go value of type []config.Binding`：
   `assembleConfig` 直接返回错误 → **该用户整个 UserSpace 加载失败**
   （dashboard 侧 `loadUserConfig` 因为 `_ =` 吞掉错误，表现为 bindings 静默丢失）。
   现在读写都过信封 `config.BindingsPayload`，且只在行内确实有 list 时才覆盖
   调用方已有的 bindings。dev 库目前没有 bindings 行，属于潜伏问题。
   回归测试：`internal/gateway/bindings_row_test.go`。

2. **`parseKVValue` 的类型再推断会打断投影**（这一整类问题已由后面的
   `value_kind` 根治，此条保留作为历史记录）。KV 里存的是字符串，读回时
   `"123"` → number、`"true"` → bool、`"{…}"`/`"[…]"` → object/array。
   于是 `objectstore.s3.bucket = "123"` 这类「字符串字段装了数字样的值」会在
   `json.Unmarshal` 处失败并带走整个 namespace。`SettingInto` 现在在这种
   投影失败时退回 legacy blob（blob 保留精确 JSON 类型）并打
   `slog.Warn("configs_kv projection failed; served from the legacy blob")`。
   回归测试：`internal/scope/configs_kv_test.go` →
   `TestSettingIntoFallsBackToLegacyBlob`。

3. **LIKE 前缀不是字面量**。`_` 在 LIKE 里匹配任意单字符，所以
   `ListConfigValues(prefix="my_provider.")` 也会扫到 `myZprovider.`，
   `DeleteConfigPrefix` 会真的删掉别人的行。provider 名来自 API（只校验非空），
   前缀并不保证没有元字符。现在统一走 `likePrefixPattern`（转义 `\`、`%`、`_`）
   + `ESCAPE '\'`。回归测试：`internal/store/configs_kv_test.go` →
   `TestConfigsKVLIKEUnderscoreIsLiteral`。

## 读源优先级：blob 权威，configs_kv 兜底（51780fa）

原来的读路径是 **configs_kv 优先、blob 兜底**，这条规则把所有投影缺陷都
放大成了线上故障：投影会重新推断类型（`"123"` → number）、会改写嵌套数据 key、
无法表示空 map，而且 `dualWriteSettingKV` 是「先删前缀、再逐行写」的非事务
过程——只要 KV 有任意一行，`Setting` 就完全无视 blob，于是 namespace 静默丢掉
KV 没覆盖到的那些 key。`web_search` 事件正是这条链路的产物。

现在反过来：**legacy blob 是权威读源**（精确 JSON 类型、完整 key 集合，dashboard
本来就读它），configs_kv 只在 blob 没有对应行时兜底（直接写进镜像的行）。
查询次数不变（两侧都是每层一次点查/前缀扫），但运行时与面板现在看的是同一份
数据——「面板显示正常、运行时不生效」这一整类问题从读路径上消失。

涉及函数：`scope.Setting`、`scope.UserScopeSetting`、`scope.Providers`、
`scope.AgentScopeProviders`、`scope.UserScopeProviders`。
依赖 KV 数值优先的旧契约 `TestProvidersDualWriteReadsFromKV` 已按新语义
重写为 `TestProvidersDualWriteReadsFromBlob`。生产代码里没有任何「只写 KV」的
业务路径：`SetConfigValue` 的调用点只有双写本身（setting / provider /
plugin_enabled 三处）、`migrateConfigsToKV` 的回填，以及 mcp undo 游标。

投影侧同时补了两处：`kvkeys` 对「自由 map 容器」（`options` / `env` / `config`）
改用前缀规则，任意深度都算数据 key；provider 投影不再吞掉 `json.Unmarshal`
错误，且未标注的行走保守规则（`kvFieldMap`）：结构照解，标量按存储字符串还原。
带 `value_kind` 的行不走这条保守规则——写侧已经说明了类型，读侧直接采信
（见下文「值保真」与「现状」的未标注行对照表）。这里的「结构照解」在当时被
漏掉了（只保留标量那半条），`TestProvidersMirrorFallbackKeepsLegacyStructure`
现在钉住它。

## review 第二轮修复：agent 层可写但没人读 / plugins.enabled 形状（752e9d0）

上一节把读源翻成 blob 权威之后，又顺着「配置在 DB 里，运行时却说没有」这条线
做了一轮同类排查，修掉三处同因问题（P1/P2/P3）。

### P1a：`tools.providers` / `tools.categories` 的 agent 层从来没人读

CLI 明确支持 `fastagent tools category set --primary … --agent X`
（`cmd/fastclaw/cmd_tools.go`），写出的行也是标准的
`kind=setting, user_id="", agent_id=X, name=tools.categories`。但网关联配配置
时**从不传 agentID**：`assembleConfig` 只有两处调用（`userspace.go`
`loadUserSpace` / `EnsureAgent` 里的 owner 回退），都是 `("", "")`；
`EnsureAgent` 的 overlay 只取 owner 的 **user** scope。于是这行被存下、
被 CLI 报「Set」、被 `notifyGatewayReload` 触发重载，运行时却全程不看它——
症状与 `web_search` 事件完全一致（无 chain、无日志）。

现在两条 attach 路径共用同一个 overlay 构造器
（`gateway.toolConfigForAgent` / `applyToolOverlay`），层序与
`scope.Setting` 一致（外→内，内层胜）：

```
base（system + 调用者自己的 user 层） < owner user 层（外部访客 + shareModelConfig）
  < agent 层（user_id="", agent_id=X） < (调用者, agent) 层
```

`loadUserSpace` 用 `toolOverlaysForAgents` 批量取（2 条查询覆盖该用户所有
agent，而不是每 agent 4 次点查），并且只接受 `user_id=""`（agent 层）与
`user_id=调用者`（自己的 per-agent 层）两种行——`BatchGetConfigsByAgentIDs`
本身不按 user_id 过滤，别人的 per-agent 行必须显式丢掉。

批量路径**不按查询返回顺序合并**：`BatchGetConfigsByAgentIDs` 没有 `ORDER BY`，
Postgres 下行的顺序取决于存储，若按返回顺序逐条覆盖，两层都命中同一个
category key 时谁胜出将不确定，还会与逐层点查的 `toolConfigForAgent`
（agent 层 → 调用者层）不一致。现在按「先 agent 层、后调用者层」显式分层遍历
（`layerUsers`），`TestToolOverlaysForAgentsLayerOrderIsDeterministic`
用一个把 `BatchGetConfigsByAgentIDs` 行序反转的包装 store 钉住这一点。

### P1b：agent 层的 `sandbox` 改为写侧拒绝

`sandbox` 走的是另一条路：executor pool 由 **system 层**的 sandbox 行一次性
构建（`gateway.buildSystemSandboxPool`）后发给所有 agent，
`ResolvedAgent.Sandbox` 只会被 system/user 层填充，且 `AgentFileConfig` 里
没有 Sandbox 字段——agent 层 sandbox 在运行时**结构性不可达**。写进去只会得到
「agent 认为需要 sandbox、但没有 executor」的 `sandbox required but no
executor available`。

因此 `scope.SaveSetting` 对 `agentID != "" && namespace == "sandbox"`
**直接返回错误**（消息里说明 sandbox 是 system/user 层设置），
`SaveSettingByScope` 走同一个守卫；`agentcli` 里那句「enabled 就顺手补
backend=docker」的死代码一并删除——它只是让一行没人读的数据看起来更合理。

### P2：`kvkeys.dataPaths` 补 `plugins.enabled.*`

每 agent 的插件开关行 `plugins.enabled` 的 data 是 `{pluginID: bool}`，
即它的下一段是**数据 key**。旧白名单漏了这一条，于是
`browserUse` 会按结构体字段被折成 `browser_use`（`BROWSER_TOOL` →
`browser_tool`）——与 `web_search` 同一个根因、隔壁一个 namespace。
现已在 `dataPaths` 补 `{"plugins","enabled","*"}`，并在
`kvkeys_test.go` 加了正/负例（含 `plugins.enabled` 本身仍是标量字段）。

### P3：`plugins.enabled` 独立成 kind

该行原先与 `plugins` setting namespace 共用 `kind=setting`。它的镜像行名是
`plugins.enabled.<pluginID>`，而 system 的 `plugins` 行镜像里有一个**标量**
`plugins.enabled`（`PluginsCfg.Enabled`）——同一
`(kind, scope, scope_id)` 分区内的前缀重叠，fallback 读 `plugins` 时
`out["enabled"]` 可能是 bool 也可能是 map，**取决于 map 迭代顺序**。

现在该行归 `store.KindPluginEnabled`（`kind="plugin_enabled"`），
读写都收口到 `scope.SaveAgentPluginEnabled` / `scope.AgentPluginEnabled`
（blob 优先、镜像兜底，镜像写在自己的 kind 分区），dashboard 与运行时读同一
份实现。`store.migratePluginEnabledKind` 把存量行（blob 行 + 镜像行）迁到新
kind，并且刻意不碰 `plugins.enabled` 标量镜像行；语句用
`NOT EXISTS` 保护 `(kind,user_id,agent_id,name)` 唯一键，SQLite / Postgres
都可重复执行。

回归测试：
`gateway` → `TestToolConfigForAgentLayerPrecedence`（层序 + 别人的
per-agent 行不可见 + base 不被改写）、`TestEnsureAgentHonorsAgentScopeTools`
（只有 agent 层有 chain 时 lazy-attach 仍能注册 `web_search`）、
`TestToolOverlaysForAgentsBatch`（批量读与单 agent 读一致）；
`TestToolOverlaysForAgentsLayerOrderIsDeterministic`（反转行序后调用者层仍胜出）；
`scope` → `TestSaveSettingRejectsAgentScopeSandbox`、
`TestAgentToolScopeStillWritable`、`TestAgentPluginEnabledRoundTrip`、
`TestPluginsNamespaceIgnoresAgentOptIns`（含 KV fallback 路径）；
`store` → `TestMigratePluginEnabledKind`；`kvkeys` → `plugins.enabled.*` 正负例。

## 值保真：`configs_kv.value_kind`（db043cd）

前三轮修的是「key 写丢了」「分区撞车」「代理层没人读」。这一轮修的是最后一层：
**值写进去的时候类型就丢了**。

### 问题

`configs_kv.value` 是 `TEXT`。写入时 `flattenJSONToKV` 把每个叶子 stringify，
读取时 `parseKVValue` 再从文本猜回来。猜必然出错：

- 字符串 `"123"` 与数字 `123` 在列里完全同形；
- `%g` 往返把 `9223372036854775807` 变成 `9.223372036854776e+18`，
  与原串不等 → 旧代码判定「不是数字」→ 19 位 id 以**字符串**回来，
  再 unmarshal 进 `int64` 字段就报错；
- `nil` 直接跳过不写行，于是 `{"a":null}` 与 `{}` 在镜像里长得一样。

这是量变到质变：靠读侧校验补不回来，因为信息在**写入时**就已经不在了。

### 方案：给行加一个类型 tag

```sql
ALTER TABLE configs_kv ADD COLUMN value_kind TEXT NOT NULL DEFAULT ''
```

`value_kind` 表明这一行的文本是 **JSON 数据模型的哪一种**（`string` / `number` /
`bool` / `null` / `object` / `array`），**不是 Go 的类型名**。选 JSON 域而不是
Go 域的理由：

1. JSON 本来就是这个数据的域——`configs.data` 是 JSON blob，`encoding/json`
   映射到 `interface{}` 恰好就是这六种，所以 tag 记录的是「它本来是什么」，
   不是给数据强加 Go 的型；
2. 任何语言的读者都能据它行动（面板是 JS）；
3. Go 侧把 `int` 改成 `int64`、重命名结构体，都不会变成数据迁移。
   反过来，tag 写 `int64` / `time.Duration` 才是强绑定。

tag 是**解码提示，不是约束**：文本与 tag 不符时仍然返回值（`Decode` 兜底），
因为拒收一个值就是在丢数据——正是这套机制要消灭的失败。

### 落地形状

`internal/store/config_value.go`（新文件）：

```go
type ConfigValue struct { Value string; Kind string }  // Kind == "" = 未标注
func EncodeConfigValue(v interface{}) ConfigValue      // JSON 值 → (文本, tag)
func (v ConfigValue) Decode() interface{}              // tag → JSON 值
func StringValue(s string) ConfigValue                 // 不透明文本的简写
```

`Store` 的 KV 接口改为收发 `ConfigValue` 而不是裸 `string`：
`GetConfigValue` / `SetConfigValue` / `ListConfigValues`。生产写路径只经
`EncodeConfigValue`，它**总是**带 tag，所以这个迁移不会在代码里悄悄退化；
接口本身并不禁止 `ConfigValue{Value: "x"}`（Kind 为空），那是给测试与历史行
留的口子——「未标注」必须仍然可表达，否则读不到迁移之前写下的数据。
未标注行的两条读规则见「现状」。

### 迁移与向后兼容

- `store.migrateConfigsKvValueKind` 用 `tableHasColumn` + `ALTER TABLE` 给已存在的
  表补列（`CREATE TABLE IF NOT EXISTS` 不会碰老表），可重复执行；
  **它必须排在 `migrateConfigsToKV` 之前**——后者经 `SetConfigValue` 写行，
  SQL 里带 `value_kind` 列名，老表上先跑回填会「no column」失败（而且是
  逐行 `slog.Warn` 静默丢行，不报错），
  `TestMigrateRetrofitsValueKindBeforeBackfill` 钉住这个顺序；
- 老行的 `value_kind` 就是默认值 `''` = 未标注，读时走**原样保留的旧启发式**
  （`store.decodeLegacyValue`，从 `scope.parseKVValue` 原封搬过去），
  行为与改动前逐字节一致；
- **刻意不做回填**：老数据里没有回填所需的信息（这正是问题本身），
  把猜测写进列里就再也分不清猜测与事实了。老行在下次被写入时自然获得 tag；
- `scope.parseKVValue` 因此退场（写侧不再产出无类型文本，读侧不再需要猜），
  只在 store 里以 legacy 形态保留一份。

### 数字为什么用 `json.Number`

`Decode` 对 `number` 返回 `json.Number` 而不是 `float64`：`encoding/json` 把
`json.Number` 按字面量原样 marshal，所以 `jsonInto` 的
marshal→unmarshal 一跳仍能把精确的 `int64` 落进 `int64` 字段。要 `float64` 的
调用方显式转换——于是「哪里允许丢精度」被写在了代码里，而不是藏在读写往返里。

边界要说清楚：如果**写侧**拿到的是 `float64`，精度在到达编码函数之前就已经没了，
tag 补不回来（`TestConfigValueNumberFloat64Boundary` 钉住这条边界）。

### 文本形式也必须与 `encoding/json` 一致

`json.Number` 只在文本本身就是那个精确字面量时才救得回精度。同一轮 review 里
发现写侧还有两个「类型对了、文本错了」的口子：

- `strconv.FormatFloat(t, 'g', -1, 64)` 在 **1e6** 就切指数记法（`"1e+06"`），
  而 `encoding/json` 要到 1e21 才切。`"1e+06"` 是合法 JSON 数字，但不是整数字面量，
  `jsonInto` 把 `contextWindow: 1000000` 投影回 `int` 字段时报
  `cannot unmarshal number 1e+06 into Go value of type int64`——而这个 float64
  是**每个**设置写入的常态（`setup.toMap` 的结构体 → `map[string]interface{}`
  一跳就把 int 变成 float64）。现在 float 分支直接输出 `json.Marshal` 的字节
  （float32 走 32 位精度），整数值的浮点仍然写成整数形式；
- `object` / `array` 的解码用 `json.Unmarshal`，嵌套数字一律变回 `float64`，
  于是 `{"id":9223372036854775807}` 在读对象那一跳就把低位抹成 0 了。
  现在改用 `Decoder.UseNumber()`，嵌套数字与顶层同样保持字面量。

`TestConfigValueIntegralFloatKeepsJSONIntForm` / `TestConfigValueNestedNumbersKeepTheirDigits`
与 `scope` 侧的 `TestSettingLargeIntThroughKVOnlyPath`（`maxTokens: 2000000`
经 `toMap` 落库再从镜像投影回 `AgentDefaults`）钉住这两条。

### 回归测试

`store` → `TestEncodeDecodeConfigValueRoundTrip`（六种类型的往返矩阵，
含「`"123"` 是字符串不是数字」）、`TestConfigValueNumberKeepsLargeInt64`
（19 位整数精确往返）、`TestConfigValueFloatFormatIsValidJSON`
（`1e21` / `5e-324` 这类格式化结果必须仍是合法 JSON 数字）、
`TestConfigValueNumberFloat64Boundary`、`TestDecodeConfigValueUnknownKindFallsBack`、
`TestConfigValueIntegralFloatKeepsJSONIntForm`（1e6 起必须仍是整数字面量）、
`TestConfigValueNestedNumbersKeepTheirDigits`（对象里的 19 位整数）、
`TestDecodeConfigValueMalformedObjectKeepsText`、`TestDecodeLegacyValue`
（旧启发式逐例保留）、`TestConfigsKvValueKindRoundTrip`（tag 落库并读回）、
`TestMigrateConfigsKvValueKindRetrofitsLegacyTable`（老表补列 + 幂等 + 老行仍可读）；
`scope` → `TestMirrorFallbackRestoresValueTypes`（**端到端**：删掉 blob 行后从镜像
投影出 `int64` / `string "123"` / `bool` / 空串，未加 tag 时该测试失败）、
`TestGetValuesScopePrecedence`（内层同时替换值与 tag）、
`TestProvidersMirrorFallbackKeepsNumericKey`（标注行与未标注行各一例）、
`TestProvidersMirrorFallbackKeepsLegacyStructure`（未标注的 `models` 数组行必须
解回结构——db043cd 把这条漏掉了，provider 会整个消失）、
`TestSettingLargeIntThroughKVOnlyPath`（1e6 以上的 int 端到端）。

### 顺带删掉的

`store.camelToSnake` 失去了最后一个生产调用者（`flattenJSON` 改用
`EncodeConfigValue`），连同只测这个薄别名的 `TestCamelToSnakeAllCaps` 一起删除——
实现与测试都在 `kvkeys`，留着就是第二份会漂移的副本。
`scope.camelToSnake` 在后续 review 里因为同样的理由删除（它已经没有生产调用者，
只剩测试在用）；`scope.snakeToCamel` 保留，`kvFieldMap` 仍在用。
「所有权 → KV scope」的映射也从两处合并为 `store.KVScopeFromOwnership`。

## 写侧保真：JSON 文本列不再经过 `float64`（963b71e）

上一节解决的是「KV 里的值带着类型」。这一节解决它的前提：**值在写进来之前
就已经被压成 `float64` 了**。

8 处代码在做同一件事——把 typed 结构体变成可以塞进 `configs.data` 的
`map[string]interface{}`：

```go
blob, _ := json.Marshal(v)
var m map[string]interface{}
_ = json.Unmarshal(blob, &m)   // ← int64 字段在这里变成 float64
```

`Marshal` 出来的 JSON 字面量是精确的，`Unmarshal` 到 `interface{}` 的默认
解码把它变成 `float64`：`9007199254740993` 当场变成 `…992`。读侧同理，
`scanConfigRow` / `scanConfigs` 直接 `json.Unmarshal` 进 `ConfigRecord.Data`。
于是「DB 里的 number 是精确的」这个前提根本不成立——`configs.data` 里的数字
可能在第一次写入时就已经四舍五入了。

现在只有两个入口，都在 `internal/store/json_map.go`：

```go
func JSONToMap(blob []byte) (map[string]interface{}, error)  // Decoder.UseNumber()
func ValueToMap(v interface{}) map[string]interface{}         // Marshal + JSONToMap
```

不变式：**任何 `interface{}` 里承载的 config 数字都是 `json.Number` 字面量**，
`float64` 只在调用方显式转换时出现（和 `ConfigValue.Decode` 对 `number` 的约定
一致）。`json.Number` marshal 时原样输出，所以它在每一跳都不变形：
`SaveConfig` 写列、`flattenJSONToKV` 写镜像、HTTP 响应体、CLI 打印。

覆盖：`store` 的六处 JSON 文本列解码（`configs.data` / `channels.data` /
`agents.config`）、`scope.providerToData` / `channelToData`、
`setup.toMap` / `wrapKeyed` / `saveAgentSkillEntries` / 两处 channel dm /
masked 响应、`gateway` 的 channel 更新、CLI 的 `structMap` /
`channelConfigData`。typed 目标（`json.Unmarshal(blob, &cfg)` 到结构体）不需要
改：解码器本来就是按字段类型解析字面量的。

**代价**：字面量被原样保留，所以客户端写 `2e6` 存下的就是 `2e6`，投影到 `int`
字段会报错（旧行为是 `float64` 把它规范化成 `2000000` 才存）。JS 的
`JSON.stringify` 对 <1e21 的整数不会产出指数形式，实际很难撞上；真要容忍
指数写法，正确的位置是**读侧的 typed 归一化**（按目标字段解析字面量），
而不是写侧丢掉字面量。

测试：`store` → `TestConfigDataKeepsNumberLiteralsAtRest`（直接把
`9007199254740993` 写进列、读回必须是 `json.Number`、再存回去数字不变）、
`TestValueToMapKeepsIntDigits`（`int64` 字段 → map → 列 → KV 全程 19 位）、
`TestJSONToMapNestedNumbers`（数组/对象里的数字）；`agentcli` →
`TestSetGetConfigAgentScope`（`GetConfig` 返回 `json.Number`，但 CLI 渲染出的
仍是 `0.42`）。

同类未覆盖（不同域，不是 config 列）：第三方 API 响应（feishu / line）、
plugin / skills manifest 的解码。它们不参与 config 往返，需要时按同一模式换
`UseNumber` 即可。

## review 第三轮修复：provider 名白名单 / BatchSettings 兜底

这一轮收掉「现状」里最后两处已知的形状不一致，两处都是**读路径承诺没被所有
读入口兑现**，与前几轮同因。

### 1. provider 名不能带点（写侧拒绝）

provider 名同时是两样东西：`configs_kv` 的 key 前缀（`<名>.<字段>`，而
`kvValsToProviders` 只能按**第一个点**切）和 `provider/model` 引用的左半边。
名字里带 `.` 时镜像投影会把 `my.provider.api_key` 切成 provider `my` + 字段
`provider.api_key`，投影出一个空 `ProviderConfig`；带 `/` 则与 model 引用
的分隔符撞车。写侧原来只校验非空，于是这种名字能建出来，只在「blob 行缺席、
读镜像」时才暴露。

现在 `scope.ValidateProviderName` 是唯一规则：`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`。
`SaveProvider` 在入口调用它（HTTP 建/改、admin API、onboarding、CLI 都走这里，
新增调用方绕不过去），HTTP 侧把错误翻成 **400 + 规则原文**，不再是保存路径的
500。回归测试：`scope` → `TestValidateProviderName`（含 `SaveProvider` 这条
写入路径）、`setup` → `TestCreateProvider_RejectsAmbiguousName`。

**兼容性**：存量行不动。已存在的非法名 blob 行照常读（blob 权威），它们的镜像
行本来就是坏的（历史写入时已经按小写化存过 `my.provider.api_key`），没有可回填
的东西。没有改名接口，这类行要么删除重建，要么保持只读；`PUT /api/providers`
现在也会回 400 而不是让 SaveProvider 抛 500。

### 2. `BatchSettings` 补上 KV 兜底（`Channels` 明确不补）

除 `BatchSettings` 外每个读入口都会在「链路上没有 blob 行」时回落到镜像，
只有它直接返回 `mergeByNamespace` 的结果。于是「只写在镜像里的 namespace」在
面板上看不到，而运行时能解析出来——正是 `web_search` 事件的反向形态。

现在对**没有 blob 行**的那几个 namespace 逐个调用 `Setting`（契约的定义者，
`BatchSettings` 的文档注释本来就写着「等价于逐个 `Setting`」，批量路径只是它的
优化），有 blob 行的 namespace 不付额外查询，面板的 2 次查询快路径在常见情况下
不变。「有没有 blob 行」看的是**行**，不是合并结果：`Enabled: false` 的行是
「关掉这个 namespace」的决定，`mergeByNamespace` 已经把它排除，若按合并结果
判断就会把它的值从镜像里复活。回归测试：`TestBatchSettingsFallsBackToMirror`
（blob namespace 取 blob、镜像独有 namespace 取镜像、没人写过的 namespace 仍然
缺席）与既有的 `TestBatchSettings_DisabledRowIgnored`。

`Channels` 仍然没有兜底，这是**刻意的**：channel 行从不进 `configs_kv`（它有自己的
`channels` 表），没有可回落的镜像。这个前提现在由
`TestChannelsAreNotMirroredInKV` 钉住——哪天有人加了 channel 的半套双写，这条
会先红，指向 `Channels` 必须先学会兜底。

**测试隔离的副作用**：`openScopeDB` 的 DSN 是 `file::memory:?cache=shared`，
即整个 `scope` 包共用一个内存库，新测试写的 `prefs` 行会漏给时区优先级测试。
新增 `openScopeDBNamed` 供需要自己数据的测试用（本次的 `BatchSettings` 测试）。

## 残留风险

1. **未标注行仍靠猜测**：新写入的值都带 `value_kind`，但改动之前写下的行没有
   （信息本来就不在），读它们只能按路径各自的旧规则来（settings 走
   `decodeLegacyValue` 猜标量，providers 只解结构、标量保持文本）——`"123"` 与
   数字 `123` 在列里本就不可分。这些行只在被重新写入时获得 tag。回填被刻意
   排除：那是把猜测写成事实。排查这类行可用 `SELECT ... WHERE value_kind = ''`。
2. **KV 镜像本身仍可能不完整**：非事务的双写、历史行、手写行都会留下子集。
   现在只表现为「镜像与 blob 不一致」，不再影响读取；如果以后要把 KV 提升为
   权威读源，需要先给它加完整性标记并事务化写入。
3. **空 map 只在读侧恢复**：`flattenJSONToKV` 仍然不为 `{}` 产出行，所以镜像里
   没有这个 key（读 blob 时正确返回空对象）。
4. **LIKE 转义只覆盖已发现的位置**：`configs_kv` 的 name/scope_id 前缀匹配都已
   转义；新增按前缀匹配的 SQL 时要记得走 `likePrefixPattern` / `escapeLike`。
5. **agent 层「写了没人读」只堵住了已知的 sandbox**：`scope.SaveSetting` 是通用
   入口，理论上仍可写入 agent 层的 `memory` / `privacy` / `hooks` /
   `objectstore` / `taskqueue` / `heartbeat` / `teams` / `skills.install` /
   `plugins` 等「仅系统层可读」的 namespace（当前 CLI/HTTP 都不产生这种行）。
   约定：新增一个 agent 层可写的 namespace 时，必须同时让 runtime 读它，或者
   像 sandbox 一样在写侧拒绝——两者都没有就是这次的 bug 类。

## 对上游的 PR 提案

**标题建议**：`fix(configs-kv): preserve per-(user, agent) scope instead of folding to user`

**正文要点**：
1. 上游 `kvScopeFromOwnership` 把 `(X, Y)` 折叠到 `(user, X)`，两个故障：
   a. 同用户多 agent 时，per-(user,A) 的 provider key 泄漏给该用户的其他 agent（数据泄露）；
   b. 迁移 `migrateConfigsToKV` 时同用户多 agent 的 key 撞到 `(user, X)` 同一行，last-write-wins 静默丢数据。
2. 改动：`configs_kv` 增加第四层 scope `user-agent`，`scope_id = userID/agentID`；
   前缀扫描 `GetValues` 的读路径加最内层（四层点查走 `GetConfigByName`）；
   所有权→scope 的映射收敛成一个函数（fork 实现在 `store.KVScopeFromOwnership`），
   迁移与读路径共用，不再各写一份。
3. 破坏性：无。`user-agent` 是新 scope 值，存量数据要么没有该层、要么迁移前已折叠
   （可重跑迁移恢复）。写路径上游本身只在 3 层内产生数据，此改动只影响
   **既有 per-(user,agent) 行**的读写，属修复性增强。
4. 附带回归测试 3 组（隔离 / 迁移 / e2e）。

**可选**：如果上游不想要第 4 层，替代方案是把 `(X, Y)` 拒绝/回退（fail-closed）
而非静默折叠——至少避免泄漏；但四层模型才是语义正确的修复。
