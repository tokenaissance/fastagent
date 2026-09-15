# Webui lint cleanup: 25 errors and 22 warnings to zero

**日期**: 2026-09-15 · **状态**: ✅ 已落 · **相关**: [chat-event-delivery.md](./chat-event-delivery.md) §9（这道门的另一半）、cloud `docs/fastagent/design/10-chat-client-parity.md` §4

---

## 1. 动机与顺序

`docker.yml` 只在 `dev`/`main` 的 push 与 tag 上构建 web，**PR 不跑**；而 `web/` 的
lint 已有 25 errors / 22 warnings。于是加了 `web-test.yml`（typecheck + lint + test），
但 lint 只能等清零之后再进 —— 一上来就红的门等于教人忽略它。

顺序：**先清债，再把门收紧到 `--max-warnings=0`**。

## 2. 25 条 error：同一个形状

全部是 `react-hooks/set-state-in-effect`，即 **effect 体里同步 setState**。四种具体形态：

| 形态 | 例子 | 修法 |
|---|---|---|
| 为「本来就是的值」再亮一次 spinner | `loading` 初值已 `true`，挂载路径仍 `setLoading(true)` | 拆 `loadX`（只取数）/ `fetchX`（取数 + spinner，供 handler 用） |
| 在答案到达前清掉旧错误 | `refresh()` 第一行 `setError("")` | 清错挪到结果回调（成功清、失败写）。**这是唯一被批准的语义变化**：重试期间旧错误留在屏上 |
| 看着 `open` 重置 dialog | `useEffect(() => { if (!open) {...} }, [open])` ×6 | 重置挪进 `onOpenChange` 的关闭分支（关闭是事件，不是外部同步） |
| 只播种一次的派生状态 | FileTree 展开态、`useAgentName` 的 fallback、access gate 的 `checking`、customize 的 loading | 一律改成**派生**（见 §3 的可见差异） |

**机制要点**：这条规则**不建模 async 函数里的 `await`**，只认 `.then` 回调。所以
`async function` 改成 promise 链不是绕过，而是让「不在 effect 同步段写入」变成规则
**能验证**的事实；React 官方取数示例也是这个形状。

## 3. 两处可见行为变化（都经批准）

### 3.1 文件树：刷新后新增的目录会展开

原来 `initedRef` 保证「只在第一棵树上播种一次」，之后新出现的目录保持关闭。现在展开态
是**派生**的（当前树的自动展开集 + 用户 toggle 覆盖表），代价就是新目录也会展开。

`web/src/components/chat-screen.tsx` —— `autoExpanded` / `toggled` / `toggle`。
**测试**：尚未断言（该面板需要驱动 workspace 交互；与本仓「cloud 对齐 webui workspace」
那条线一起补，见 §5）。

### 3.2 主题：新增跨 tab 同步与 OS 偏好即时跟随

`theme` 是外部状态（localStorage + OS 偏好），塞进 state 要么 hydration mismatch
（server 没有 localStorage），要么在 effect 里同步写（正是被禁的形状）。改用
`useSyncExternalStore`：server snapshot 是默认值，client snapshot 是存储值，React 在
hydration 后自行reconcile。**顺带白拿**两项能力：另一个 tab 写 localStorage 会被
`storage` 事件接住；`prefers-color-scheme` 变化即时生效。

`web/src/components/theme-provider.tsx`；**测试** `web/src/__tests__/theme-provider.test.tsx`
（读、跟随、持久化三条；删掉 storage 监听即红）。

## 4. 22 条 warning：一条项目级决策 + 逐条删除

* **10 条 `no-img-element`**：`next.config.ts` 是 `output: 'export'` +
  `images.unoptimized: true` —— 没有优化器给 `next/image` 走，换过去只有 width/height
  的布局风险与额外客户端组件。规则在 `eslint.config.mjs` **关一次**并写明前提（将来
  变成带图像优化的服务端部署就打开），**16 处已经失活的 inline disable 注释一并删除**
  —— 否则等于用一次决策换 16 条死注释。
* **8 条未用变量**：`Send`、`Badge`、`Bot`、`curContent`（写 6 次读 0 次的旧流式残骸）、
  `handleSelectSession`、`formatRelativeTime`、`SIDEBAR_WIDTH`，以及只写不读的
  `sessionId` state —— 全部删除，不重命名成 `_x`。
* **3 条 `exhaustive-deps`**：`models` 的 `fetchConfig` 补 memo 并列出诚实依赖；
  chat 订阅 effect 的 `applySteerEvent` 走 **ref**（声明在 250 行之下，列依赖会 TDZ，
  且连接身份本就不该取决于 handler）；`handleNewChat` 上移后进入 `handleSend` 的依赖。

## 5. 门（两半）

| 位置 | 命令 | 覆盖 |
|---|---|---|
| CI `.github/workflows/web-test.yml` | `pnpm typecheck` · `pnpm lint --max-warnings=0` · `pnpm test` | push + PR，全树 |
| 本地 `.githooks/pre-commit`（`make hooks`） | 只对 staged 的 `web/**` 跑 `eslint --max-warnings=0` | 提交前，无 autofix，缺 `node_modules` 时警告放行 |

本地可以被 `--no-verify` 跳过 —— 这正是 CI 那一半存在的理由。

## 6. 未做（明确记录）

* `web/src/lib/*.test.ts` 之外的 C1–C7 契约断言：runner 与门都已就位，缺的只是断言本身。
* FileTree 行为变化的断言：与该面板的 workspace 交互一起补。
