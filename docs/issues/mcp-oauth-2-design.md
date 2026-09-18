# 决策：MCP 的 OAuth 2.0 如何与 cloud 现有登录体系结合

**状态**：设计（未实现） · **日期**：2026-09-18
**问题**：MCP + OAuth 2.0 如何与 cloud / fastagent 当前的用户鉴权体系结合，MCP 登录复用 cloud 的用户登录
**规范**：MCP 2026-07-28 基础协议 Authorization（OAuth 2.1 + RFC 9728 资源元数据 + RFC 8414 AS 元数据 +
CIMD/DCR 客户端注册 + RFC 8707 resource indicator + RFC 9207 `iss`）
**关联**：`mcp-skills-egress-design.md`（出口设计）、`docs/mcp-oauth-design.md`（我们作为 **客户端** 接别人，本设计是镜像）

---

## 0. 先回答"登录页谁展示"——MCP 没有自己的登录方案

规范里那张完整流程图写得很直白：客户端打开浏览器到**授权服务器（AS）的授权端点**，然后标注
`Note over A: User authorizes` —— 用户在 **AS 的页面**上完成登录与同意。MCP 只规定三件事：

1. **发现**：客户端从 RS 的 RFC 9728 元数据拿到 `authorization_servers`，再去 RFC 8414 元数据；
2. **注册**：CIMD（规范优先）→ DCR（兜底）→ 预注册；
3. **硬约束**：PKCE S256、`resource`（RFC 8707）、`iss`（RFC 9207）、audience 校验。

MCP 没有、也不打算有自己的一套用户登录页。所以"跳到 cloud 的用户验证页面"不是变通，而是规范规定的样子：
**因为我们的 RS 与 AS 都是 cloud，用户看到的就是我们自己的登录页。**

用户视角的完整路径：

```
① 本地 Agent 请求 /mcp/agents/<id> → 401 + WWW-Authenticate(resource_metadata=…)
② 客户端拉 RS 元数据 → 拿到 AS → 拉 AS 元数据
③ 客户端生成 PKCE + state + resource，打开浏览器到 <cloud>/oauth/authorize?…
④ 浏览器在 cloud 域：无 session → 跳现有登录页 → 登录（注册 / 2FA / 角色全部继承）
⑤ consent 页（cloud 渲染）：client 名 · redirect 主机名 · 目标 agent · scope(skills:read)
⑥ 同意 → 302 到 http://127.0.0.1:<port>/callback?code=…&iss=…&state=…
⑦ 本地 Agent 后端 POST /oauth/token（code + verifier + resource）→ 本地保存 token
⑧ 重试 MCP 请求，带 Bearer；RS 校验 audience / scope / 归属 → 正常
```

关键在于第 ⑥ 步：浏览器与本地 Agent 之间是 **loopback 回调**，密码与授权码都不经过本地 Agent 的界面。
这也是"用 cloud 登录"与"本地 Agent 拿凭证"能同时成立的原因。

## 1. 结论

**cloud 同时扮演授权服务器（AS）与资源服务器（RS）；登录步骤 100% 复用现有登录体系。**

| 角色 | 谁 | 说明 |
| :--- | :--- | :--- |
| Authorization Server | cloud（fastagent 服务本体） | 新增标准 AS 端点；**不新建账号体系** |
| Resource Server | `/mcp/agents/<agent-id>` | MCP 出口端点，校验 Bearer token 的 audience 与 scope |
| Client | 本地 Agent（Claude Code / Codex CLI / Inspector） | 走标准授权码 + PKCE |

两句话概括机制：

1. **登录复用**：`/oauth/authorize` 不自己写登录页 —— 它要求一个有效的 cloud **session**（现有
   `fastagent_session` cookie / `/api/login`）；没有就 302 到现有登录页并带 `next=` 回来。
2. **凭证收敛**：MCP 端点只认两种 Bearer —— 新的 **OAuth access token**（主路径）和**既有 apikey**（兼容兜底），
   两者最后都归一到 `auth.Identity`，因此 `CanAccessAgent`、角色、租户隔离全部沿用。

## 2. 现状事实（代码）

