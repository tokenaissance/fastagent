# Background execution inside a sandbox

How a long-running command (batch job, training run, dev server) is started
inside a sandbox, observed from later turns, and stopped — without tying up a
tool call or the turn budget.

## The contract

Three calls, identical in host mode and sandbox mode:

| Call | Meaning |
|---|---|
| `exec({"command": "…", "run_in_background": true})` | start detached, return immediately with a `bash_id` |
| `bash_output({"bash_id": "sbg_1"})` | output printed since the previous call, plus a status line |
| `kill_shell({"bash_id": "sbg_1"})` | SIGKILL the job (and its process group, when it has one) |

Status lines are one of `running`, `exited (code=N)`, `killed`, `lost`.

## Why this exists: the failure that motivated it

Production incident (kronos crypto batch, 2026‑09‑13/14). The model was
driving a job by hand:

```sh
exec({"command": "nohup bash /workspace/job.sh > /tmp/job.log 2>&1 & sleep 175; ls out | wc -l", "timeout": 240})
```

Three runs, three different ways to lose the work:

* `r39`/`r41`: `e2b exec body read: context canceled (got 69 bytes)` — the turn
  budget (plus its grace window) ended the call while `sleep 175` was still
  running. 69 bytes is the start frame and a keepalive: no output at all.
* `r42`: `deadline_exceeded … (got 195 bytes)`, with `525` inside those bytes —
  envd's own deadline hit; the job had *worked*, the *observation* died.
* Every retry had to re-derive progress by listing files, because the shell
  that would have reported the pid was gone.

The mechanism was fine; the missing piece was a **handle the model keeps** —
a pid and a log it can read from a later call. `nohup … &` alone provides
neither, and it also leaves the exec stream open (`nohup` redirects stdout but
not stdin), which is why the call blocks until a server-side deadline instead
of returning immediately.

## Mechanism

A sandbox job is **two files plus the pid that owns them**, all inside the
sandbox:

```
/tmp/fastagent-bg/sbg_1.log    stdout+stderr, append-only
/tmp/fastagent-bg/sbg_1.exit   the exit code, written when the job ends
```

Nothing is added to the `sandbox.Executor` interface: every operation is one
more shell command through the executor's existing `Exec`, so all three
backends (e2b, docker, boxlite) behave identically and the model learns one
contract.

### start

```sh
mkdir -p /tmp/fastagent-bg || exit 1
: > /tmp/fastagent-bg/sbg_1.log
rm -f /tmp/fastagent-bg/sbg_1.exit
__RB=$(command -v setsid 2>/dev/null || true)
if [ -n "$__RB" ]; then
  __G=1
  $__RB sh -c '(<command>)
__rc=$?
printf %s "$__rc" > '"'"'/tmp/fastagent-bg/sbg_1.exit'"'"''"'"' > /tmp/fastagent-bg/sbg_1.log 2>&1 < /dev/null &
else
  __G=0
  ( (<command>)
__rc=$?
printf %s "$__rc" > '/tmp/fastagent-bg/sbg_1.exit' ) > /tmp/fastagent-bg/sbg_1.log 2>&1 < /dev/null &
fi
printf 'fcbg %s %s\n' "$!" "$__G"
```

Three details are load-bearing:

1. **`> log 2>&1 < /dev/null`** — the job holds none of the exec stream's file
   descriptors, so the exec call returns the moment the launcher shell exits.
   This single line is the difference between "returns in 20 ms" and the r39
   `context canceled` hang.
2. **`setsid`** — the job gets its own session, so it survives the exec request
   ending and `kill -KILL -pid` can reach the whole tree, not just the wrapper.
   Images without setsid (a minimal busybox) fall back to a backgrounded
   subshell; the launcher reports which form it used (`__G`) so kill never
   signals a process group that isn't ours.
3. **the subshell `( … )`** — makes `$?` the status of the caller's command
   whatever shape it had (pipeline, `&&` chain, `cd`), and keeps a trailing
   comment from eating the exit-code line.

Every interpolated path goes through single-quote escaping, so a command
containing quotes, newlines or `$(…)` reaches the job literally.

### poll

```sh
__S=$(wc -c < /tmp/fastagent-bg/sbg_1.log 2>/dev/null || echo 0)
if [ -f …/sbg_1.exit ]; then __ST=exited; __C=$(cat …/sbg_1.exit); else __ST=running; __C=-; fi
printf '\037fcbg %s %s %s\037' "$__ST" "$__S" "$__C"
tail -c +<cursor+1> …/sbg_1.log 2>/dev/null | head -c 65536
```

The status marker is wrapped in `0x1F` so arbitrary log content can never be
mistaken for protocol, the size is read before the tail so the bytes it names
are stable in an append-only file, and the cursor advances only past the bytes
actually returned (one poll is capped at 64 KiB; the caller is told how much is
still unread).

**Completion is decided by the exit file, never by `kill -0`.** A finished
process that nobody reaped is a zombie, and `kill -0` succeeds on zombies —
verified on a real container where a long-gone job's pid still answered
`kill -0` and had been handed to an unrelated process.

