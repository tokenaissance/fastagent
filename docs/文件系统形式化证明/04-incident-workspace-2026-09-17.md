# 04 · 事故记录：交付物静默回退（2026-09-17）

> 状态：已定位、已解释、修复待排期 · 最后核对：2026-09-17
> 关系：本文是 [01](./01-current-implementation.md) / [02](./02-semantics-and-architecture.md) /
> [03](./03-state-machine-and-timing.md) 的实证。三篇讲机制，本文讲"机制在生产上如何被触发"。

## 1. 现象

2026-09-17，生产会话 `OvDLzEPKcEK84Hpfq0U6LK`（agent `agt_cda27bbfbf4a84e2dfa6`，
无 project，channel=web，132 条消息）中：agent 两次报告"已恢复被回退的交付物"，
而同一批文件随后又被自动覆盖回更早的版本，全程无任何错误返回给 agent 或用户。

三个文件从新到旧的字节数：

| 文件 | agent 认为的"当时现状" | 回退后 | 恢复写入 | 再次回退 |
|------|----------------------|--------|---------|---------|
| `byo-account-design.html` | 50,282（含附录 A/B） | 41,262 | 55,527（12:07） | **41,262**（12:15） |
| `qc-email-draft.md` | 6,893（5 问版） | 5,705（4 问版） | 6,893（12:07） | **5,705**（12:15） |
| `todo.md` | 462（本轮计划） | 580（上一轮） | 462（12:07） | **580**（12:15） |

本轮新建、从未进入沙箱的文件（`quantconnect-mcp-integration.md` 19,041、
`quantconnect-overview.html` 23,285）**始终完好**。

## 2. 证据链

### 2.1 持久存储（S3，DO Spaces，bucket `fastagent-nyc3`）

前缀 `prod/agt_cda27bbfbf4a84e2dfa6/sessions/OvDLzEPKcEK84Hpfq0U6LK/`：

| 对象 | 字节 | LastModified (UTC) |
|------|------|-------------------|
| `byo-account-design.html` | 41,262 | 2026-09-17T12:15:16 |
| `qc-email-draft.md` | 5,705 | 2026-09-17T12:15:16 |
| `todo.md` | 580 | 2026-09-17T12:15:17 |
| `quantconnect-mcp-integration.md` | 19,041 | 12:06:13 |
| `quantconnect-overview.html` | 23,285 | 08:19 |
| `quantconnect.html` / `quantconnect-gonogo.html` | 86,580 / 24,083 | 15:57 / 15:42 |

三个被回退对象的 `LastModified` **落在同一次 evict 同步上**（12:15:16–17），
与其它对象明显不同——这直接排除了"某个编辑工具写坏了"的可能，指向批量覆盖。

### 2.2 网关日志（production namespace）

```
05:25:33  e2b sandbox hydrated            sandboxID=iknoadnp4ho8un3yhmjr2 workspaceFiles=1
05:25:50…07:30:13  sandbox sync: snapshot failed ×42   （over the 32.0 MB cap）
06:26:14 / 07:38:05 / 07:52:58  extend timeout … 404   （该沙箱已不存在）
07:56:44  e2b sandbox expired, recreating
07:56:53  e2b sandbox hydrated            sandboxID=iydyr4nz1rt1kxq57a1mh workspaceFiles=10
07:59:21  e2b sandbox adopted (local cache stale)
08:09:47  sandbox synced to workspace store  cause=evict      files=2
08:24:18  sandbox synced to workspace store  cause=evict      files=3
12:05:26  sandbox synced to workspace store  cause=post-exec  files=1
12:15:17  sandbox synced to workspace store  cause=evict      files=3
```

注意两个时间点：三个文件第一次被宿主工具写入是 **08:04**，而第二个沙箱
`iydyr4nz1rt1kxq57a1mh` 的 hydrate 发生在 **07:56:53**——也就是说**它从出生那一刻起
持有的就是旧版本**，并在会话余下的 4.5 小时里从未被更新（host 工具不镜像，见
[01](./01-current-implementation.md) §3.1）。

