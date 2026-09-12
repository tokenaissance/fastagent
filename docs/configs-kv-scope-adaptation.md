# configs 数据域：blob / configs_kv 双写与四层 scope 适配

> 状态：fork 已落地。本文档是这个数据域的**设计记录 + 实现对照**：第一节
> 「现状」描述当前代码实际在做的事（改代码先改这一节），后面按时间保留每轮
> review 的推理过程，最后一节是给**上游 fastclaw** 的 PR 提案。历史小节里的
> 结论如果与「现状」冲突，以「现状」为准。
>
> **决策（2026-09-12）：`configs_kv` 是目标形态，`configs` 正在向它演进。**
> 方向是 blob 逐键拆进 `configs_kv`、最终由 `configs_kv` 承担权威读源、blob 表
> 退场——**不是两表长期并存**。所以当前「blob 权威 + KV 镜像必须是 blob 的忠实
> 投影」是**迁移期的临时不变式**，不是终点契约；它有明确的退出条件（见「现状 ·
> 目标形态与迁移阶段」）：typed encoding → 事务化双写 → 完整性标记 → 翻转权威 →
> 下掉 blob。不会发生的是「删掉镜像」——镜像就是终点；会发生的是删掉 blob 表。
>
> **决策（2026-09-13）：保留 `configs_kv`，不回退到上游的 JSON blob + `scope_id`
> 单列。** 「删掉 configs_kv / 全盘对齐上游」这条路线被明确否决（影响面与理由见
> `configs-kv-scope-decision.md`）：当前不变式是 blob 权威、镜像是可验证投影，删表
> 本身安全，但代价是拆掉阶段 0–2 的全部基础设施、并放弃阶段 3–4，所以不删。沿
> 「演进到 `configs_kv`」的路线继续：阶段 3 = 翻转权威，阶段 4 = 下掉 blob。
> 同一条决策钉下**用户模型分层**：第 4 层 scope 的 `user` 是**发起人**
> （principal），不是固定的 owner —— 见「现状 · 用户模型分层」。

## 现状：实现对照（权威）

### 四层映射与依赖方向

| 层 | 本仓库的落点 | 谁依赖它 |
|---|---|---|
| Entities | `internal/config` 的 typed 结构体（`Config` / `ProviderConfig` / `ChannelConfig` / `AgentDefaults` …）与四层所有权语义 | 被所有层依赖，自己不依赖任何 IO |
| Use Cases | `internal/scope`：解析与合并（`Setting` / `ExactSetting` / `Providers` / `Channels` / `GetValues` / `SettingInto`）+ 单层读模型（`readmodel.go` 的 `SettingAt` / `SettingNamesAt` / `ProviderStateAt` / `ProvidersAt` / `AgentScopeRows` / `RowsAt`）+ 读权威开关（`readmodel.go` 的 `configsReadAuthority`：`blobFirst` / `mirrorFirst` 与逐行 `certifiedMirror` 认证）——「哪一层的行胜出、值怎么投影、这次读去哪张表」 | `setup` / `gateway` / `agentcli` |
| Interface Adapters | `internal/kvkeys`（键形状编解码）、`store.ConfigValue`（value + value_kind 编解码）、`store.ConfigMirror` / `store.MirrorSelfConsistent`（镜像完整性标记：prefix + key_count + fingerprint + enabled）、`store.JSONToMap`（blob 解码）、`scope.Save*` 的双写编排、`setup` 的 `scope` 字符串 ↔ `(user_id, agent_id)` 转换 | `scope` / `setup` |
| Frameworks & Drivers | `internal/store/database.go` 的 SQLite / PostgreSQL、表结构、迁移；HTTP / CLI 入口 | 最外层，随时可换 |

依赖方向只有向内一条：`setup → scope → store → database/sql`，`config` 在最里
层不依赖任何人。**store 不 import scope**（否则成环），所以「所有权 → KV
scope」这条映射规则放在 store（`store.KVScopeFromOwnership`），由 scope 复用。
这条依赖方向现在有测试兜底：`internal/scope/read_routing_test.go` 扫源码，
任何 `internal/scope` / `internal/store` 之外的包用 `GetConfigByName` /
`ListConfigs` / `ListConfigsByUser` / `BatchGetConfigsByAgentIDs` 读**带镜像的
kind**（setting / provider / plugin_enabled）即失败。

### 用户模型分层：第 4 层 scope 的 `user` 是发起人

这个系统里的「用户」是分层的，**第 4 层 scope（`user-agent`）里的 `user` 不是
固定的 owner**，而是这次读的**发起人（principal）**。`scope_id` 一律是
`<user>/<agent>`（`store.KVUserAgentScopeID`），变的只是那个 `user` 的所指：

| 读入口 | 第 4 层的 `user` 指谁 | 来源 |
|---|---|---|
| `agents.defaults` / `tools.*` / `skills.entries`（agent 级 overlay） | **调用方账号**（`UserSpace.UserID`） | 自己的 UserSpace |
| 跨 UserSpace 访客的 owner-fallback | **agent owner**（`rec.UserID`） | owner 的 user-scope 行，受 `shareModelConfig` 门控；访客自己显式的 `agents.defaults.model` 最后再钉一次 |
| `prefs`（timezone / `set_preference`） | **消息发起人**（`chatterUID`） | 会话里说话的人；IM 里是 channel owner 名下铸出的 app_user |

两个锚点必须分开记，因为它们不必然相等：

- **`UserSpace` 的 key 是调用方账号**（`loadUserSpace(userID)` / `UserSpace.UserID`）。
  owner 自己的 agent 就是 owner；**foreign agent**（超管 / 公开链接访客 /
  apikey 共享用户）则路由到访客自己的 UserSpace，owner 的行是作为 overlay 叠上去的。