### kill

```sh
kill -KILL -<pid>     # own session (verified: wrapper, subshell and command all die)
# or, without setsid:
pkill -KILL -P <pid>; kill -KILL <pid>
```

SIGKILL, not SIGTERM: a wrapper shell blocked in `wait` defers SIGTERM until its
child exits, so a TERM'd job can keep running — observed on the docker backend.

## Why not e2b's `background: true`

e2b has a first-class background flag, and it is a good primitive — but:

* it exists on one backend out of three, so the model would have to learn a
  different contract per backend for the same intent;
* the stream it returns is delivered only to the process that started the
  command. e2b's own cross-process recipe is "start in background **and
  redirect the output to a file**" ([commands/background](https://docs.e2b.dev/commands/background)),
  because a reconnecting caller sees only output produced after it reconnects;
* the file is therefore the part that does the work, and this composition
  produces it on every backend while making the call return immediately.

If e2b's envd turns out to reap detached sessions when the exec request ends
(unverified: it is outside our account's observable behaviour), the first
symptom is `TestSandboxBackgroundE2BLive` failing, and the fix is one field on
that one request — the log/cursor/exit-file machinery stays as it is.

## Limits (deliberate, and visible to the model)

| Limit | Why it is acceptable | Signal the model gets |
|---|---|---|
| Log lives in `/tmp`, not `/workspace` | `/workspace` is flushed to durable storage after *every* exec; a growing log there would be re-uploaded on every poll and would pollute the Files panel | start message prints the path |
| Log dies with the sandbox | A job cannot outlive the kernel it runs in | `[status] lost` |
| No exit code when the job is killed from outside (OOM killer, sandbox kill) | the wrapper never gets to write it | `[status] running` with no growth — confirm with `exec({"command":"ps"})` |
| Job table is per replica (in memory) | a later turn on another replica cannot see `bash_id`; the log path from the start message still works via `exec({"command":"tail /tmp/fastagent-bg/sbg_1.log"})` | `no such bash_id` |
| One poll ≤ 64 KiB | bounds a single tool result | `[more output pending: N bytes unread]` |

Revisit the per-replica row the moment cross-replica polling is actually
needed: persisting the job row (id, pid, log path, owner) in the store is the
same shape as the sandbox lease row.

## Where the code lives

| Layer | File | Role |
|---|---|---|
| Tool surface | `internal/agent/tools/exec.go` | `run_in_background` on the sandbox closure; tool descriptions |
| Use case | `internal/agent/tools/sandbox_background.go` | launcher/probe/kill composition, job table, status semantics |
| Tool surface | `internal/agent/tools/bash_tools.go` | `bash_output` / `kill_shell` fall back to the sandbox job table |
| Wiring | `internal/agent/tools/registry.go` | job table lifecycle (created with the registry; deliberately NOT killed on `Close`) |
| Driver | `internal/sandbox/*` | unchanged — the feature rides on the existing `Exec` |

## Tests

Unit (`internal/agent/tools/sandbox_background_test.go`) — the wire shapes and
job lifecycle, no sandbox involved:

`TestBackgroundLaunchCommandDetachesTheJob`,
`TestBackgroundLaunchCommandQuotesHostileCommand`,
`TestBackgroundProbeCommandReadsFromTheCursor`,
`TestParseBackgroundLaunch`, `TestParseBackgroundProbe`,
`TestSandboxJobOutputReturnsDeltaThenStatus`,
`TestSandboxJobOutputReportsUnreadTail`,
`TestSandboxJobOutputHandlesReplacedSandbox`,
`TestSandboxJobOutputReportsKill`,
`TestSandboxJobKillTargetsTheProcessGroup`,
`TestSandboxJobsStartRegistersResolvableJob`,
`TestSandboxJobsStartFailsLoudly`.

Real shell, no sandbox (`sandbox_background_unix_test.go`) — the composed
commands run against a local `sh`, so the mechanism is verified offline:

`TestSandboxBackgroundMechanismJobOutlivesTheCall` (the r39 regression: the
call must return before the job does),
`TestSandboxBackgroundMechanismCapturesExitCode`,
`TestSandboxBackgroundMechanismKillStopsTheTree`,
`TestSandboxBackgroundMechanismFallsBackWithoutSetsid`.

Tool contract (`sandbox_background_tool_test.go`):

`TestExecRunInBackgroundInSandboxMode` (start → poll → kill through the tool
surface, and the job never touches the host),
`TestExecRunInBackgroundInSandboxModeInjectsSkillEnv`,
`TestExecRunInBackgroundWithoutExecutorFailsClearly`.

Live (`sandbox_background_e2e_test.go`) — wired into `.github/workflows/go-test.yml`
(`sandbox-background` job for docker; the credential-gated job for e2b):

`TestSandboxBackgroundDockerE2E` (real container: the job ticks across three
independent tool calls, the delta never repeats, kill silences the tree, and a
failing job reports `exited (code=3)`),
`TestSandboxBackgroundE2BLive` (the production backend: the job must still be
running on a later call).
