# Chat event delivery: placement — session-key affinity, not a fan-out relay

> **Status**: decided · 2026-09-24 · in force, **not implemented** (nothing in the
> tree sends or reads the affinity header yet — §7 lists the landing order)
> **Scope**: *which pod* a session's requests land on, and why that is a delivery
> input at all. The mechanism that carries events across pods is
> [chat-event-delivery.md](./chat-event-delivery.md).
> **Authority**: this is the single source for the placement decision (affinity
> vs. a fan-out relay). Where another document disagrees, this one wins.
> **Related**: [chat-event-delivery.md](./chat-event-delivery.md) §4 D4 (why
> `content_delta` is not persisted) · [session-turn-integrity.md](./session-turn-integrity.md)
> (the lease that already serialises one session) · `internal/agent/event_hub.go`.

## 1. Why placement is a delivery input

The hub is in-process, and says so:

> In-memory only — multi-pod deploys need to swap this for redis pub/sub or
> similar. (`internal/agent/event_hub.go:22`)

The sibling doc answers that with the DB tail: every **persisted** event reaches a
subscriber on any pod, ≤500 ms late (`chatEventTailInterval`,
`internal/setup/chat_event_tail.go:35`). One type is deliberately not persisted —
one row per token would dwarf the table (`internal/agent/events.go:70`) — and the
tail is not allowed to carry it (`internal/setup/handlers.go:1615`, `:1626`). So
the split is:

| Event | Persisted (`seq ≥ 0`) | Cross-pod today | Needs placement? |
|---|---|---|---|
| `tool_call` / `tool_result` / `queued` / `turn_active` / `done` | yes | tail replays it, ≤500 ms | no |
| `content_delta` | **no** (`seq = −1`) | **lost** | **yes** |

So "which pod does the subscriber run on" is not a capacity question first: it is
the only thing that decides whether a watching tab sees the answer stream in, or
appear at once at `done`.

Nothing chooses it today. The browser never talks to fastagent directly:

```
browser → cloud proxy → ingress → Service → a pod (round-robin)
```

* the proxy **rebuilds** the request headers, keeping only `Authorization` and
  `Content-Type` (`tokenaissance-cloud`, `src/routes/api/fastagent/$.ts:249`), so
  the ingress's cookie affinity never applies;
* it **rebuilds** the SSE response with three fixed headers (`:286`), so the
  upstream `Set-Cookie` never reaches the browser either.

Net: two browsers signed in as the same user, watching the same session, sit on
two pods — and a turn's `content_delta` reaches only the one that happens to share
the runner's pod.

## 2. Decision

**The standard practice for cross-replica fan-out is a pub/sub broadcast** (Redis,
or any equivalent relay): one publisher, N subscribers, and the subscriber's pod
stops mattering — it receives every event regardless of who produced it. That is
what the hub's own comment names, and it is what we would reach for in a system
that already runs Redis.

**We are not adding Redis.** These deployments have no Redis today — no service, no
`FASTAGENT_REDIS_*` in the environment or the ConfigMap. Buying a new stateful
component (a new deploy dependency, a new failure mode, a new thing to operate and
to page on) to recover one live-only field is the wrong trade while the durable
half already has a transport (§1). Keeping the ops surface small is worth more than
the typewriter effect.

**We affinity-pin the session instead.** Every request belonging to one session —
the POST that runs the turn, and the `subscribe` of every tab watching it — lands
on one pod, so the in-process hub is enough and no cross-process hop exists.

Two notes that belong next to that decision, because they are the parts most
likely to be re-litigated:

* **Why the key is the session, not the user.** A session key is strictly finer:
  more buckets, less skew (§5). It is also the identity the hub fans out on
  (`hubKey(userID, agentID, sessionKey)`, `internal/agent/event_hub.go:78`), and
  the identity every participant in that conversation already shares — a second
  browser and a share-link viewer are on the same session without being the same
  client.
* **Why not the cookie affinity the ingress already has.** It is per-browser, not
  per-session: two browsers are two cookies, therefore two pods — precisely the
  case in §1. And per §1 it is inert on our path anyway.

### Rejected alternatives

| Option | Why not |
|---|---|
| Redis pub/sub relay | the standard answer; deferred **for now** on ops cost (§2), not on capability |
| Persist `content_delta` | one row per token — the wrong shape for the table; already decided in the sibling doc's D4 |
| Cookie affinity (already configured) | per-browser granularity, and inert through the proxy (§1) |
| Affinity keyed on the user | fewer buckets ⇒ worse skew; and a share-link viewer's request carries the *owner's* credentials, so the key would have to be the credential owner, not the caller |
| Route the subscriber to the holder ("read follows the lease") | `holder_id` is `"<pod>/<random>"`, minted per acquisition — a **signature, not an address** (`internal/agent/sessionlease.go:126`); pod names change on every rollout and nothing can address one pod |
| Do nothing | a long-lived `EventSource` never re-runs the one-shot replay, so the gap is permanent until a manual reload (sibling doc §1) |