- **`prefs` 的 key 是消息发起人**。群聊里消息统一进 channel owner 的 UserSpace，
  但 `set_preference` / `timezone` 落在 (该成员, agent) 上——这行的 `user`
  逃出了 `UserSpace` 的 key。

这不是 bug，是刻意的分层：同一份「用户配置」要同时表达「这台 bot 是谁的」和
「这次说话的人是谁」。**它也正是 `scope_id` 不能折成单列标量的原因**——上游把
`(X, Y)` 折成 `(user, X)`，同时丢掉双维归属并造成跨 agent key 泄漏（见「问题：
上游 configs_kv 只实现三层」）；fork 保留显式双列 + 第 4 层 scope，就是为了容纳
这两个锚点。`Timezone` 的读层序 `(chatter, agent) → (chatter,'') → ('',agent) →
('','')` 是这条规则最直白的体现。

### 存储面

```
configs             id, kind, scope(标签), user_id, agent_id, name,
                    enabled, credential_key, data(TEXT/JSON), 时间戳
                    UNIQUE(kind, user_id, agent_id, name)
                    INDEX(kind, user_id, agent_id), INDEX(kind, credential_key)

configs_kv          kind, scope, scope_id, name, value(TEXT), value_kind(TEXT)
                    PRIMARY KEY(kind, scope, scope_id, name)
                    INDEX(kind, scope, scope_id)

configs_mirror      kind, scope, scope_id, name, prefix, key_count, fingerprint,
                    enabled(NULL 可空), 时间戳
                    PRIMARY KEY(kind, scope, scope_id, name)
```

- **kinds**：`provider` / `setting` / `channel` / `plugin_enabled`（`store.KindPluginEnabled`）。
- **scopes**：`system` / `user` / `agent` / `user-agent`；`scope_id` 分别是
  `""` / `userID` / `agentID` / `"<userID>/<agentID>"`。格式由
  `store.KVUserAgentScopeID` 定义——删除路径用 `<user>/%` 与 `%/<agent>` 的
  LIKE 模式匹配它，所以分隔符是存储契约。
- **谁进 configs_kv**：`provider`、`setting`、`plugin_enabled` 双写；
  **`channel` 不进**（它有自己的 `channels` 表，`migrateChannelsFromConfigs`
  负责搬迁）。迁移 `migrateConfigsToKV` 只回填 `provider` + `setting`。
- **configs_mirror 是镜像的完整性标记**：每个 `configs` 行一条，记录这一行的投影
  覆盖哪个 KV 前缀、有多少个叶子（`key_count`）、这些叶子的指纹
  （`fingerprint`，`store.MirrorFingerprint`）。双写在与叶子**同一个事务**里写它；
  有标记 = 某个双写把这一行的每个叶子都写下去了。它单独一张表而不是 configs_kv
  里的一行，因为它是**关于投影的元数据**，不能出现在投影自身的前缀扫描里。
- **标记同时记 `enabled`**：一个 configs 行的读状态有两半——叶子集合（payload）
  和 `enabled` 决策（disabled 行否决外层同名条目、并挡住镜像兜底）。只记前者，
  标记就没法让翻转后的读侧回答「这个 scope 到底有没有提供它」；而一个
  **没有叶子的 disabled 行**在标记里完全没有表示。列可空，`NULL` 的含义是
  「这行标记写于该列存在之前，没有记录任何决策」——既不是 true 也不是 false。
  `store.VerifyConfigMirror(m, enabled, leaves)` 遇到 NULL 直接判否，于是旧标记
  不会替一个没人写下的决策背书（`migrateConfigsMirrorEnabled` 刻意不回填：
  答案可以从 `configs.enabled` 读出来，但标记是「写者记录了什么」的陈述，
  补写等于把缺失的记录变成断言）。

#### 目标形态与迁移阶段

目标是 **`configs_kv` 单一权威表**：blob 逐键拆进 KV，读路径最终改成 KV 优先
（乃至只读 KV），`configs` 表的 blob 列退场。当前所处的阶段是「blob 仍是权威读源、
KV 是镜像」，这是**迁移期的临时不变式**，不是终点。翻转权威不能靠改优先级硬来，
必须先补齐下面这些前提——顺序反了就会重演 `web_search` 那段历史（投影还没保真就
把 KV 当权威，缺陷被放大成线上故障）：

| 阶段 | 前提 | 状态 |
|---|---|---|
| 0 | typed encoding：值域能表达「这是 string / number / bool…」，不靠读侧猜 | **已做**：`configs_kv.value_kind`（数字保字面量，`json.Number`） |
| 1 | 事务化双写：blob 与镜像不会「写一半」 | **已做**：`store.WithConfigTx`（`9dbccd9`） |
| 2 | 完整性标记：能判定「镜像 = blob 的完整投影」，而不只是抽样一致 | **已做**：`configs_mirror` 表 + `store.ConfigMirror`（prefix / key_count / fingerprint / enabled）；双写与回填写标记，`store.VerifyConfigMirror` 判定；存量行由 `store.ReconcileConfigMirrors`（CLI `fastagent configs reconcile-mirror [--strict] [--repair]`）做 blob↔镜像核对、回填标记，`--repair` 时按 blob 重投影 diverged 行 |
| 3 | 翻转权威：读路径改 KV 优先、blob 变兜底 | **未做（机制已就位，默认仍 blob 优先）**：读路径的权威选择收敛成 `scope` 的一个开关（`readmodel.go` 的 `configsReadAuthority`），`mirrorFirst` 分支逐行要求 `store.MirrorSelfConsistent` 认证——只有标记覆盖了刚读到的叶子，镜像才作数，否则回落 blob。翻转因此是改这一个名字，不是改每一个调用方；准入仍是下面那条 `--strict` 验收 |
| 4 | 下掉 blob：迁移完成后删除 `configs` 的 blob 列与相关读代码 | **未做** |