| 面 | 现状 |
| :--- | :--- |
| 身份模型 | `internal/auth`：两种凭证（session cookie、apikey）→ 同一个 `Identity{UserID, Role, AuthMethod, APIKeyType, APIKeyAgents}` |
| apikey | `apikeys` 表 + `apikey_agents` ACL；`type=admin/user/agent`；`CanAccessAgent` 是唯一的 agent 授权判定 |
| 登录 | `POST /api/login`、`POST /api/logout`、`POST /api/register`（`internal/setup/server.go`），会话表 `web_sessions` |
| MCP | 仓库里只有 **客户端** OAuth（`internal/mcp/oauth/{domain,usecase,port,adapter}`，设计见 `docs/mcp-oauth-design.md`）；**没有** AS/RS 侧 |
| 出口端点 | 尚不存在（见 `mcp-skills-egress-design.md` §2 四层映射） |

**复用面在哪**：AS 只需要"当前是谁"，而这件事 `internal/auth` 已经回答；RS 只需要"这个 token 属于谁、能碰哪些 agent"，
`Identity` + `CanAccessAgent` 已经回答。所以本设计不引入第二套权限语义。

## 3. 端点清单（RS + AS）

| 端点 | 角色 | 作用 | 规范依据 |
| :--- | :--- | :--- | :--- |
| `GET /.well-known/oauth-protected-resource` | RS | 声明 `resource`、`authorization_servers`、`scopes_supported`（v1：`skills:read`） | RFC 9728（**MUST**） |
| `POST /mcp/agents/<id>` | RS | MCP 端点；未带有效 token 时返回 `401` + `WWW-Authenticate: Bearer resource_metadata="…", scope="skills:read"` | MCP 授权规范 |
| `GET /.well-known/oauth-authorization-server` | AS | AS 元数据；声明 `authorization_endpoint`、`token_endpoint`、`client_id_metadata_document_supported: true`、可选 `registration_endpoint`、`authorization_response_iss_parameter_supported: true` | RFC 8414（**MUST**） |
| `GET/POST /oauth/authorize` | AS | 复用 session 登录 → consent 页 → 发授权码（PKCE S256 必需） | OAuth 2.1 |
| `POST /oauth/token` | AS | `authorization_code` + `refresh_token` 两种 grant | OAuth 2.1 |
| `POST /oauth/register` | AS | 动态客户端注册（**兜底**：规范优先 CIMD） | RFC 7591 |
| `POST /oauth/revoke` | AS | 撤销 access/refresh token | RFC 7009 |

**resource indicator**：客户端必须在授权请求与 token 请求里带 `resource=https://<cloud>/mcp/agents/<id>`
（RFC 8707，**MUST**）；我们签发的 token 把该值绑成 audience，RS 侧校验不一致即拒。

## 4. 登录复用：授权端点怎么接现有体系

```
本地 Agent                cloud AS                                 用户
   │  GET /oauth/authorize?client_id=…&redirect_uri=…&code_challenge=…&resource=…
   ├──────────────────────────────▶  有 fastagent_session？
   │                                   ├─ 否 → 302 /login?next=<授权请求原文>
   │                                   │        用户在现有登录页登录（注册/2FA/角色全部继承）
   │                                   └─ 是 → 解析 Identity
   │                                   consent 页：client 名 · 目标 agent · scope(skills:read)
   │                                        （用户点同意 → 记录 grant）
   │  ◀── 302 redirect_uri?code=…&iss=…&state=… ───────────────┤
   │  POST /oauth/token (code + code_verifier + resource)
   ├──────────────────────────────▶  校验 PKCE / client / resource / grant
   │  ◀── access_token(1h) + refresh_token ────────────────────┤
   │  POST /mcp/agents/<id>  Authorization: Bearer <access_token>
   └──────────────────────────────▶  Identity{UserID, Role} + CanAccessAgent(<id>)
```

三条落地规则：

1. **AS 判身份只读 session**：不复制用户表、不做第二套密码校验；`super_admin` / `actAs` / 角色语义自然继承
   （`actAs` 只读态下拒绝签发新 grant）。
2. **consent 每次授权都要展示**：client 标识（CIMD/DCR 注册名）、目标 agent、scope。
   grant 记录键为 `(user_id, client_id, resource, scope)`，复访可跳过页面但不能跳过校验。
3. **登出/禁用即失效**：`POST /api/logout`、改密、禁用用户、撤销 apikey 时一并撤销该用户的 OAuth token
   （挂到现有登出路径上，不新增用户可见概念）。

## 5. 令牌模型与授权语义