## 3. The mechanism, when it lands

1. **client / cloud**: every fastagent call carries `X-Fastagent-Session: <sessionId>`.
   Today the id lives in the stream POST's body and in the subscribe/history query
   string; one header unifies both.
2. **cloud proxy**: forward that header — the same place it sets `Authorization`
   (`forwardRequest`).
3. **ingress**: `nginx.ingress.kubernetes.io/upstream-hash-by: "$http_x_fastagent_session"`,
   and drop the three cookie-affinity annotations (wrong granularity, and inert).
4. **server**: drop the two live-only skips on the subscription path so deltas are
   forwarded. This half is testable in-process: the two-replica harness in
   `internal/setup/chat_event_delivery_e2e_test.go` already subscribes on B while
   A runs the turn — its `content_delta` assertion flips from "must not arrive" to
   "must arrive".
5. **client**: render `content_delta` in the subscribe handler, and split the flag
   that today means both "this tab POSTed" and "this tab is receiving".

Step 4 is the one with a red test before it. Steps 1–3 are infrastructure and are
verified against a live cluster: open the same session in two browsers and check
that both subscriptions report the same holder.

## 4. What affinity does not cover

| Producer | Covered? | Why |
|---|---|---|
| Browser `POST /chat/stream` | ✅ | goes through the proxy with a session id |
| Any tab's `chat/subscribe` | ✅ | same session ⇒ same pod |
| `delegate_task` sub-agent events | ✅ | they run inside the lease holder's process |
| Empty-session client paths (IM/channel turns, project-level runtime calls) | ⚠️ partial | no session id to hash on; their deltas stay same-pod-only |
| **cron / goal ticks** | ❌ | the producer is whichever pod wins `LockCronJob` — a DB race (`internal/cron/scheduler.go:228`), unrelated to any client identity |
| Rolling deploy | ❌ | every pinned stream breaks when its pod is replaced; the tail is the recovery path |

## 5. What it costs: the capacity consequence

Measured on prod, 2026-09-24 (`kubectl get deploy fastagent-gateway -n production
-o jsonpath='{.spec.template.spec.containers[0].resources}'`, `kubectl top pods`):

| Fact | Value |
|---|---|
| replicas / HPA | 2 (min 2, max 10), `averageUtilization: 60` |
| CPU requests / limits | `250m` / `2` — an **8× burst ratio** |
| idle usage | 1m per pod |

Read together: the HPA target is 150m per pod, i.e. **1.5 cores of aggregate usage
reaches max=10**. With an 8× burst ratio one pegged pod drags the ten-pod average
to ~98%, so "the average hides the hotspot" is *not* the binding problem here. The
binding problem is that **a new replica only takes new sessions** — a hash never
re-reads load. Capacity therefore is *peak concurrent heavy sessions*, and the knob
is `minReplicas`, not the HPA target.

| Lever | How | Cost | Solves |
|---|---|---|---|
| Finer key | session rather than user (§2) | free | reduces skew (more buckets), never removes it |
| Make scaling see the skew | fix `requests` to a real steady state; or scale on **max in-flight turns per pod** instead of mean CPU | one measurement + tuning | growth by *new sessions*; never a single heavy session |
| Admission + fairness | per-pod in-flight cap (the UI already has the `Queued` state) plus a per-**uid** cap so one heavy user cannot starve another on a shared pod | one middleware at the turn lease — which is already the single-writer gate | turns "this pod is pegged" into "new turns queue" — a bounded, visible degradation |
| Drop affinity | a fan-out relay (Redis pub/sub) or persisted deltas | a new component / a new table shape | the only option that lets routing return to plain round-robin |

The missing measurement is the one that would settle this: **per-pod skew** (max
vs. mean CPU, and in-flight turns per pod) is not exported anywhere today, so the
cost of the choice in §2 is currently unobservable.

## 6. Reopen triggers

Affinity stays the decision until one of these is true:

* per-pod max/mean CPU skew stays above a chosen ratio for a full day (needs §5's
  missing metric to exist first);
* peak concurrent heavy sessions approaches `minReplicas`, i.e. a session waits on
  `queued` for capacity rather than for its own sibling turn;
* Redis arrives for an unrelated reason — then the relay's marginal cost collapses
  and the standard practice (§2) is better than a placement constraint;
* `content_delta` stops being the only live-only type: a second one would be
  evidence that the fan-out gap is structural rather than cosmetic.

## 7. Status of the work

Nothing is implemented: no header, no ingress annotation, no server change, no
client change. The landing order is steps 1 → 5 in §3, red case first, one file per
commit. Until then the behaviour is exactly what
[chat-event-delivery.md](./chat-event-delivery.md) describes: persisted events
arrive cross-pod, `content_delta` does not.