在阶段 3 到来之前，**blob 权威**；镜像必须是对 blob 的忠实投影，这不是终点契约，
而是「让阶段 3 安全着陆」的前置条件。阶段 2 的标记机制已经就位，阶段 3 的准入
条件因此收窄成一条可执行的验收：**跑一次 `fastagent configs reconcile-mirror
--strict`，gap 归零且每一行都认证**（`--strict` 同时看 `Certified == Examined`，
所以「有行没标记」本身就会红，不是一个 gap 之外的隐性缺口）。新写入与回填自动
认证；存量行由这次核对认证（重新投影每一行、与实际镜像逐叶子比对）。比对分三种
结果：

- **exact**（名字、值、`value_kind` 全同）→ 写标记，认证；
- **untyped**（名字和值都对，只是行早于 `value_kind`、没有标注）→ **就地补标注**
  再认证。这是刻意的例外：类型由权威的 blob 明确给出，补的是「已知的事实」而不是
  猜一个值，`value` 一个字节都不动；
- **diverged**（缺叶子 / 多叶子 / 名字被改写 / 值不同 / 存的类型与 blob 矛盾）→
  默认只报告 gap，并确保没有标记为它背书；加 `--repair` 则**按 blob 重投影该
  namespace**（删掉它的全部 configs_kv 叶子 → 写投影 → 认证）。
- **空投影**（这一行没有叶子）→ 也认证：它是合法的读状态（一个没有 payload 的
  disabled 行），标记里有 `enabled` 恰好能表示它。此前这一支会把标记删掉并
  `continue`，结果是「examined 里有它、certified 里没有、gap 也没有」——
  一行镜像翻转后必须服务的行，没有任何东西为它背书。

所以闸门是**「gap 归零 + 全量认证」**，untyped 允许存在但会被补掉。没有这一步，
翻转权威等于把「镜像可能不全」从潜伏变成正式语义。

核对被设计成**可重跑**的：默认那次只做「认证 + 报告 + 补标注」，**绝不改 diverged
行的值**（不一致是需要人决策的数据，不是一个函数该替你选的值），所以重复跑结果
稳定（第二次 untyped=0），适合放进发布前检查。

`--repair` 是那个「人已经决策过」的动作，方向是**唯一的**：blob 权威，所以 diverged
行按 blob 重投影，而不是反过来把 blob 改成镜像的样子。它修的是**旧构建写出来的
形态**，不是「blob 与镜像谁对」的争议——两个已知来源：

- **塌陷**：更早的 flattener 只对**恰好** `map[string]interface{}` 下钻，于是
  `map[string]string`、`map[string]SomeCfg`、struct 都被当成一个 object 叶子写下去
  （`tools.providers.searxng` 而非 `tools.providers.searxng.endpoint`）。现在两侧
  flattener 都用 `store.JSONObjectOf` 按**结构**判断，任何 JSON object 都继续下钻。
- **数据 key 被折叠**：`skills.entries.*.env`、`tools.providers.*.options` 这类
  openPath 下的 key 是用户数据，必须原样保留；旧构建把它们当 struct 字段 snake 化
  （`AppID` → `app_i_d`）。`kvkeys` 的 `dataPaths` / `openPaths` 已经修好写入侧，但
  旧行要靠 `--repair` 才能追上来。

### 写路径

```
SaveSetting / SaveProviderState / SaveAgentPluginEnabled   ← 唯一写入口
  └─ store.WithConfigTx（Txer → *sql.Tx；不支持事务的 store 退化为顺序执行）
       ├─ KV 侧：DeleteConfigPrefix（含标记）
       │            → flattenJSONToKV → store.EncodeConfigValue(leaf)
       │            → SetConfigValue × n
       │            → SetConfigMirror（ConfigMirror = prefix + key_count + fingerprint）
       └─ blob 侧：ConfigRecord{Data map[string]interface{}} → SaveConfig → configs.data
```

两张表在**同一个事务**里写（`9dbccd9`）；完整性标记也在这个事务里，所以「标记存在」
等价于「这一行的每个叶子都写下去了」。镜像现在是 blob 的投影，所以「写了一半」
不再是正常状态：任一语句失败就整体回滚，读侧不必再为半写状态兜底。
`store.WithTx` / `store.WithConfigTx` 对没有事务能力的 store 退化成顺序执行——
那是给测试替身和别的后端留的口子，不是生产路径的退路。

**例外：channel 行只写 blob，无事务。** channel 有自己的 `channels` 表，
`migrateChannelsFromConfigs` 能从 blob 重建那张索引，所以它没有需要与 blob 配对的
镜像（见「存储面」），`SaveChannel` 因此只要求 `store.ConfigRowWriter`。

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

**优先级（当前阶段）：blob 权威，KV 兜底。** 只有当链路上四层都没有 blob 行时，
才去读镜像（镜像可能被直接写过、也可能是历史行）。这是**迁移期**的读序：目标
形态是 KV 权威、blob 退场（见「目标形态与迁移阶段」），届时这张表的 KV 兜底列
会整体翻面。