- **格式**：不透明 token（DB 行，哈希存储，复用 apikey 的哈希/比对工具）。理由：可即时撤销、与现有 apikey 同构；
  JWT 只在需要跨服务离线校验时才值得引入（见 §9）。
- **生命周期**：access 1h；refresh 30 天，**旋转 + 复用检测**（检测到旧 refresh 再用 → 撤销整条链）。
- **scope（v1 只有一条）**：`skills:read`。skills 出口是只读分发，所以不需要写 scope；
  后续 tools 面（见能力清单）再逐类加 `agents:read` / `tools:call`，并用规范要求的 step-up 授权。
- **授权判定**：`token → Identity{AuthMethod:"oauth", UserID, Role}` → `CanAccessAgent(URL 里的 agent)`；
  与 session / apikey 走同一函数，不新增旁路。
- **兜底路径**：不支持 OAuth 的客户端仍可 `Authorization: Bearer <apikey>`（现有能力），
  文档里明确标注为"兼容模式"，两条路径都受同一 audit 与撤销约束。

## 6. 与现有数据的兼容

**只新增表，不改既有表**：`oauth_clients`（DCR/CIMD 注册结果）、`oauth_authorization_codes`（短 TTL、一次性）、
`oauth_tokens`（access + refresh，哈希存储）、`oauth_consents`（`user × client × resource × scope` 同意记录）。
`users` / `apikeys` / `apikey_agents` / `web_sessions` 一行都不动 —— 因此对已上线部署是纯增量迁移。

回滚面：删掉新表 + 下线 AS 端点即可；apikey 兜底路径始终可用，客户端不会被"锁死"。

## 7. Clean Architecture 四层映射

| 层 | 职责 | 位置 |
| :--- | :--- | :--- |
| Entities | 授权码 / token / client / grant 的不变式：一次性、audience 绑定、PKCE 校验、旋转与复用检测 | `internal/mcpauth/domain/` |
| Use Cases | `RegisterClient`、`BeginAuthorization`（含 session 判定与 consent 决策）、`MintCode`、`ExchangeCode`、`Refresh`、`Revoke`、`ValidateAccessToken` | `internal/mcpauth/usecase/` |
| Adapters（入站） | 上表七个 HTTP 端点；RS 的 Bearer 校验器（给 MCP 端点用） | `internal/mcpauth/http/`，挂进 `internal/setup/server.go` 的 mux |
| Adapters（出站） | 新四张表的读写；`auth.Identity` 适配；session 校验复用 `internal/auth` | `internal/mcpauth/store/` |
| Frameworks | mux、DB 迁移、config（端点开关 / 外部 issuer 名）、audit 日志 | 既有 `internal/setup`、`internal/store` |

依赖规则：`usecase` 不 import `net/http`/`store`；RS 校验器只依赖 `usecase.ValidateAccessToken`；
`internal/mcpauth` 不 import `internal/agent`。

## 8. 安全清单（逐条对应规范）

| 要求 | 落地 |
| :--- | :--- |
| PKCE S256 必选（OAuth 2.1） | 授权请求必须带 `code_challenge`，`plain` 拒绝 |
| redirect_uri 精确匹配 | 只接受注册时登记的值（CIMD 文档里的 `redirect_uris` 或 DCR 注册值） |
| 禁用 implicit / password grant | 只实现 authorization_code + refresh_token |
| resource indicator + audience | 授权与 token 请求都要求 `resource`；token 记录 audience，RS 校验 |
| `iss`（RFC 9207） | 授权响应带 `iss`，元数据里声明 supported |
| CIMD 校验 | 拉取 `client_id` 对应 HTTPS 文档，校验 `client_id` 与 URL 完全一致、必填字段、`redirect_uris` |
| DCR 防滥用 | `registration_endpoint` 限流 + 注册名审计（怀疑滥用可禁用该 client） |
| 不接受未绑定 token | RS 只接受 audience 指向本端点且未过期的 token；不转发/不透传第三方 token |
| 撤销及时性 | 登出 / 改密 / 禁用 / 撤销 apikey → 级联撤销 |
| 日志与 URL | token、code、verifier 永不进日志与 URL；audit 只记 `token_id`、client、agent、结果 |

## 9. 待决策