同期还有大量被拒绝的同步（与本事故互为背景，见 §2.4）：

```
sandbox sync: snapshot failed … over the 32.0 MB cap …
  Largest entries: 1371391 /workspace, 1371387 /workspace/qcdocs, 584799 /workspace/qcdocs/.git
```

### 2.3 会话消息（`session_messages`，seq 递增）

| seq | 时间 (UTC) | 事件 |
|-----|-----------|------|
| 258 | 08:04:09 | `edit_file` 写 `qc-email-draft.md` → 6,893 |
| 264 | 08:04:36 | `apply_patch` 更新 `qc-email-draft.md` |
| 279 | 08:15:16 | `list_dir` 显示 `byo-account-design.html` 50,282 / `qc-email-draft.md` 5,705 |
| 280 | 08:15:26 | agent 首次发现回退（"5,705 正是最初写入时的大小"） |
| 283 | 08:15:44 | `write_file` 修复 `qc-email-draft.md` → 6,893 |
| 307 | 12:05:45 | `write_file` 新建 `quantconnect-mcp-integration.md` → 6,748 |
| 309 | 12:06:13 | `edit_file` 该文件 → 19,041 |
| 311 | 12:06:17 | `list_dir` 显示 `byo-account-design.html` **41,262** ← agent 第二次发现 |
| 318–320 | 12:07:00 | 修复三个文件（6,893 / 55,527 / 462） |
| 322 | 12:07:06 | `list_dir` 确认已恢复 |
| — | 12:15:17 | **evict 同步再次覆盖**（无会话活动，见 §2.2） |

### 2.5 活体沙箱直读（闭合证据，2026-09-17 复核）

按 `sandbox_leases` 定位活体沙箱 `iydyr4nz1rt1kxq57a1mh`（state=paused），
`POST /sandboxes/{id}/connect` 取 token 后直读 `/workspace`：

```
GET /workspace/byo-account-design.html : 200, 41,262 bytes
GET /workspace/qc-email-draft.md       : 200, 5,705 bytes
GET /workspace/todo.md                 : 200, 580 bytes
GET /workspace/sessions/OvDLzEPKcEK84Hpfq0U6LK/byo-account-design.html : 404
```

三个数字与第三、四、五次同步写入 store 的字节数完全一致，且与 S3 当前对象一致；
第 4 条的 404 排除"沙箱里另有会话前缀副本"。读毕已 `POST /sandboxes/{id}/pause` 复原。

字节级复核：沙箱内 `md5sum byo-account-design.html = 305a43137bf54d18235ef50647579413`，
与 S3 对象 ETag 完全相同（store 里现存的就是沙箱那份，不是同长度的另一版本）。
另据 `stat`：三个文件的 **birth time 均等于 B 的 hydrate 时刻 07:56:53**，
mtime 则保留 store 当时的 `LastModified`（07:39–07:41）——
这与"hydrate 把 S3 的修改时间写进 tar"一致，也说明 **mtime 无法区分新旧版本**。

**这一步把 §3 的根因从推理升级为实测**：沙箱副本确实停留在 hydrate 时刻的版本，
而 store 的作者是宿主工具——两份副本的分叉是物理事实，不是模型假设。

附：二次自校验还发现 store 中存在两个**不在沙箱**的附件
（`mcp-oauth-authorization-flow.md`、`mcp-oauth-design.md`，12:04 上传），
说明 `Sync` 只增不减、且没有"上传后补 hydrate"的机制；其后果与对策记在
[07-formal-rootcause-and-fix.md](./07-formal-rootcause-and-fix.md) 第五部分。

### 2.6 历史回退的本地审计（7 天，2026-09-17）

网关日志没有转发、pod 一重启就丢（只剩当天 04:02 起约 18 小时），但**长期证据在另外
两个源里**：`session_messages` 记录每次工具写入的字节数与编辑增量，对象存储记录每个
对象的当前大小与 `LastModified`。审计脚本
[scripts/workspace_revert_audit.py](../../scripts/workspace_revert_audit.py) 据此判定：