**这个翻面已经收敛成一个开关，且带逐行认证。** 单层读模型（`readmodel.go`）持
一个包级策略 `configsReadAuthority`：`blobFirst`（今天）就是上表；`mirrorFirst`
（目标）让单层读先看镜像，但**只在这一行的完整性标记认证了刚读到的叶子时才作数**
（`store.MirrorSelfConsistent`：指纹与 key 数都对得上、且标记记录了 enabled 决
策）。两者都不满足就走 blob——没有标记的历史行、被手工改过而漂移的行、标记早于
`enabled` 列的行，全都落回 blob，所以一个**不完整的投影永远不会被当成整份**服
务（这正是 `web_search` 事件的形状）。翻转 = 把这一个名字改成 `mirrorFirst`，
前置是下面那条 `--strict` 验收；回归测试在 `internal/scope/read_authority_test.go`
（`blobFirst` 与 `mirrorFirst` 两种序都钉住，含「标记不再覆盖叶子 → 回落 blob」
这一条）。

仍未接入这个开关的是**合并解析器**（`Setting` / `SettingInto` / `BatchSettings` /
`Providers`，`scope.go`）：它们逐层读 blob 再合并，`mirrorFirst` 要逐层做认证
再合并，属于同一机制的第二半。`Channels` / `Timezone` / `SettingNamesAt` /
`RowsAt` 刻意不参与翻面（见上表各自的「无，且刻意如此」）。

| 入口 | 合并层 | KV 兜底 |
|---|---|---|
| `Setting` → `SettingInto` | system → user → agent → user-agent，顶层 key 字段级合并 | 有（`GetValues` + `kvToSettingMap`，规则 = `ConfigValue.Decode`） |
| `ExactSetting` / `UserScopeSetting` | 单层，不合并 | 有（同上） |
| `Providers` | 四层，内层整行替换 | 有（`kvValsToProviders`，规则 = `kvFieldMap`） |
| `AgentScopeProviders` / `UserScopeProviders` | 单层 | 有 |
| `AgentPluginEnabled` | agent 层单行 | 有 |
| `BatchSettings` | 仅 system + user 两层 | 有，但只对**没有 blob 行**的 namespace 逐个走 `Setting`（面板专用，N 个 namespace 合并成 2 次查询；补兜底只影响 blob 缺席的 namespace） |
| `Channels` | 四层，disabled 行擦除外层（同下节的统一规则） | **无，且刻意如此**：channel 行从不进 KV（见「存储面」），`Channels` 没有可回落的镜像。`TestChannelsAreNotMirroredInKV` 钉住这个前提 |
| `Timezone` | chatter → agent → user → system（反向优先级） | **无 —— 决定不补**（见残留风险 6）：`prefs` 是双写 namespace，但 `Timezone` 只逐层点查 blob。理由是它做的是「按优先级走层」而不是合并/投影，宽化它等于让非权威行去改一个用户可见的时间；生产写入只有 `SaveUserTimezone → SaveSetting`（事务化双写），dev 库镜像独有 prefs 行实测 0 |

单层读模型（`readmodel.go`）是同一份规则的另一种形状，给「只要这一层」的调用方用：

| 入口 | 解决的问题 | KV 兜底 |
|---|---|---|
| `SettingAt` | 一个 namespace 在一个 scope 上解析成 map（`ExactSetting` 的 map 形式）；返回浅拷贝，供读-改-写 | 有（与 `ExactSetting` 共用 `settingAtRaw`，即同一条规则，不是第二份实现） |
| `ExactSetting` / `UserScopeSetting` | 同上，投影到 typed dst | 有 |
| `ProvidersAt` / `ProviderStateAt` | 一个 scope 上的全部 / 单个 provider，`AgentScopeProviders` 与 `UserScopeProviders` 现在是它的两个薄壳。单数形式返回 `(payload, present, enabled)`：读-改-写要 `present`（被禁用的行仍有 payload，不能重置成 preset），运行时「这个 scope 用哪个 provider」要 `enabled` | 有，按名字 |
| `SettingNamesAt` | 一个 scope **有行**的 namespace 集合（配置 dump、按 namespace 复制） | **无，且刻意如此**：从 KV 前缀反推 namespace 名需要 `MirrorPrefixFor` 的逆映射，而这个布局里有改名，逆映射不存在。与 `Channels` / `Timezone` 同一取舍 |
| `AgentScopeRows` | 一个 namespace 在 agent 层的**批量**行读（`tools.categories` / `tools.providers` / `skills.entries` / `agents.defaults` 的逐 agent 覆盖） | **暂无**：批量的意义就是一次查询，而这四个 namespace 都只经双写落盘。翻转时兜底加在这里，而不是它的四个调用方 |
| `RowsAt` | 一个 scope 的原始行（面板 CRUD 编辑器要的是「这一层有哪些行」，用 id / updatedAt 寻址） | **无，且刻意如此**：镜像独有的名字没有 id，列出来调用方也寻址不了。合并视图是 `Providers` / `Setting` / `BatchSettings` |

**收敛本身是可执行的**：`internal/scope/read_routing_test.go` 扫
`internal/` + `cmd/` 的生产代码，`scope` / `store` 之外任何一处用整行读方法读
带镜像的 kind 就红。写侧同理收在唯一入口（`SaveSetting` /
`SaveProviderState` / `SaveAgentPluginEnabled`），所以「哪张表」这个决定在两张表
上都只有一处。

#### enabled 语义：所有读入口一条规则（`23736ee`）

「行存在」本身就是一个决定：这一层对这个名字有话要说。`enabled = false` 表示
「这里没有它」，**并且否决外层所有同名条目**；更内层可以重新打开。这条规则来自
channel 的原始语义（内层 disabled 行擦除外层），现在 setting / provider /
plugin_enabled 一视同仁：