1. **CIMD 与 DCR 的先后**：规范优先 CIMD（`client_id_metadata_document_supported: true`）+ DCR 兜底。
   要不要为了省事只做 DCR？建议按规范做 CIMD 主、DCR 兜底 —— 客户端（Claude Code 等）会按优先级自动选。
2. **token 格式**：不透明（推荐，撤销即时）vs JWT（跨服务离线校验）。当前只有一个 RS，不需要 JWT。
3. **scope 粒度**：v1 只 `skills:read`。是否现在就把未来 tools 面的 scope 名字定下来，避免以后改名。
4. **resource 粒度**：URL 里带 agent（`/mcp/agents/<id>`）→ audience 天然是"某 agent 的 skill 视图"；
   若以后做用户级并集端点，需要第二套 resource 值。
5. **refresh 有效期**：30 天是否合适（本地 Agent 长期挂机场景）。

## 10. Review 修订（2026-09-18，对着规范与现有代码复查后）

上一版有多处按经验写、没有逐条对规范与代码的地方，复查后修订如下。**这些是必须改的，不是可选优化。**

| # | 修订 | 依据 |
| :-- | :--- | :--- |
| 1 | AS 元数据必须带 `code_challenge_methods_supported: ["S256"]` | 规范：该字段缺失时，合规 MCP 客户端**必须拒绝继续**。上一版漏了 |
| 2 | consent 页必须显示 **redirect URI 的主机名**；`localhost` 类还应额外警示 | 规范 MUST / SHOULD（Localhost Redirect URI Risks） |
| 3 | redirect URI 精确匹配 vs 本地 Agent 的**动态 loopback 端口**：建议对 `127.0.0.1` / `[::1]` 允许 any-port，其余精确匹配 | 规范 MUST 精确匹配；any-port 是 RFC 8252 §7.3 的本地应用惯例。这是唯一一处放宽，必须显式记录并限定在回环地址 |
| 4 | `GET /oauth/authorize` 只渲染；同意走 `POST`，需 CSRF 防护并绑定 session 与 pending 请求 | OAuth 2.1 安全 BCP |
| 5 | 登录后**回到原授权请求**目前做不到：`web/src/components/login-screen.tsx` 没有 `next` 处理 → 需要一个小的 UI 改动，且 `next` 必须同源校验 | 规范 MUST 防 open redirect |
| 6 | AS 必须与登录页**同主机**：`fastagent_session` cookie 是 host-only（无 `Domain`）、`HttpOnly`、`SameSite=Lax`，且当前**没有 `Secure`** | `internal/auth/auth.go`。Lax 允许顶层 GET 导航，正好覆盖授权跳转；云上应补 `Secure` |
| 7 | CIMD 是**我们去 fetch 客户端提供的 URL** → SSRF 面：仅 https、禁私网/回环、限重定向与体积、超时、缓存 | 规范明确要求 AS 考虑 SSRF（Client ID Metadata Document Security） |
| 8 | headless / SSH 场景：规范**没有** device flow，客户端自己负责打印 URL；我们的逃生口是 apikey 兜底 | 规范全文无 device / out-of-band 定义 |
| 9 | RS 必须拒绝任何"不是签发给它"的 token（audience 精确比对，不做归一化） | 规范 MUST（Token Audience Binding and Validation） |

另外两点认知修正：

- **CIMD 不是可选项而是首选**：规范定义客户端优先级为 CIMD → DCR → 预注册。我们若不声明
  `client_id_metadata_document_supported`，客户端才会退到 DCR；两者都不做，只支持静态 header 的客户端才能用，
  其余会卡在授权阶段。
- **不是所有本地 Agent 都实现了 MCP OAuth**。所以"apikey 兜底"不是历史包袱，而是能力矩阵的一部分：
  实现了 OAuth 的客户端走浏览器路径，只支持静态 header 的客户端用 apikey。

## 11. 先做 spike，再写 AS

在写 AS 之前，用最小桩（假授权页 + 假 token 端点 + 两种注册方式各开一次）对真实客户端做一次三问实测：

1. 客户端走 **CIMD** 还是 **DCR**？（决定我们要实现哪一种，还是两种都做）
2. 客户端用的 redirect 端口是**固定**还是**随机**？（决定 §10 第 3 条的 any-port 放宽是否必须）
3. 授权失败时客户端**显示什么**？（决定我们错误页要写哪些字段，以及兜底提示怎么写）

