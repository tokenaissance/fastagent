# Spike results

Measured 2026-09-18 on this machine, against `mcp-oauth-stub`. Evidence is the stub's JSONL log;
every claim below names the event that shows it.

## Client matrix

| Client | Version | Handshake it used | Revision it negotiated | Methods after connect |
| :--- | :--- | :--- | :--- | :--- |
| Claude Code | 2.1.276 (latest published today) | probes `server/discover`, then falls back to `initialize` | **2025-11-25** | `notifications/initialized`, `tools/list` |
| Codex CLI | 0.150.0-alpha.8 (latest published: 0.155.0) | `initialize` only | **2025-06-18** | `notifications/initialized`, `tools/list` |

Log excerpt (Claude Code):

```
{"event":"mcp","method":"server/discover"}
{"event":"mcp","method":"initialize"}
{"event":"handshake","method":"initialize","requestedVersion":"2025-11-25","answeredVersion":"2025-11-25"}
{"event":"mcp","method":"notifications/initialized"}
{"event":"mcp","method":"tools/list"}
```

Log excerpt (Codex CLI):

```
{"event":"mcp","method":"initialize"}
{"event":"handshake","method":"initialize","requestedVersion":"2025-06-18","answeredVersion":"2025-06-18"}
{"event":"mcp","method":"notifications/initialized"}
{"event":"mcp","method":"tools/list"}
```

## Answers to the five questions

**Q1 — registration path.** Codex CLI used **DCR** (`POST /oauth/register`) even though the stub
advertised `client_id_metadata_document_supported: true`; it sent
`application_type: native`, `client_name: "Codex"`, `grant_types: [authorization_code,
refresh_token]`, and one `redirect_uris` entry. CIMD was not exercised. → v1 must implement DCR;
CIMD stays the spec-preferred path but has no observed client yet.

**Q2 — redirect URI.** `http://127.0.0.1:<random port>/callback/<random id>`, registered through DCR
immediately before the authorization request each time (two logins produced two ports and two path
ids). Because the client registers the exact URI first, exact matching works — **the `127.0.0.1`
any-port relaxation is not needed** for this client. It does mean the AS must accept a fresh
registration per login attempt.

**Q3 — failure display.** Measured with `-deny` (the AS redirects back with
`error=access_denied`, which is the realistic denial shape) and `-fail-authorize`
(the AS returns 403 HTML, which is what the browser shows).

- Codex CLI prints: ``Error: OAuth provider returned `access_denied`: stub:+the+user+denied+this+request+on+purpose``.
  Two things to design around:
  1. **It does not decode the form-encoded `error_description`** — the spaces arrive as `+`. Keep the
     description short and encoding-safe, or accept that the user sees `+`.
  2. After the denial it said `OAuth provider rejected discovered scopes. Retrying without scopes…`
     and re-ran the whole flow with a **fresh DCR registration** (one login attempt produced four
     `dcr-register` + four `authorize-request` events). Our AS must tolerate repeated registrations
     from the same client and must not treat a denial as a scope-negotiation signal.
- Claude Code's `mcp list` does not start an authorization flow: with an auth-required stub it
  reports `! Needs authentication`. So for Claude Code, Q3's user-visible failure text appears inside
  a session, not in the health check.
- With `-fail-authorize` the browser shows the stub's plain body, `stub: authorization denied on
  purpose`, i.e. the client surfaces the AS page verbatim when the AS returns HTML instead of an
  OAuth error redirect. An AS that shows users a real page should therefore always answer with an
  OAuth error redirect first, not a bare 403.

**Q4 — handshake.** Measured, and it contradicts "just make users upgrade": the **latest** Claude
Code negotiates **2025-11-25** and the installed Codex CLI **2025-06-18**. Neither settles on
`2026-07-28` (the revision the Skills extension is written against). Claude Code does probe
`server/discover` first, so it knows the method, but it still completes the session through
`initialize`. → the egress must answer `initialize` at the client's revision *and* `server/discover`;
declaring only 2026-07-28 makes the endpoint unusable for both clients measured today.

**Q5 — which surface.** On connect, both clients called **`tools/list`** — neither asked for
`skills/list` nor `resources/*` while negotiating a 2025-xx revision. → the tools tier
(`list_skills` / `read_skill`) is the load-bearing one for current clients; the skills extension is
the forward-facing path.

**The tools tier was then verified end to end.** In a real Codex session the model called
`spike/list_skills` and answered with the URI the stub returned:

```
mcp: spike/list_skills started
mcp: spike/list_skills (completed)
skill://spike-skill/SKILL.md
```

Stub-side: `{"event":"tools/call"}`, `{"event":"tools-call","name":"list_skills"}`.

One client-side friction worth writing into the user-facing docs: Codex gates MCP tool calls behind
its approval policy. With the default `approval_policy = never`, the call is refused outright —
`MCP tool call requires approval, but approval policy is never` — and the model reports it could not
use the tool. Our tools fallback is therefore not friction-free for every user; the docs should say
which approval setting the server needs.

## Caveats

- ~~The stub's `tools/list` entries lack `inputSchema`~~ fixed: with conformant schemas Claude Code
  2.1.276 reports `✔ Connected`. The earlier `tools fetch failed — Invalid result for tools/list:
  tools.0.inputSchema` was the stub's fault, not the client's.
- Codex's session logs carry `Auth required, when send initialize request` warnings at shutdown even
  with `-no-auth`; they appeared after the session, and the handshake above completed, so they are
  not yet explained. Worth replicating before drawing conclusions about Codex's auth retry behavior.
- One client per family, one machine, one day. Treat this as a floor, not a census.
