# 全量扫描：skill 名字的三处持久化与旧数据兼容

**状态**：扫描完成 + 兼容层已实现（reconcile 命令可用） · **日期**：2026-09-18
**关联**：`mcp-skills-egress-design.md`（§5 命名规则、§5.1 自动改名）、`mcp-skills-fastclaw.md`（出口提案）

---

## 1. 扫描结论：名字被写进了哪些地方

| # | 存储 / 引用 | 形状 | 谁写 | 改名是否需要动它 |
| :-- | :--- | :--- | :--- | :--- |
| 1 | **磁盘目录** | `<layer root>/<dirName>/SKILL.md` | 安装 / 上传 / learner / hydrate | **要**（reconcile 已做） |
| 2 | **对象存储 key** | `<owner>/skills/<dirName>/<rel>`，owner ∈ `_global` / `<agentID>` / `_user_<uid>`（`internal/skills/objectstore.go` §`buildKey`） | `SyncSkillUp` | **要**（服务内迁移，见 §4） |
| 3 | **configs_kv 行** | `skills.entries.<name>`、`skills.entries.<name>.env.<VAR>`、`skills.agent_entries.<agent>.<name>.*`；列表 `skills.always_load[]`、`skills.disabled[]` | 控制台 / API（`internal/kvkeys` 把它们标成 data 段，不折叠大小写） | **要**（键级 rename，见 §4） |
| 4 | sidecar `.fastagent-install.json` | 只记 repo | `writeInstallMetadata` | 不要（跟着目录走） |
| 5 | sidecar `.bundled-hash` | 内容哈希 | bundled 安装 | 不要（与名字无关） |

**只读引用（不需迁移，但要知道它们按目录名工作）**：`load_skill` 工具、沙箱 `/skills/<name>` 挂载
（docker bind / boxlite tar，`internal/sandbox/*`）、`internal/agent/tools/{registry,route}.go` 的路径解析、
web 控制台 URL（`/api/agents/{id}/skills/{name}`）、CLI `skill` 子命令。

## 2. 为什么不能"直接改名了事"——四个真实交互

1. **先同步后改名 = 两个名字并存。** `SyncSkillUp(owner, name, dir)` 的 key 由调用方传的名字决定。
   如果先镜像再改名，对象存储里仍是旧 key，其他 pod 会按旧名 hydrate —— 同一个 skill 在系统里有两个身份。
   所以入口对齐必须在同步之前（本 worktree 的实现就是这样）。
2. **hydrate 的 prune 会删东西。** `HydrateSkillsDown` 在 remote 非空时，会删掉本地不在 remote 列表里的目录。
   若 store 里是旧名、本地已改名，新目录会被当成"别处已删除的陈旧技能"**删掉**。
   这是本次扫描发现的最危险的一条。已加 `LocalDirMatchesRemote` 兜底（目录名**或**声明名命中都不删），
   但迁移顺序依然不能反。
3. **configs_kv 按旧名 → 静默失效。** 改名后 `skills.entries.<旧名>` 里的 env / enabled、
   `always_load` / `disabled` 列表都不再匹配新名字。技能还在，但它读不到自己的密钥、也不再 always-load ——
   而且不报错。这是 F2（δ 必须有 σ）意义上"世界变了但没人说"的典型。
4. **混合机队。** 旧 pod 按 slug 安装，新 pod 按 frontmatter 名对齐，同一个 skill 可能以两个名字同时存在。
   新 pod 的出口按声明名发布，所以 MCP 侧始终一致；运行时要等 reconcile 收敛。

第五个较小的：**运行中的沙箱**里的 `/skills/<name>` 是一份拷贝/挂载，改名对它不可见，直到沙箱重建。
这是既有语义（技能变更下一轮生效），不是新问题，但要在迁移文档里说明。

## 3. 本 worktree 已实现的兼容层