拿到这三个事实再定 AS 的实现范围与顺序：先最小可用（一种注册方式 + 一个 scope + apikey 兜底），
再补另一条注册路径与 step-up 授权。避免先把完整 AS 写完，才发现目标客户端压根不走这条路。

## 12. 通信拓扑：agent → mcp → cloud proxy → fastagent 下的授权边界

### 12.1 资源服务器（RS）在边缘，不在 fastagent

三条理由，任何一条都足以定位 RS：

1. 客户端在授权与 token 请求里带的 `resource` 必须**等于它实际打的那个 URL**（RFC 8707 的 canonical URI），
   否则客户端会拒绝我们签发的 token；
2. RFC 9728 的 `/.well-known/oauth-protected-resource` 必须挂在 **RS 的 URL** 上 —— 客户端正是从它发现 AS；
3. `401 + WWW-Authenticate: …resource_metadata="…"` 挑战必须来自客户端真正打到的那一层。

所以：**边缘（proxy tier）= RS，cloud = AS，fastagent = 被断言身份的内部后端**。
客户端全程只看到两个主机：RS 主机（mcp…）与 AS 主机（app…，即现有登录页所在域）。

### 12.2 内部那一跳复用既有模式（有先例）

现有代码已经确立了"cloud 可信、fastagent 在后"的模式，两处证据：

- `internal/auth` 的 `X-Fastagent-End-User`：**只在 apikey 请求上生效**，把身份重绑到对应 app_user（懒创建）；
  头不合法时不上浮错误，请求退回 key owner 的语义。
- `internal/setup/server.go` 的 MCP OAuth 回调：公开的 GET 只是薄转发，
  真正的 `POST /api/mcp/oauth/callback` 由 **admin key**（`requireSuperAdmin`）调用。

于是内部跳的形态是：**proxy 用服务凭证 + 身份断言调用 fastagent，不向内转发用户的 bearer**。
好处有三：用户 token 不进集群内日志/链路；fastagent 的信任模型保持"只有云端能断言身份"；
撤销仍只需在边缘完成（token 从不下沉）。

**对本设计的影响（重要修正）**：第一版 **fastagent 侧不需要任何 OAuth 代码**。
它只需要一个"信任云端服务凭证 + 接受身份断言"的内部 skill 出口；AS（含 CIMD/DCR、PKCE、token 生命周期）
与 RS（元数据 + 挑战 + audience/scope 校验）都落在边缘那层。
上一版把 `/.well-known/oauth-protected-resource` 与 `/mcp/agents/<id>` 写在 fastagent 侧，随拓扑修正为边缘侧。

### 12.3 内部跳的四条硬约束

| # | 约束 | 理由 |
| :-- | :--- | :--- |
| 1 | 身份断言只在**带有效服务凭证**的请求上生效，且服务凭证按租户/用途限定 | 复刻 `X-Fastagent-End-User` 的既有规则；对内端点绝不能"匿名 + 声称是谁" |
| 2 | 内端点不得从公网可达（网络层 + 凭证层双重） | 否则任何人伪造 `End-User` 头即可读别人 skill |
| 3 | 用户 bearer **不下沉**；若将来要下沉，需单独评审 | 避免 token 进入内部日志与第三方追踪；也避免 fastagent 变成第二个 RS |
| 4 | 两层各自判定，且必须一致：边缘判 `skills:read` + agent 绑定，fastagent 判"这个被断言的用户是否拥有该 agent" | 纵深防御；两侧判定不一致时以更严的一侧为准 |

### 12.4 故障语义（否则客户端会误动作）

| 边缘观测 | 必须返回 | 不能返回 |
| :--- | :--- | :--- |
| 无 token / token 过期 / audience 不符 / scope 不足 | `401`（+ `WWW-Authenticate`，scope 不足时带 `scope=`） | `403`/`500` |
| token 有效，但 fastagent 不可达或报错 | `502`/`503`（body 可区分） | **`401`** —— 客户端会把它当成"需要重新授权"，可能直接丢掉刚拿到的 token |
| token 有效，但该用户无权访问 URL 里的 agent | `403` | `401` |

这条与形式化约定里的 F2/F3 同源：**边缘发出的每个否定都必须说出正确的理由**，否则客户端的动作会错。

### 12.5 两种部署形态（同一份 RS 逻辑，挂在不同侧）