| 读入口 | disabled 行的后果 |
|---|---|
| `Channels` / `Providers` / `AgentScopeProviders` / `UserScopeProviders` | 从结果里删掉该名字，并挡住更外层同名条目 |
| `Setting` / `SettingInto` / `BatchSettings` / `ExactSetting` | 该 namespace 解析为空，且**不回落镜像**——行本身已经表达了决定 |
| `AgentPluginEnabled` | 视为「没有这个覆盖」 |
| `Timezone` | 返回 `""`，不再问更外层（否则会捡回内层刚刚否掉的值） |

写侧配套：`SaveProviderState` 让调用方显式传 `Enabled`（改一个已禁用的 provider
不会把它悄悄打开）；`SaveSetting` 写值即 `Enabled: true`，等于清掉旧的否决。
存量数据里没有任何 disabled 行（dev 库实测 `provider|true|2`、`setting|true|99`），
所以这次对齐今天是 no-op——它防的是以后的第一条 disabled 行。
回归测试：`internal/scope/enabled_semantics_test.go`。

#### 兜底粒度：按名字，不按链条（`23736ee`）

provider 的镜像兜底原来以「链」为单位：只要链路上任一层的 blob 行覆盖过这种
kind，镜像就整个不读。于是「blob 里有一个 provider、镜像里另有一个只由 KV 写入的
provider」时，后者会凭空消失。现在按**名字**判定——blob 决定过的名字由 blob 负责，
其余名字继续去镜像取。

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
例外是 `agents.defaults` → `agent.`（历史遗留，`store.MirrorPrefixFor` 是唯一
定义处，读侧经 `scope.kvPrefixForNamespace` 复用）。所有前缀匹配走
`likePrefixPattern`（转义 `\`、`%`、`_` + `ESCAPE '\'`）——`_` 在 LIKE 里是
通配符，provider 名里的下划线曾扫到别人的行。

映射必须**在同一个 kind 内单射**：两张表共享前缀就共享一个 `DeleteConfigPrefix`
区间和一次前缀扫描，一行会静默删掉或吞掉另一行的叶子。普通名字映射到
`<name>.`，所以唯一能撞上改名行的，就是名字等于那个前缀的词干——`agents.defaults`
占着 `agent.`，于是 `agent` 这个名字被 `store.ValidateConfigName` 在写侧拒绝
（`SaveSetting` 入口，同 `SaveProviderName` 的位置）。`mirrorRenames` /
`reservedMirrorStems` 是这张表的唯一来源，`TestMirrorPrefixIsInjective` 把
「保留名 + 普通名」放在一起证明无碰撞。

`kvValsToProviders` 用**第一个点**切 provider 名，所以带点的 provider 名会被
切错。写侧因此不接受这种名字：`scope.ValidateProviderName`（`SaveProvider`
的入口检查，HTTP 侧回 400）只允许 `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`，理由见
「review 第三轮修复」。

provider 行的形状还有一条隐含前提：前缀 `<名字>.` 之后**每一段都是结构体字段**，
所以它们能被安全地 snake↔camel 转换。名字本身是数据段，但它由写入方拼进前缀、
从不经过 `kvkeys` 的转换器（写侧另有 charset 白名单挡住无法表示的名字）。
`internal/scope/provider_kv_data_key_test.go` 从两端钉住这条前提：一次真实的
镜像往返，加一个反射守卫——`config.ProviderConfig` 里一旦出现 map / interface
字段（会被折叠器递归进去改 key 大小写），测试立刻红。

### 面板读写（`/api/config`，`7f72934`）

面板不做自己的合并。`handleGetConfig` 走 `scope.BatchSettings` + `scope.Providers`
+ `scope.Channels`——与 runtime 同一组解析器——再把结果灌进 typed `Config` 序列化
出去（`loadUserConfig` 是这条读模型的唯一实现，前端只负责渲染）。

写侧是**增量**的：`POST /api/config` 是 PATCH 语义，handler 先从原始 body 问
「这次请求提到了哪些 namespace」（`namespacesInBody` / `jsonPathPresent`，按
`settingNamespace.jsonPath` 定位），只保存命中的那几个。此前每次保存都扫
`settingNamespaces` 全表，一次面板编辑等于 ~17 个 namespace 的清空+重建写入，
并且在一个并发窗口里，读到的旧快照会覆盖调用方根本没提过的 namespace。

两个坑写在代码里：

- `jsonPath` 不是 namespace 的字符串变换——线上的 key 是 `objectStore` /
  `toolProviders` / `tools`，而 `skills.install` 与 `skills.entries` 共用
  一个 `skills`。
- 存在性检查必须大小写不敏感。否则 `{"objectstore":…}` 会被 typed 解码接受，
  却在这层判定里被当成「没提到」而静默不写（e2e 抓到的就是这个）。

**这条「面板与 runtime 同构」只覆盖 `/api/config`。** 另一组面板端点——按
(scope, scopeId) 组织的 CRUD（`GET/POST/PUT/DELETE /api/providers`、
`/api/channels`）——走的是 `listConfigsByScope`（`setup/handlers_scoped.go`）
→ `scope.RowsAt`，读的是**某一个 scope 的行**，不合并、不回落镜像。
对一个编辑器来说这是刻意的（它要列出「这一层有哪些行」而不是「解析结果是什么」）。
两个后果分别做了取舍（测试：`setup/providers_scope_list_test.go`）：

- **`enabled` 对齐**：provider 列表现在返回 `enabled`（channel 列表一直有，见
  `:346`）。既然 enabled 对 provider 是有语义的（disabled 行删掉该 provider 并
  否决外层同名条目），编辑器就必须能表示这个状态，否则它会展示一条 runtime 根本
  不用的行、却看起来一切正常。
