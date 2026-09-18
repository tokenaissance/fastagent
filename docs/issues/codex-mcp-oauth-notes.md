# 调研：Codex 里的 MCP OAuth 2.0 是怎么跑通的

**状态**：调研完成（含源码与实测） · **日期**：2026-09-18
**问的问题**：Codex 能跑通 MCP OAuth 2.0，我们的 AS 要照着实现哪些东西
**配套**：`mcp-egress-decisions.md`（决策版）、`spike/mcp-oauth-stub/RESULTS.md`（本机实测）

---

## 1. 结论先说

Codex 自己不实现 OAuth 协议，它用的是 **rmcp SDK**（Rust MCP SDK）里的
`rmcp::transport::auth::AuthorizationManager`。Codex 负责的是：触发登录（`codex mcp login`）、
把凭证落盘或落 keyring、按需刷新、把 Bearer 放进 MCP 请求。

所以"如何实现"分两半：

- **协议引擎**：rmcp 的 `crates/rmcp/src/transport/auth*.rs`，官方文档 `docs/OAUTH_SUPPORT.md`
  列了完整十步流程，仓库里还有一份**可运行的 AS 样板**：
  `examples/servers/src/complex_auth_streamhttp.rs`。我们照它的端点集合实现即可。
- **Codex 侧的接线**：`codex-rs/rmcp-client/src/oauth/*`（凭证存储、刷新事务、issuer 绑定）
  加上 `codex-rs/app-server-protocol/.../McpServerOauthLogin*.json`（`mcp login` 只回
  `authorizationUrl`，完成后发通知）。

## 2. rmcp 的十步流程（官方文档要点）

1. 探 server，从 `WWW-Authenticate` 里取 `resource_metadata` 与 `scope`；
2. 取 **RFC 9728** 保护资源元数据（AS 列表 + 支持的 scope）；
3. 取 **RFC 8414 / OIDC** 的 AS 元数据；
4. 客户端注册：**DCR（RFC 7591）或 CIMD（URL 形式的 client_id）**；
5. scope 选择顺序：`WWW-Authenticate` > PRM > AS 元数据 > 调用方默认；
6. 授权请求：**PKCE S256 + RFC 8707 `resource`**；
7. 用 code 换 token（**token 请求也带 `resource`**）；
8. 用 access token 调 MCP；
9. 过期自动 refresh（**refresh 请求回带之前授予的 scope**）；
10. 遇到 `403 insufficient_scope` 时做 scope 升级并重新授权。

这与 spike 实测一致：Codex 先拿 401 challenge → PRM → ASM → DCR → `/oauth/authorize`
（带 `code_challenge`、`resource`、`scope`）→ loopback 回调 → `/oauth/token`
（带 `code_verifier` 与 `resource`）。

## 3. 我们 AS 必须实现的东西（清单）

| # | 要求 | 依据 | 备注 |
| :-- | :--- | :--- | :--- |
| 1 | `/.well-known/oauth-protected-resource`（RS） | RFC 9728 | 裸路径 + 带路径两种形式返回同一份 JSON |
| 2 | `401` + `WWW-Authenticate: Bearer resource_metadata=…, scope=…` | 流程第 1 步 | 没有它客户端不会开始发现 |
| 3 | `/.well-known/oauth-authorization-server`（AS） | RFC 8414 | 必须含 `code_challenge_methods_supported: ["S256"]`，否则合规客户端拒绝继续 |
| 4 | `POST /oauth/register`（DCR） | 流程第 4 步 | **实测 Codex 走的就是这条**；必须容忍"每次登录重新注册一个 client" |
| 5 | `GET/POST /oauth/authorize` | 流程第 6 步 | 校验 `code_challenge` 与 `redirect_uri`；用户同意在此发生 |
| 6 | `POST /oauth/token`（authorization_code） | 流程第 7 步 | 校验 `code_verifier` 与 `resource` |
| 7 | `POST /oauth/token`（refresh_token） | 流程第 9 步 | 旋转 refresh；回带原 scope |
| 8 | `403 insufficient_scope` 的语义 | 流程第 10 步 | 客户端据此升级 scope 并重新授权 |
| 9 | `iss`（RFC 9207）+ **授权端点与 issuer 同源** | Codex `oauth/issuer_binding.rs` | rmcp **会拒绝**授权端点 origin 与 issuer origin 不一致的 AS；我们 AS/RS 同源，天然满足 |
| 10 | refresh 的 issuer 绑定与串行化 | Codex `oauth/refresh_transaction.rs` | 刷新前后校验 issuer；换 issuer 会被判定需要重新授权 |
| 11 | 错误一律走 OAuth error redirect | 本机实测（Q3） | 裸 403 会被浏览器原样显示；客户端不解码 `error_description`（空格显示成 `+`） |

## 4. Codex 侧的凭证与生命周期

- **存储**：`OAuthCredentialsStoreMode` 决定 file / keyring / auto；刷新走
  `refresh_transaction.rs` 的"读-刷新-写"串行事务，并用 keyring 做单飞锁。
- **刷新失败**：本机日志里就是真实形态——
  `MCP OAuth refresh token was rejected; reauthorization required … invalid_grant: token is invalid`
  （用户的 quandora 连接正是这样过期的），客户端会要求重新授权，而不是静默失败。
- **登录入口**：`codex mcp login <name>`；app-server 只回 `authorizationUrl`
  （`McpServerOauthLoginResponse`），完成后发 `McpServerOauthLoginCompletedNotification`。

## 5. 哪些是实测、哪些是源码

| 事实 | 来源 |
| :--- | :--- |
| 走 DCR（不是 CIMD） | **实测**（spike 日志 `dcr-register`） |
| redirect 是 `http://127.0.0.1:<随机端口>/callback/<随机 id>`，每次登录重新注册 | **实测** |
| 授权与 token 请求都带 `resource`；token 请求带 `code_verifier` | **实测** |
| 拒绝时显示 `access_denied` 且 `error_description` 不解码 | **实测** |
| 十步流程、scope 选择顺序、insufficient_scope 升级 | 源码与文档（rmcp `OAUTH_SUPPORT.md`） |
| 授权端点 origin 必须等于 issuer origin | 源码（Codex `oauth/issuer_binding.rs`） |
| 凭证存 file/keyring、刷新串行化 | 源码（Codex `rmcp-client/src/oauth/*`） |

## 6. 对我们的直接建议

1. **照 `complex_auth_streamhttp.rs` 的端点集合实现第一版**：它就是一个能跑通 Codex 的 AS 样板；
   我们在其上加多租户、真实登录与同意页、以及内部那一跳（user key + 身份断言）。
2. **DCR 是硬需求**（实测唯一被走的路）；CIMD 只声明支持，作为前向路径，不依赖它。
3. **issuer 与授权端点同源**这条要在部署形态里锁死：我们的 AS/RS 都在 cloud 主机，满足；
   若以后把 AS 挪到独立域名，必须同步改 issuer 或声明 issuer-bound callbacks。
4. **refresh 会带 scope 并做 issuer 绑定**：换 issuer 或改 scope 集合时用户需要重新授权 ——
   这点要写进文档，不要让用户自己猜。