| 形态 | RS 在哪 | 适用 |
| :--- | :--- | :--- |
| **edge-RS（推荐，即本拓扑）** | 边缘 proxy | 多租户云：边缘统一做鉴权、限流、审计；fastagent 保持内网 |
| direct-RS | fastagent 自身 | 自托管 / 无代理部署：fastagent 直接暴露时，它必须自己服务元数据、发挑战、校验 audience |

两种形态下 `resource` 的取值都来自**配置里的公开基址**，而不是运行时的内部地址 —— 否则 token 的 audience
会变成内网 URL，客户端必然拒绝。这条要在实现时就写成断言（配置缺失则拒绝启动，而不是退化成内部地址）。

### 12.8 resource 已冻结：`https://<cloud-host>/mcp/agents/<id>`

**决策（2026-09-18）**：资源标识符是顶层路径 `/mcp/agents/<id>`，**不带 `/api/` 前缀**。
这个值一旦签发过 token 就绑死了，所以下面这套 URL 集合从第一版起就是公开契约：

| 用途 | URL | 说明 |
| :--- | :--- | :--- |
| RS 端点（audience 本体） | `https://<host>/mcp/agents/<id>` | MCP JSON-RPC，POST；将来要 SSE 流再加 GET |
| RS 元数据（裸） | `https://<host>/.well-known/oauth-protected-resource` | 给只探 origin 的客户端 |
| RS 元数据（带路径） | `https://<host>/.well-known/oauth-protected-resource/mcp/agents/<id>` | RFC 9728 对带路径 resource 的规定形式 |
| AS 元数据 | `https://<host>/.well-known/oauth-authorization-server` | RFC 8414；与上两个同主机共存 |
| 授权 / 令牌 / 注册 / 撤销 | `https://<host>/oauth/authorize` `/oauth/token` `/oauth/register` `/oauth/revoke` | AS 端点 |

canonical 形式必须写死：**https、小写主机、无尾斜杠、无默认端口**；RS 侧精确字符串比对，不做任何归一化。
若将来必须换形态，采用双 audience 过渡（RS 在弃用期同时接受新旧，AS 只签新的）。

**落地时与 cloud 现有结构的接缝**（已核对当前代码）：

1. 顶层没有 `/mcp` 路由，不冲突。但旁边已经有一个 `/oauth/mcp/$callbackID/callback`
   —— 那是我们作为 **客户端** 接第三方 MCP server 的回调。两者名字相邻、语义相反，
   文档里必须写清："`/oauth/mcp/*` = 我们连别人；`/mcp/agents/<id>` = 别人连我们"。
2. 它**不走** `/api/fastagent/*` 那条 proxy（那条是转发到 fastagent 的通用管道），
   而是 `src/routes/mcp/**` 下的独立 server route；因此 scope 判定（`skills:read` + agent 绑定）
   写在它自己的 handler 里，而不是塞进 `fastagent-route-access.ts` 的读写表。
   但两者共用同一份凭证解析：`getFastagentCredentials` / `resolveUserCredentials`（§12.2 的 user key 跳）。
3. `.well-known` 在本仓库还没有任何路由，是新增面；两条 protected-resource 元数据返回同一份 JSON。
4. 部署在 Cloudflare Worker 上（`wrangler.toml`），路径从 `/api/*` 挪到顶层后要确认
   WAF / bot 规则 / 缓存策略对 `/mcp/*` 与 `/.well-known/*` 的默认行为（尤其别被 bot-fight 拦掉）。

### 12.6 数据落点的修正

上一版说"只新增四张表"，没说是哪一层的库：**oauth_clients / oauth_authorization_codes / oauth_tokens /
oauth_consents 都在边缘那层（cloud / proxy）的库里**，不进 fastagent 的 `internal/store`。
fastagent 侧保持不变：只有既有身份模型 + 一条新的内部出口。于是回滚也简单：边缘删表下线端点，
fastagent 的那条内部路由关掉即可，apikey 兜底路径始终可用。

### 12.7 这条拓扑顺带解决的两个问题

1. §10 第 6 条的 cookie 约束不再是限制：AS 与登录页都在 app 主机，RS 在 mcp 主机，浏览器只访问 AS 主机，
   host-only cookie 完全够用。
2. 出口端点可与其它云侧能力（限流、审计、计费）共用同一层中间件 —— 见 `mcp-server-capability-map.md`
   §5 的治理清单。