- **不合并镜像，刻意不对齐**：列表只报本 scope 的 blob 行，看不到只存在于镜像的
  provider。这个端点是一张按 `id` 增删改的 CRUD 表，而 `id` / `updatedAt` 都是
  blob 行的属性——列出镜像行等于给调用方一个无法寻址的条目。解析后的合并视图是
  `/api/config`（以及 runtime 的 `Providers`）的职责。

另：`configs_kv_e2e_test.go` 里曾有一条陈旧注释（原写「handleListProviders
reads through scope.Providers」），是 51780fa 之前的化石，已更正。

### 依赖面：store 的能力端口（`internal/store/ports.go`）

`store.Store` 是 115 个方法的单一接口，而 configs 域实际只用 6 个：读侧
`GetConfigByName` / `ListConfigs` / `ListConfigValues`（外加批量形的
`BatchGetConfigsByAgentIDs`，它问的是同一个问题、只是访问形状不同，调用方与这三个
完全重合），写侧 `SaveConfig` / `DeleteConfig` / `SetConfigValue` /
`DeleteConfigPrefix`。`scope` 现在按能力声明参数——`ConfigReader` /
`ConfigRowWriter` / `ConfigWriter` / `ConfigStore` / `KVStore` 全是个位数方法
——于是一个只读配置的调用方、或一个测试替身，不必再实现 users / agents /
sessions / cron / MCP。

端口是**实现侧**声明（在 store 包内），`ports.go` 末尾用
`var _ ConfigReader = (Store)(nil)` 这类断言钉住 `Store ⊇ 端口`：改 Store 的签名会在
ports.go 编译失败，而不是在某个无关调用点爆掉。事务的窄版本是
`store.WithConfigTx`。`ports_test.go` 固定每个端口的方法集合，防止某个端口被慢慢
撑回大接口。

### 不变式

1. 值类型在写侧确定一次；读侧不猜（未标注行除外，规则见上表）。
2. blob 是权威、KV 是兜底；任一读入口只有在链条上没有 blob 行时才碰镜像，
   且兜底粒度按**名字**，不按链条。
3. 数据 key 段两个方向都不转换，`kvkeys` 是唯一规则来源。
4. 四层所有权 → (scope, scope_id) 只有 `store.KVScopeFromOwnership` 一处定义。
5. 新写入的 configs_kv 行必带 `value_kind`（生产路径只经 `EncodeConfigValue`）。
6. 双写在一个事务里；channel 例外，它没有镜像可配对。
7. enabled 全入口一条语义：disabled 行否决外层同名条目，且该 namespace 不回落镜像。
8. 面板与 runtime 共用同一组解析器；面板写侧只写请求提到的 namespace。
9. 「读哪张表」只在 `internal/scope` 里决定：adapter 只用读模型，不出现整行读
   方法 + 带镜像 kind 的组合（`read_routing_test.go` 扫源码钉住）。
10. `configs_mirror` 的标记描述一个行的**完整读状态**：叶子集合 + `enabled` 决策。
11. 第 4 层 scope 的 `user` 是**发起人**：overlay 用调用方账号（`UserSpace.UserID`），
    `prefs` 用消息发起人（`chatterUID`）。`UserSpace` 的 key 是调用方账号，两者
    不必然相等——那把 `user` 想成固定的 owner 就是把两把 key 当成一把。

违反 1 的症状是「值变形/丢字段」，违反 2 的症状是「面板正常、运行时不生效」，
违反 3 的症状是「配置写进去了但运行时查不到」（`web_search` 事件），
违反 4 的症状是「另一个 agent 读到了别人的 key」，违反 6 的症状是「镜像与 blob
互相矛盾且说不清是谁写的」，违反 8 的症状是「面板看着对、跑起来不对」。每类都
有一组回归测试。违反 9 的症状是「翻转权威时要改 N 个调用点、漏一个就静默不生效」，
违反 10 的症状是「翻转后 disabled 行复活」（一条被禁用的 provider/channel 在镜像
读路径上重新出现）。违反 11 的症状是「两把 key 被当成一把」——群聊成员的个人偏好
写进/读成 owner 的行（或反过来，owner 的行被当成某个成员的）。

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
无法表示空 map，而且 `dualWriteSettingKV` 是「先删前缀、再逐行写」的过程
（当时无事务；现已由 `9dbccd9` 收进一个事务，见「写路径」）——只要 KV 有任意一行，
`Setting` 就完全无视 blob，于是 namespace 静默丢掉 KV 没覆盖到的那些 key。
`web_search` 事件正是这条链路的产物。

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
（**更正**：后来 `store.ReconcileConfigMirrors` 也走它——untyped 行的补标注、
以及 `--repair` 的重投影，见「目标形态与迁移阶段」。写入方仍是那几处，变的只是
它们属于「双写」还是「认证」。）

投影侧同时补了两处：`kvkeys` 对「自由 map 容器」（`options` / `env` / `config`）
改用前缀规则，任意深度都算数据 key；provider 投影不再吞掉 `json.Unmarshal`
错误，且未标注的行走保守规则（`kvFieldMap`）：结构照解，标量按存储字符串还原。
带 `value_kind` 的行不走这条保守规则——写侧已经说明了类型，读侧直接采信
（见下文「值保真」与「现状」的未标注行对照表）。这里的「结构照解」在当时被
漏掉了（只保留标量那半条），`TestProvidersMirrorFallbackKeepsLegacyStructure`
现在钉住它。