```
expect ≥ 最后一次宿主写入 + (该次之后编辑的净增)
若 store 明显小于 expect，且 agent 自己的编辑解释不了 → 回退
```

数据源（各一条命令，脚本只吃 TSV）：

```sql
-- 1) 消息（含 tool_calls 的 JSON 与 tool 结果的字节数）
select session_key, to_char(created_at,'YYYY-MM-DD HH24:MI:SS'), role,
       replace(coalesce(content,''), chr(10), ' '),
       replace(coalesce(tool_calls,''), chr(10), ' ')
  from session_messages where created_at > now() - interval '7 days' order by session_key, seq;
```

```bash
# 2) 对象清单
aws --endpoint-url "$ENDPOINT" s3api list-objects-v2 --bucket fastagent-nyc3 \
    --prefix prod/ --max-items 100000 --output json
```

7 天结果（3,215 条消息 / 7,274 个对象）：

| 会话 | 路径 | 最后一次宿主写入 | 其后编辑净增 | 下界 | store 现值 | 缺口 |
|------|------|----------------|-------------|------|-----------|------|
| `OvDLzEPKcEK84Hpfq0U6LK` | `byo-account-design.html` | 13,915 | +41,706 | 55,621 | 41,262 | **14,359** |
| `OvDLzEPKcEK84Hpfq0U6LK` | `qc-email-draft.md` | 6,893 | 0 | 6,893 | 5,705 | **1,188** |
| `82c14309-c2ec-4862-a846-3fde81168f68` | `reddit_pull.py` | 2,244 | 0 | 2,244 | 1,976 | **268** |

**合计 3 条路径 / 2 个会话 / 下界 15,815 字节**，分布上集中在"沙箱活过了宿主编辑"
的那一类会话——与机制一致（其余会话的同步写入几乎都是 `exec` 新产物，属允许分支）。

两处校准：

- `qc-email-draft.md` 与 `reddit_pull.py` 的缺口是**精确值**（其后没有编辑）；
- `byo-account-design.html` 的 14,359 是用工具文本估的**上界**：事故自身的
  `list_dir` 在 `12:07:06` 记录到 55,527 字节，而 store 现为 41,262，
  **精确缺口是 14,265**。差值 94 字节来自 patch/编辑文本的估算误差（1.7% 以内），
  说明方法与实测自洽。

同一会话内另有 8 条路径（`quantconnect*.html`、`qc-lean-mcp/*`、`quantconnect-mcp-integration.md`
等）出现"store 与最后一次宿主写入不一致"，但其编辑文本不足以判定，脚本有意不把它们计入——
**宁可少报**。也就是说 15,815 字节是下界，不是全貌。

> 事故当时的报告只点出三个文件（41,262 / 5,705 / 580）。本次审计把其中两个确认为
> 可量化的数据丢失（14,265 + 1,188 字节），并**在同一天另一个会话里又找到一处**
> （`reddit_pull.py`，268 字节）——说明这不是单会话事件。

### 2.4 时序对齐（对照 [03](./03-state-machine-and-timing.md) §4）

| 模型事件 | 实际发生 |
|---------|---------|
| `Hydrate`（`X ← S`） | 05:25:33，沙箱建立，三个文件的 `X` 停在当时版本 |
| `HostWrite`（`S ← 新版本`） | 08:04–08:19 多次；12:05–12:07 再次 |
| `Sync()` | 08:09:47 / 08:24:18 / 12:05:26 / 12:15:17 |
| 症状 | `S` 被 `X` 的旧内容覆盖 |

12:15:17 那一次尤其说明问题性质：它发生在**用户会话已结束、agent 也已停止**之后
（最后一条消息 12:12:02），由空闲驱逐触发。

五次同步的字节数与沙箱/对象存储的对照（§2.1 + §2.5）：

