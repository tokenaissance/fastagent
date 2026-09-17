# 沙箱里的写盘身份：谁写、以谁的身份写、写不了时说什么

**一句话**：envd 的 `/files` 一律以沙箱用户 `user` 打开目标文件。所以"读得到、写不了"是一个合法状态 —— 文件属于别的账号（多为某次 root 命令创建），而 `write_file` / `edit_file` / `apply_patch` 全走这条路，于是三个工具给出**同一句**看不懂的 500。

## 1. 写入路径（谁以谁的身份写）

| 写什么 | 走哪条路 | 以谁的身份 |
|---|---|---|
| 相对路径（`board_core.js`、`notes/x.md`） | workspace store（S3/DB），**不经沙箱** | — |
| 绝对路径（`/workspace/...`、`/tmp/...`） | envd `POST /files?path=…&username=user`（`writeFileOn` / `uploadBytesOn`） | `user` |
| hydrate 解包（skills + workspace） | `sudo tar` 解包，随后 `sudo chown -R user:user /skills /workspace` | `root` 解、`user` 收 |
| `exec` 里的命令 | 以 `user` 跑（e2b base 模板给 `user` 免密 sudo；用了 sudo 就是 root） | 取决于命令 |

`isWorkspacePath`（`internal/agent/tools/file.go`）只认**相对路径**，所以写 `/tmp` 或任何绝对路径时，文件既不进持久工作区，也不在 hydrate 的 chown 范围内。

## 2. 故障形状（prod，2026-09-17 05:36Z）

```
05:36:27  sandboxed exec: cd /tmp && node --check board_core.js && … node board_core.js   → exit 1（脚本内断言失败）
05:36:47  apply_patch → e2b upload HTTP 500: {"code":500,"message":"error opening file: open /tmp/board_core.js: permission denied"}
05:36:52  edit_file   → 同一条错误（同一个文件、同一个身份、同一个 open()）
```

关键：**读得到**（node 能跑）≠ **写得到**。世界可读 + 属主不是 `user` ⇒ `open(O_WRONLY)` 被拒。换工具不解决问题，因为三个工具共用一个入口。

## 3. 现在的行为

`uploadHTTPError`（`internal/sandbox/e2b_executor.go`）把**这一种**判定翻译成模型能执行的下一步：

```
/tmp/board_core.js: 这个路径对沙箱用户不可写（属主/权限不匹配）：改写到 /workspace ——
envd 一律以 user 身份写盘，而这个文件不属于 user（/tmp 不在 hydrate 的 chown 范围内，
多半是某次 sudo/root 命令创建的）。用相对路径（例如 board_core.js）会落进持久工作区，
沙箱重建后仍在。原始错误：e2b upload HTTP 500: {…}
```

三条**故意**的边界：

1. **只翻这一种**：只有 `500` 且 body 含 `permission denied` / `EACCES` 才加这句话。别的 500（磁盘满、目录不存在）、401、404 一律原样透出 —— 把"属主不对"套到磁盘满上，会把模型指去改一堆本来没问题的路径。
2. **不自动 `sudo chown` 后重试**：那是一次提权写入，而且同一个身份下次还是会失败；工具层只给一步可行的改写方向（写到 workspace），不替模型提权。`TestWriteFileDoesNotRetryARefusedWrite` 钉住"只尝试一次"。
3. **仍然是"请求判决"，不是"实例故障"**：错误继续包着 `sandboxHTTPError`，`statusCodeOf` 仍读到 500，所以 §7.4 的换实例判定不会被它触发（`sandboxGone` / `sandboxUnusable` 照旧为假）。一次写失败不该销毁一个健康的沙箱。

## 4. 不覆盖什么（记清楚，别以为已经覆盖）

* **读**路径（`readFileOn`）保持原样：读不到是另一种故障（权限 600、或文件不存在），补救方式也不同，没有一起翻译。
* 只有 **e2b** 后端有这条翻译。`boxlite` / `docker` 的写路径不经过 envd `/files`，行为未变。
* `writeFileOn` 之上的重建分支（502/404 → recreate → 重试）不受影响：那是"实例故障"，本文件只处理"请求被判不可写"。

## 5. 用例

`internal/sandbox/e2b_write_permission_test.go`：

| 用例 | 断言 |
|---|---|
| `TestWriteFileNamesTheFileAndTheWayOut` | 500+permission denied → 含"改写到 /workspace"、含被拒的路径、保留 provider 原文、`errors.As` 仍拿到 500、`sandboxGone`/`sandboxUnusable` 均为假 |
| `TestUploadBytesGetsTheSameWayOut` | hydrate 的 bundle 上传走同一入口，提示一致 |
| `TestWriteFileLeavesOtherProviderFailuresAlone` | 磁盘满 / 目录不存在 / 401 / 404 四条**不**被翻译（灵敏度：放宽判据 → 四条全红） |
| `TestWriteFileDoesNotRetryARefusedWrite` | 一次拒绝只发一次 `/files` 请求，不做同身份重试 |

## 6. 实用结论

* 工作文件放**相对路径**（`board_core.js`）或 `/workspace/...`；`/tmp` 不 hydrate、不落盘、不进 Files，沙箱一换就没了。
* 已经踩上时，`exec({"command":"sudo chown user:user <path>"})` 可解，但这句**不**放进模型看到的提示里（见 §3 第 2 条）。