| 层 | 实现 | 位置 |
| :--- | :--- | :--- |
| 入口对齐 | `FinalizeInstallDir`：解包后读 frontmatter 名，必要时改名（**在 `SyncSkillUp` 之前**）；声明名已被占用则**拒绝安装**；无 frontmatter 保持原样并记录原因 | `internal/skills/name.go` |
| 接入点 | ClawHub / skills.sh / GitHub 三个安装器、上传接口、skills learner | `internal/skills/{install,skillssh,github}.go`、`internal/setup/skill_install.go`、`internal/agent/skills_learner.go` |
| 读取容忍 | hydrate prune 使用 `LocalDirMatchesRemote`（目录名或声明名命中即保留） | `internal/skills/objectstore.go` |
| 路径单一源 | `GlobalSkillsDir` / `AgentSkillsDir` / `UserSkillsDir` + `FilesystemLayers()`（含 owner 与优先级） | `internal/skills/{layout,layers}.go` |
| 存量收敛 | `fastagent skill reconcile [--apply] [--global\|--agent X\|--user U] [--json]`，**默认 dry run** | `cmd/fastclaw/cmd_skill.go`、`internal/skills/reconcile.go` |

reconcile 的输出同时给出另外两处的工作清单，所以一次 dry run 就能看清全貌：

```
DRY RUN — 1 skill director(ies) scanned
  rename   managed   pdf-tools   -> pdf-processing   (dry run: directory would be renamed)
           object store: _global/skills/pdf-tools/ -> _global/skills/pdf-processing/
           config rows to rename: skills.entries.pdf-tools, skills.always_load[], skills.disabled[]
```

## 4. 迁移 SOP（分阶段，每步可回滚）

| 阶段 | 动作 | 验证 | 回滚 |
| :--- | :--- | :--- | :--- |
| 0 观察 | 全量 `skill reconcile --json > plan.json`（三个 scope 各跑一次） | 统计 rename / conflict / 无名 三类数量 | 无副作用 |
| 1 新写入对齐 | 已随本 worktree 生效 | 装一个 slug≠name 的技能，确认落到声明名 | 回退代码即可 |
| 2 目录改名 | `skill reconcile --apply`（先单 agent 试点） | 再跑 dry run：rename = 0 | 目录名改回原样（内容未动，字节不变） |
| 3 对象存储搬 key | 服务内：读旧前缀 → 写新前缀 → 逐文件校验 → 删旧前缀（**先写后删**） | `ListRemoteSkillNames` 只剩新名；其他 pod hydrate 正常 | 旧前缀未删干净前可回退；删除后需从磁盘重新同步 |
| 4 configs_kv 改名 | 键级 rename：`skills.entries.<old>[.env.*]`、`skills.agent_entries.<agent>.<old>.*`、两个列表中的元素 | 旧键查询为空、新键能读到同样的 env / enabled | 反向 rename（保留旧键快照） |

**顺序不可交换**：先目录、后对象存储、最后配置。反过来会出现 §2 的四种症状。

## 5. 不迁移 / 已知残留

- **运行中的沙箱**：`/skills/<name>` 是拷贝，下次重建才对齐（既有语义）。
- **自由文本里的技能名**：会话历史、cron 消息、用户提示词里写死的技能名不会被改写。
- **`extra` 层**（运维手放的目录）：不在 reconcile 扫描范围，需要人工确认。
- **web 书签 / 外部链接**：指向 `/api/agents/{id}/skills/{old}` 的链接会 404，控制台按新名重新展示。
- **`skills.agent_entries`** 的 rename 需要知道 agent 列表；reconcile 的 **CLI** 只动磁盘与工作清单，
  真正改 KV 的是服务内步骤（阶段 4），因为它需要 DB handle。

## 6. 下一步切片

1. 服务内 `ReconcileOwner(ctx, ws, owner, rootDir)`：把阶段 3（对象存储先写后删）做成幂等函数，
   由 API/daemon 触发（CLI 无 store handle）。
2. configs_kv rename 工具：按 §4 阶段 4 的键表执行，带 `--dry-run` 与旧键快照。
3. 把 reconcile 接进 `fastagent doctor`：输出"有多少技能不可发布（无名/冲突）"作为健康项。
4. 出口侧（MCP）复用同一份 `ReadSkillName`，让"不可发布"的原因在 listing 诊断里可见。
