# Spike: the session lease rows

A one-off Go program. It drives the **shipped** `store` code against a real Postgres and reports
what the `session_turns` table does. It backs the live measurement recorded in
`docs/fs-formal-proof/11-change-register.md` row 111.

The program uses synthetic keys (`probe-row111`) that no turn uses, and it deletes its own rows at
the end. It writes to no other table.

## What it checks

Six assertions, in order:

1. A fresh acquisition takes the row with `epoch = 1` and `cancel_requested = 0`.
2. `RequestSessionCancel` stamps the live possession.
3. A lapsed row is invisible to `GetSessionLease`, and a late stop request is refused.
4. **The clause under test.** A takeover of the lapsed row starts at `cancel_requested = 0`, so the
   stamp of the previous possession does not leak into the new one.
5. A stale release cannot free the row of the current possession.
6. The guarded release of the current possession removes the row.

The last line is `ALL-OK` when all six hold.

## Run it

Run it **inside a gateway pod**. The DigitalOcean pooler speaks plain Postgres inside the cluster,
so a laptop needs TLS and fails. The pod environment already carries `FASTAGENT_STORAGE_DSN`.

```bash
# build for the pod's architecture and copy it in
GOOS=linux GOARCH=amd64 go build -o /tmp/lease-row-probe ./spike/lease-row-probe
kubectl --context do-nyc2-tokenaissance-nyc2 -n production cp \
  /tmp/lease-row-probe <gateway-pod>:/tmp/lease-row-probe
kubectl --context do-nyc2-tokenaissance-nyc2 -n production exec <gateway-pod> \
  -- /tmp/lease-row-probe
```

`PROBE_DSN` overrides the DSN. An empty `PROBE_DSN` falls back to `FASTAGENT_STORAGE_DSN`.

## Watch mode

`-watch 30s` prints the live lease rows once a second. Use it to watch a turn that runs through
the API from the outside: when a row appears, when its epoch moves, and when it goes away.

```bash
kubectl --context do-nyc2-tokenaissance-nyc2 -n production exec <gateway-pod> \
  -- /tmp/lease-row-probe -watch 30s
```
