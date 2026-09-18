# Spike: what do real MCP clients actually do?

A throwaway authorization server + resource server. It answers five questions that decide how much
of the OAuth design (`docs/issues/mcp-oauth-2-design.md`) has to be built, before any of it is built.

Not production code: no database, no login, no rate limiting. It accepts any client and any user.

## The five questions

| # | Question | Why the answer changes the plan |
| :-- | :--- | :--- |
| Q1 | Does the client register through **CIMD** (URL client_id) or **DCR** (`POST /oauth/register`)? | Decides which registration path v1 implements; the spec orders clients CIMD → DCR → pre-registered, but that says nothing about what a given client does |
| Q2 | Is the redirect URI a **fixed** port or a **random** one? | Decides whether "exact redirect URI match" needs the `127.0.0.1` any-port relaxation (RFC 8252 §7.3) |
| Q3 | When authorization fails, **what does the client show**? | Decides which fields our error page must carry and what the fallback hint says |
| Q4 | Does it speak **`server/discover`** or **`initialize`**, and does it send `_meta`? | Decides whether the egress supports both handshakes or only the 2026-07-28 one |
| Q5 | After the token, does it call **`skills/list`** (the extension), **`resources/*`**, or **`tools/*`**? | Decides which of the three compatibility tiers in the egress proposal is actually load-bearing |

Q4 and Q5 fall out of the same run for free: the stub answers every MCP surface and logs which one
gets used.

## Run it

```bash
cd spike/mcp-oauth-stub
go run . -addr 127.0.0.1:8787            # logs one JSON object per line
go run . -fail-authorize                 # for Q3: make /oauth/authorize fail on purpose
go run . -dcr=false                      # for Q1: advertise CIMD only
go run . -cimd=false                     # for Q1: advertise DCR only
```

Point the client at the resource, not at the AS:

```
resource = http://127.0.0.1:8787/mcp/agents/agt_spike
```

Clients that refuse plain `http` for the AS endpoints (the spec requires HTTPS) can be satisfied with
a tunnel; the stub then needs to advertise the tunnel URL:

```bash
cloudflared tunnel --url http://127.0.0.1:8787
go run . -public-url https://<tunnel>.trycloudflare.com
```

## Wiring each client (one at a time, fresh shell)

| Client | How to point it at the stub |
| :--- | :--- |
| MCP Inspector | `npx @modelcontextprotocol/inspector` → transport "Streamable HTTP" → URL `http://127.0.0.1:8787/mcp/agents/agt_spike` |
| Claude Code | `claude mcp add --transport http spike http://127.0.0.1:8787/mcp/agents/agt_spike` then trigger a tool call and follow the browser prompt |
| Codex CLI | add the server to `~/.codex/config.toml` as a streamable-HTTP MCP server with the same URL, then run a prompt that uses it |

## What to record

Copy this table once per client and fill it from the stub's JSONL output, not from memory:

```markdown
### <client> <version> — <date>

| # | Observation | Evidence (log line) |
| :-- | :--- | :--- |
| Q1 | registration path: CIMD / DCR / pre-registered / none | `cimd-fetch` or `dcr-register` |
| Q2 | redirect URI: `http://127.0.0.1:PORT/...` — fixed or random? | `authorize-request.redirectURI` |
| Q3 | failure display: (quote the client's message) | `authorize-request` + the client's own UI |
| Q4 | handshake: `initialize` / `server/discover`; `_meta` present? | `mcp.method` |
| Q5 | consumed surface: `skills/list` / `resources/*` / `tools/*` | `mcp.method` sequence after the token |
| — | did it send `resource` on the token request too? | `token-request.resourceMatches` |
| — | did it send `code_verifier`? | `token-request.codeVerifier` |
| — | which well-known form did it probe? | `http.path` lines |
```

## Exit criteria

The spike is done when each client has a filled row and the plan can say, in one sentence each:

- "v1 implements <CIMD|DCR|both>, because these clients do <…>."
- "The any-port relaxation is <needed|not needed>, because redirect ports are <random|fixed>."
- "The egress serves <skills|resources|tools> first, because that is what clients call."

If a client turns out to do none of it (no discovery, no `resource`, static header only), that is a
result, not a failure: it is the client that has to go through the apikey fallback path.

## Throw it away

When the table is filled, the code goes: the conclusion lives in the design doc. Do not grow this
stub into the real AS — the moment it needs a database or real login, it is a different piece of work.
