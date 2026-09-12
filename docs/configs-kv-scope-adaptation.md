# configs_kv：per-(user, agent) 独立 scope 适配（给上游 PR 的文档）

> 状态：fork 已落地（commit `ce6f2b1`）。本文档用于向**上游 fastclaw** 提交 PR，
> 说明 fork 对 configs_kv 四层 scope 模型的适配理由与最小改动。若上游采纳，
> 请把代码 diff + 本文档的「PR 提案」章节一起提交。

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

```go
func kvScopeFromOwnership(userID, agentID string) (scope, scopeID string) {
    switch {
    case userID != "" && agentID != "":
        return UserAgent, userID + "/" + agentID   // 新增，不再折叠
    case userID != "":
        return User, userID
    case agentID != "":
        return Agent, agentID
    default:
        return System, ""
    }
}
```

### 3. 读路径加最内层

`GetValue`（单值）与 `GetValues`（前缀扫描）在 `agent` 层之后、`user` 层之内
增加 per-(user,agent) 层查询；`GetValue` 语义 = innermost wins：

```go
// GetValue：tryGet 顺序 system → user → agent → user-agent（最内层覆盖）
if userID != "" && agentID != "" {
    if err := tryGet(UserAgent, userID+"/"+agentID); err != nil {
        return "", true, nil
    }
}
```

### 4. 迁移映射 `internal/store/database.go`

```go
case cfg.UserID != "" && cfg.AgentID != "":
    kvScope   = "user-agent"
    kvScopeID = cfg.UserID + "/" + cfg.AgentID   // 不再折叠到 user 层
```

## 回归护栏（测试）

- `internal/scope/configs_kv_test.go` → `TestProviderPerUserAgentIsolation`：
  绑定 per-(user,A) provider key，断言 B（兄弟 agent）读不到、不落 user 层。
- `internal/store/configs_kv_test.go` → `TestMigrateConfigsToKV`：四层迁移后
  per-(user,agent) 落 `(user-agent, X/Y)` 独立行，不碰撞。
- `internal/setup/configs_kv_e2e_test.go` → `TestProviders_CloudPathE2E`：
  真实 handler 路径下兄弟 agent 不继承 agent-scope key。

## key 形状规则（数据 key vs 结构体字段）

`camelToSnake`（写）只把 camelCase 折成 snake_case，已经是 snake_case 的 key
原样保留；但 `snakeToCamel`（读）对这类 key **不是它的逆**：`camelToSnake("webSearch")`
和 `camelToSnake("web_search")` 都落到 `web_search`。因此两个方向都不能对每个
点号段一律转换，否则会改写「数据 key」（分类 id、provider 名、skill id、team id、
env 变量名），而改写后的 key 静默失配运行时查询 ——
`gateway.registerAgentToolChains` 查 `cfg.Tools["web_search"]` 查不到，就只是
**不注册 web_search 工具**，全程没有任何报错。

规则集中在 `internal/kvkeys`（读 `RestoredSegment`、写 `StoredSegment`、
白名单 `dataPaths`），`scope.dualWriteSettingKV` / `scope.kvToSettingMap` /
`store.migrateConfigsToKV` 共用同一份，不再各持一份 `camelToSnake` 实现：

- 结构体字段段（`memory.auto_persist` → `memory.autoPersist`、
  `objectstore.s3.access_key` → `.accessKey`）继续 snake_case→camelCase；
- 数据 key 段按 `dataPaths` 白名单**两个方向都原样保留**（`*` 匹配一段）：
  `tools.categories.*`、`tools.providers.*`、`tools.providers.*.options.*`、
  `skills.entries.*`、`skills.entries.*.env.*`、`plugins.entries.*`、
  `plugins.entries.*.config.*`、`plugins.enabled.*`（每 agent 的插件开关行）、
  `teams.*`。

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

## 读源优先级：blob 权威，configs_kv 兜底（本次变更）

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
重写为 `TestProvidersDualWriteReadsFromBlob`（生产代码里没有任何「只写 KV」的
路径，唯一的 `SetConfigValue` 调用点是双写本身和 mcp undo 游标）。

投影侧同时补了两处：`kvkeys` 对「自由 map 容器」（`options` / `env` / `config`）
改用前缀规则，任意深度都算数据 key；provider 投影不再吞掉
`json.Unmarshal` 错误，且未标注的标量一律按存储字符串还原（`kvFieldMap`）。
带 `value_kind` 的行不走这条保守规则——写侧已经说明了类型，读侧直接采信
（见下文「值保真」）。

## review 第二轮修复：agent 层可写但没人读 / plugins.enabled 形状（本次变更）

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

## 值保真：`configs_kv.value_kind`（本次变更）

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
`GetConfigValue` / `SetConfigValue` / `ListConfigValues`。**写不出未标注的行**，
所以这个迁移不会在代码里悄悄退化。

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

### 回归测试

`store` → `TestEncodeDecodeConfigValueRoundTrip`（六种类型的往返矩阵，
含「`"123"` 是字符串不是数字」）、`TestConfigValueNumberKeepsLargeInt64`
（19 位整数精确往返）、`TestConfigValueFloatFormatIsValidJSON`
（`1e21` / `5e-324` 这类格式化结果必须仍是合法 JSON 数字）、
`TestConfigValueNumberFloat64Boundary`、`TestDecodeConfigValueUnknownKindFallsBack`、
`TestDecodeConfigValueMalformedObjectKeepsText`、`TestDecodeLegacyValue`
（旧启发式逐例保留）、`TestConfigsKvValueKindRoundTrip`（tag 落库并读回）、
`TestMigrateConfigsKvValueKindRetrofitsLegacyTable`（老表补列 + 幂等 + 老行仍可读）；
`scope` → `TestMirrorFallbackRestoresValueTypes`（**端到端**：删掉 blob 行后从镜像
投影出 `int64` / `string "123"` / `bool` / 空串，未加 tag 时该测试失败）、
`TestGetValuesScopePrecedence`（内层同时替换值与 tag）、
`TestProvidersMirrorFallbackKeepsNumericKey`（标注行与未标注行各一例）。

### 顺带删掉的

`store.camelToSnake` 失去了最后一个生产调用者（`flattenJSON` 改用
`EncodeConfigValue`），连同只测这个薄别名的 `TestCamelToSnakeAllCaps` 一起删除——
实现与测试都在 `kvkeys`，留着就是第二份会漂移的副本。

## 残留风险

1. **未标注行仍靠猜测**：新写入的值都带 `value_kind`，但改动之前写下的行没有
   （信息本来就不在），读它们仍走 `decodeLegacyValue` 猜——`"123"` 与数字 `123`
   不可分。这些行只在被重新写入时获得 tag。回填被刻意排除：那是把猜测写成事实。
   排查这类行可用 `SELECT ... WHERE value_kind = ''`。
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
   `GetValue`/`GetValues` 读路径加最内层；迁移映射不再折叠。
3. 破坏性：无。`user-agent` 是新 scope 值，存量数据要么没有该层、要么迁移前已折叠
   （可重跑迁移恢复）。写路径上游本身只在 3 层内产生数据，此改动只影响
   **既有 per-(user,agent) 行**的读写，属修复性增强。
4. 附带回归测试 3 组（隔离 / 迁移 / e2e）。

**可选**：如果上游不想要第 4 层，替代方案是把 `(X, Y)` 拒绝/回退（fail-closed）
而非静默折叠——至少避免泄漏；但四层模型才是语义正确的修复。