> 前向指针：这一节把读源从 KV 翻回 blob，是**当时的止血**，不是终点。
> `configs_kv` 仍是目标形态，最终要由它承担权威读源、blob 退场；翻转的前提与顺序
> 见「现状 · 目标形态与迁移阶段」。不要把「blob 权威」读成长期结论。

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

## review 第四轮修复：语义对齐 / 事务 / 面板增量 / 依赖面（23736ee、9dbccd9、7f72934）

这一轮不是「又发现一个漏读」，而是把前三轮反复出现的同一类问题从**根上**收口：
同一个概念在多处各有一份实现或每处各有一套语义。八件事，按依赖顺序：

1. **兜底粒度对齐**（`23736ee`）：镜像兜底按名字而不是按链条，见「兜底粒度」。
   症状与此前的 `web_search` 同源——「明明写了，读的人看不到」，只是这次消失在
   provider 的合并层。
2. **enabled 语义对齐**（`23736ee`）：「行存在即决定、disabled 否决外层」从 channel
   的私有规则升格为全入口规则，见「enabled 语义」。相关影响方逐个 review 过：
   `Channels` / `Providers` / `Setting` / `BatchSettings` / `ExactSetting` /
   `AgentPluginEnabled` / `Timezone` 与写侧（`SaveProviderState`）。对存量数据的
   影响为零（dev 库无 disabled 行），唯一的新后果是「以后第一条 disabled 行会
   真的否决外层」——那正是这条语义要来管的事。
3. **面板读写改增量、读模型与 runtime 同构**（`7f72934`）：设计落在 Go 侧
   （`settingNamespace.jsonPath` + `namespacesInBody`），前端只渲染。
4. **双写事务化**（`9dbccd9`）：`WithTx` / `WithConfigTx` 把 blob 与镜像包进一个
   事务。UT 三层——`store/tx_test.go`（提交 / 回调回滚 / 语句失败 / 嵌套加入 /
   无事务退化的 `WithTx` 路径）、`scope/tx_dualwrite_test.go`（半途失败的
   provider / plugin / 成对删除）、`setup/tx_dualwrite_e2e_test.go`（真 handler +
   注入失败的镜像 store → 500 且两张表都没动，另有健康对照组）。
   **刻意不做**：channel（`channels` 表能从 blob 重建，见「写路径」）。
5. **provider data-key 回归测试**：见「key 正确性」末段
   （`provider_kv_data_key_test.go`）。
6. **Store 115 方法 → 能力端口**：见「依赖面」（`store/ports.go`、`ports_test.go`）。
7. **文档同构**：本节与「现状」同一次改动更新，不再出现「代码已改、文档还写着
   非事务」这种状态。
8. **读写模型一致性测试**：
   `setup/panel_read_write_model_test.go` 的
   `TestPanelReadModelMatchesRuntimeResolver` 是表驱动的双路径对照——面板 handler
   是路径 A，用单 namespace 解析器（`scope.Setting`，不是 handler 用的
   `BatchSettings`）独立拼一份是路径 B，两边的 namespace / providers / channels
   必须逐字一致；每个 case 另有独立断言（veto 不能复活、镜像兜底要生效、stale
   镜像不能盖过 blob、禁用的 provider/channel 不能出现）。把 handler 的 user
   scope 改错就能看到它红，所以它钉的是行为，不是实现的自证。

## 残留风险

1. **未标注行仍靠猜测**：新写入的值都带 `value_kind`，但改动之前写下的行没有
   （信息本来就不在），读它们只能按路径各自的旧规则来（settings 走
   `decodeLegacyValue` 猜标量，providers 只解结构、标量保持文本）——`"123"` 与
   数字 `123` 在列里本就不可分。这些行只在被重新写入时获得 tag。回填被刻意
   排除：那是把猜测写成事实。排查这类行可用 `SELECT ... WHERE value_kind = ''`。
2. **KV 镜像仍可能不完整（且它就是翻转权威的准入条件）**：生产写入已事务化，但
   历史行与直接写镜像的路径仍会留下子集（迁移回填、手工 SQL、以后可能出现的
   KV-only writer）。现在只表现为「镜像与 blob 不一致」，不影响读取，因为 blob
   仍是权威。方向是 `configs` 演进成 `configs_kv`（见「目标形态与迁移阶段」），
   **2026-09-13 复核确认保留 `configs_kv` 并沿这条路线继续**（见
   `configs-kv-scope-decision.md`「追加决策」）；
   镜像必须是对 blob 的忠实投影——但这是**迁移期的临时不变式**，服务于阶段 3 的
   翻转，不是终点契约。**这个风险现在可判定了**：`configs_mirror` 的标记让
   「这一行被完整投影过」成为记录下来的事实，`store.VerifyConfigMirror` 同时挡住
   「标记之后行又被改过」（手工 SQL 删一个叶子就会被抓到）。因此翻转之前剩下的
   只有一步——**跑 `fastagent configs reconcile-mirror --strict`，gap 归零且
   `Certified == Examined`**（存量行没有标记 = 未认证 = 翻转后必须回落 blob；
   核对一致才回填标记，不一致的行留作待决策）。
   在那之前，新增写入路径要么双写（收在同一个 `Save*` 入口里，自动带标记），要么
   明确登记为 KV-only 并同时补上读侧与编辑器的可见性。
   **`enabled` 已进标记**（2026-09-13）：旧构建写下的标记没有 `enabled` 列，
   迁移只加列不回填，所以它们一律判为「未记录」，由这次核对重新认证。
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
6. **`Timezone` 是唯一没有镜像兜底的读入口——决定：不补**（2026-09-12）。它的
   形状与修复前的 `Providers` 相同，但结论相反，理由是它做的事不同：`Timezone`
   是按优先级**走层**（chatter → agent → user → system），不是合并也不是投影；
   给它加兜底等于让一个非权威行去决定用户可见的时间，而这恰好是「blob 权威」这
   条规则要挡住的方向。生产写入只有 `SaveUserTimezone → SaveSetting`（事务化双写，
   必然留下 blob 行），dev 库镜像独有 prefs 行实测为 0，所以没有需要服务的行。
   若将来出现 KV-only 的 prefs 写入方，**由那条写入路径负责补 blob 行**，而不是
   宽化这个读循环。`timezone.go` 的函数注释里记了同一条，避免下一次 review 又把它
   当遗漏重新提出来。翻转权威（阶段 3）时它会**整体**从 blob 优先改成 KV 优先，
   而不是在迁移期零敲碎打地补兜底。