| 同步 | cause | files | 写入的字节 | 与沙箱直读一致 |
|------|-------|-------|-----------|---------------|
| 08:09:47 | evict（沙箱 A） | 2 | 旧版本 | —（A 已销毁） |
| 08:24:18 | evict（沙箱 B） | 3 | 41,262 / 5,705 / 580 | ✅ |
| 12:05:26 | post-exec（B） | 1 | （单文件） | ✅ |
| 12:15:17 | evict（B） | 3 | 41,262 / 5,705 / 580 | ✅（当前 store 值） |

## 3. 根因

> **同一逻辑路径存在两份副本（store 与沙箱 `/workspace`），回写通道 `syncSnapshot`
> 单向且以"字节数是否相同"作为是否需要覆盖的判据。**

- 判据的正确性前提是"只有一个写入者"，该前提在 docker 上因 bind mount 而天然成立，
  在 e2b / boxlite 上不成立；
- 宿主文件工具（`write_file` / `edit_file` / `apply_patch`）写 store 时**不更新沙箱**，
  于是沙箱里的旧副本成为"看起来更新的候选"；
- 判据只看大小：大小不同 → 用沙箱覆盖。旧副本因此覆盖新版本。

完整的机制、语义错位与状态模型分别见 [01](./01-current-implementation.md) /
[02](./02-semantics-and-architecture.md) / [03](./03-state-machine-and-timing.md)。

### 3.1 为什么"从宿主侧修 store"无效

修复动作本身也是 `HostWrite`，只改变 `S`；`X` 中的旧副本依然存在，
下一次 `Sync()` 会再把 `S` 覆盖回旧值。事故中 agent 实际上修复了两次（08:15、12:07），
两次都失败——第二次的失败日志就是 12:15:17 那条 `cause=evict files=3`。

**只要沙箱副本存活，任何宿主侧的修复都是临时的。** 这是本事故最容易被误判为
"工具 bug"或"agent 幻觉"的地方。

### 3.2 为什么"新建文件没问题"

`Sync()` 对 store 中不存在的路径是可信的（见 [03](./03-state-machine-and-timing.md) §6）。三个受害文件都是
`Hydrate` 时已存在、之后被宿主工具改过的路径。因此 agent 观察到的
"被回退的都是碰过 `apply_patch` 的文件"是**相关性**：真正的判据是
"该路径在沙箱里有一份旧副本"。

## 4. 影响面

### 4.1 同后端的所有会话

触发条件与用户、agent 行为无关，只与"分离式后端 + 路径在沙箱中存在 + 宿主侧改过"有关。
同期 48 小时窗口内的回写统计（两个网关副本的日志）：

```
cause=post-exec  21 次
cause=evict       4 次
涉及会话：OvDLzEPKcEK84Hpfq0U6LK(4)、Tnv9L1hqRzoNgrwUDtfPv8(9)、82c14309-c2ec-4862-a846-3fde81168f68(12)
```

这三条会话的记录只能说明**回写发生过**，不能直接等同于**发生过回退**——
区分二者需要 §4.2 的诊断手段。这也是本次复盘暴露的可观测性缺口。

### 4.2 现有诊断手段（只读）

```bash
# 1) 对象最后写入时间（是否晚于会话中的修复动作？）
aws --endpoint-url https://nyc3.digitaloceanspaces.com s3api head-object \
  --bucket fastagent-nyc3 --key "prod/<agent>/sessions/<sid>/<path>"

# 2) 回写事件（files=N、cause）
kubectl -n production logs <gateway-pod> --timestamps \
  | grep "session=<sid>" | grep -E "synced to workspace store|snapshot failed|hydrated"

# 3) 会话内字节数变化：tool 角色消息里带字节数
#    session_messages.session_key = '<sid>' 且 role = 'tool'
#    消息形如 "Written 554 bytes to todo.md" / "Edited x.html (1 replacement(s))"
```

只能到"推断"级别——这是 §5 要补的东西。

## 5. 归因修正（避免误判）

以下两个结论在本事故复盘中一度被提出，需要修正：