## review 第五轮：标记补 enabled / 读模型收敛（2026-09-13）

第四轮之后重新走了一遍「翻转权威」的准入条件，发现前四轮修的是**投影是否正确**，
而漏掉了**读状态是否完整**和**读点是否收敛**这两件事。三处改动：

### 1. `configs_mirror` 加 `enabled`

标记此前只描述叶子集合，可是一个 configs 行的读状态有两半：payload 和
`enabled` 决策。缺后者有两个具体后果：

- **翻转当天 disabled 行复活**：`scope.Providers` 对 `!Enabled` 的行做
  `delete(out, name)`，`Setting` 用它做 veto；而双写**无论 enabled 与否都把叶子
  写进镜像**（`dualWriteProviderKV` 不带分支）。于是「镜像 + 标记」这一侧无法
  表达「这一行说了不要它」，翻转后镜像会把它复活。dev 库现在 0 条 disabled 行，
  所以这个洞今天是隐形的——这正是它危险的地方。
- **没有叶子的 disabled 行没有表示**：`len(want)==0 && len(got)==0` 那一支原本
  删标记并 `continue`，于是 `Examined` 里有它、`Certified` 里没有、`Gaps` 里也
  没有——`--strict` 的「gap 归零」在有一行无人背书的情况下依然通过。现在空投影
  走同一条 switch（它本来就判为 exact），标记写下 `enabled` + 0 个叶子。

列**可空**，`NULL` = 「写于该列存在之前，没记录任何决策」。与 `value_kind` 的
取舍一致（不把猜测写成事实）：那里是信息在写入时已被销毁，这里是信息还在
`configs.enabled` 但**标记的语义是写者的记录**，回填等于伪造一条记录。代价是
旧标记一律判为未认证，由一次 `reconcile-mirror --strict` 重新认证——而那次运行
本来就是翻转前的准入条件。

配套：`--strict` 从「无 gap」升级为「无 gap 且 `Certified == Examined`」。

### 2. 读模型收敛：adapter 不再直连 blob

「读哪张表」原来散在 adapter 里：`agentcli` 9 处、`setup` 若干处、`gateway` 若干处
直接用整行读方法取 setting / provider 行，各写一份「blob 优先、KV 兜底、enabled
否决」的近似逻辑（`handlers_agents.go` 里六处近似重复的 agents.defaults 读就是
典型）。这类代码**今天是对的**，但翻转权威时要逐个改，漏一个就是静默不生效——
`web_search` 事件的同一形状。

做法不是逐点改调用点，而是把缺的读模型补进 `internal/scope/read_model`（见
「读路径」的表），再让 adapter 只认读模型：

- `SettingAt` / `SettingNamesAt` / `ProviderStateAt` / `ProvidersAt` /
  `AgentScopeRows` / `RowsAt` 六个入口覆盖了全部调用形状；
- `ExactSetting` 改写成 `settingAtRaw` 的一层投影，`AgentScopeProviders` /
  `UserScopeProviders` 改成 `ProvidersAt` 的薄壳——同一个规则从三份实现变成一份；
- 顺带删掉两个重复实现（`cmd_tools.go` 的 `loadSettingExact` 就是 `ExactSetting`
  的手抄版；`handlers_scoped.go` 的 `getConfigByNameScope` 零调用方）。

代价与收益都是显式的：收敛后**镜像独有的行对所有 adapter 都可见**了（此前只有
runtime 的合并读路径看得见），也就是「面板说没有、跑起来有」这一类不一致从读侧
消失；反过来，`RowsAt` 与 `SettingNamesAt` 明确**不给**兜底，理由写在代码里
（编辑器按 id 寻址；前缀反推 namespace 需要逆映射而布局里有改名）。

### 3. `MirrorPrefixFor` 的隐式碰撞规则改成被检查的不变式

`agents.defaults` → `agent.` 是历史布局，改不了；而普通名字映射到 `<name>.`，
于是**一个叫 `agent` 的 setting namespace 会落在同一个前缀区间**：
`DeleteConfigPrefix("agent.")` 会把两行的叶子一起删掉，前缀扫描会把两行合并。
原来这条规则只是注释。现在：改名表 `mirrorRenames` + 保留词干
`reservedMirrorStems` 是唯一来源，`store.ValidateConfigName` 在 `SaveSetting`
入口拒绝保留名，`TestMirrorPrefixIsInjective` 把保留名与普通名放在一起证明单射。

### 文档更正

「读源优先级」一节写的「`SetConfigValue` 的调用点只有双写 / 回填 / mcp undo」
在第四轮之后就不再完整——`store.ReconcileConfigMirrors` 的补标注与 `--repair`
重投影也走它。已在原句处标注更正。

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