| 曾出现过的判断 | 修正 |
|---------------|------|
| "是 `apply_patch` 导致的回退" | `apply_patch` 只是**最彻底的触发者**（它从不镜像进沙箱）；`write_file` / `edit_file` 在非 coding 会话里同样只写 store，同样会触发。工具不是原因，"两个写入者 + 字节数判据"才是 |
| "是 agent 误报或写错了" | 不是。08:15 与 12:07 的写入都成功（有工具结果与 `list_dir` 复核），回退由 08:24 / 12:15 的 evict 同步造成 |

## 6. 历史归属：是否由沙箱租约引入

**不是。** 逻辑缺陷在 2026-04-20 的 commit `950070b`
（"feat: cloud-ready architecture — stateless gateway, per-key scoping, provider tools"）
首次出现——同一个 commit 里既有宿主 `write_file → workspaceStore.Put`，
也有 `flushIfSupported` 的"字节数相同则跳过、否则用沙箱覆盖"。

时间线（`fastagent` 仓库）：

| 日期 | commit | 内容 | 与缺陷的关系 |
|------|--------|------|-------------|
| 2026-04-20 | `950070b` | `LifecyclePool` 首次出现 | **缺陷诞生**（host 写 store + 体积判据回写） |
| 2026-04-28 | `047f107` | per-session workspace 与沙箱隔离 | 作用域变了，判据没变 |
| 2026-05-01 | `527b8fb` | E2B hydrate + **post-exec sync** | **第一次放大**：回写从"仅驱逐时"变为"每次 exec 后" |
| 2026-05-07 / 05-08 | `5c0d74b` / `7c98b37` | `edit_file` / `apply_patch` | 宿主侧修改既有文件成为日常操作 |
| 2026-05-09 | `1fbbbb8` | `mirrorSandboxWrite` | 增加一条宿主→沙箱镜像（仅覆盖部分路径） |
| 2026-09-06 | `e359bf0` 等 | 跨 pod E2B 租约、采纳、pause/resume、epoch | **第二次放大**（不是引入） |

### 6.1 为什么"租约之前不一定看得见"

触发需要三个条件同时成立（见 [03](./03-state-machine-and-timing.md) §7）。首版只有 docker 实现
`SnapshotWorkspace`，而 docker 是 bind mount——两份副本是同一份，字节数恒相同，全量跳过。
E2B 直到 `527b8fb`（05-01）才实现 `SnapshotWorkspace`，"同一路径、两份内容"这才真正成立。

准确表述：**逻辑缺陷 04-20 就在，但只有在分离式后端上才可被触发。**

### 6.2 租约实际改变了什么

| 变化 | 影响 |
|------|------|
| 跨 pod 采纳（`e2b sandbox adopted from shared lease`） | 写文件的 pod 与做快照的 pod 可能不是同一个进程，任一侧都无法用本地状态仲裁 |
| `pause/resume` + 10 分钟 idle sleep | 一个沙箱实例跨越多个 turn 与多段用户等待，旧副本存活时间显著变长（本事故的沙箱活了约 7 小时） |
| 实例更长寿、更共享 | 回退的潜伏期从"一次会话内"扩展到"数小时后的任意一次驱逐" |
| epoch / CAS 硬化 | 解决的是"同一沙箱被两个 pod 同时持有"，与"同一文件两条写入路径"无关，因此没有顺带修掉本缺陷 |

结论：租约把发生概率从"偶发"抬到"常态"，但没有触碰仲裁逻辑。

## 7. 复盘要点（可复用的纪律）

1. **"没有报错"不等于"没有发生"**：本事故的前两次回退都由 agent 通过比对字节数才发现，
   第三次（12:15）至今没有任何自动告警。凡是"后台异步覆盖用户数据"的路径，必须有可观测输出。
2. **验证要落在持久层**：`list_dir` / `read_file` 读的是 store，因此复核有效；
   但若复核对象是 sandbox 视图（`exec`），结论会相反。
3. **修复要覆盖源侧**：修 store 而不修沙箱副本时，修复是临时的（§3.1）。
4. **相关性 ≠ 因果**：`apply_patch` 与回退高度相关，但起决定作用的是"路径在沙箱中存在"这一条件（§3.2）。
