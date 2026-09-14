# Tool output limits: nothing a tool returns may be unbounded

Every tool result becomes a message in the conversation, and the conversation
is re-sent on the next model round of the same turn. Unbounded tool output is
therefore not a one-off memory spike — it is a permanent weight on the turn
that carries it.

## The incident

Production, namespace `production`, 2026-09-14. Two gateway pods were
`OOMKilled` (exit 137) inside one hour:

| Pod | Last state | Finished (UTC) |
|---|---|---|
| `fastagent-gateway-585b5dfc76-r86p6` | Terminated / OOMKilled / exit 137 | `2026-09-14T11:05:55Z` |
| `fastagent-gateway-585b5dfc76-8t5hb` | Terminated / OOMKilled / exit 137 | `2026-09-14T08:44:39Z` |

The last line each pod logged before dying was the same shape:

```text
e2b exec completed … outputLen=73031680 … bodyBytes≈97MB
```

Thirty-odd commands on one sandbox (`igjxq8bpvs2a43lq6vznc`) each returned
≈75.7 MB, and consecutive calls differed by **16–228 bytes**. That monotone
creep is the tell: the commands were not big, they were each re-reading the
same *growing* run log (the model checking progress with `ls -la --time-style`,
`pgrep -af`, `tail` of the job log). The pod limit is 1 GiB; a 73 MB string
plus its decoded frames plus the JSON copies around it was enough.

Two properties made this worse than a single spike:

1. The result stays in session history, so every later round of the turn
   re-serializes it (the loop re-sends the whole message list).
2. It never self-corrects. A model that got one 73 MB answer has no reason to
   think the next progress check will be smaller.

## The invariant

> **No tool result leaves its producer without an upper bound.**

Bounded at the source, not at the edge: the place that assembles the bytes is
the place that clips them, so the oversized string is never constructed at all
and no downstream copy (history, SSE event, next request) can see it.

## Where the bound is applied

Head + marker + tail, capped at `sandbox.OutputHeadCap` (64 KiB) and
`sandbox.OutputTailCap` (64 KiB):

```text
[… 70.2 MB of output omitted — the command produced 70.3 MB; showing the first 63.9 KB
   and the last 63.9 KB. Re-run with `tail -n 40`, `wc -l` or `head -c` to see a
   specific part. …]
```

Both ends survive on purpose: the head says what the command *was*, and the
tail usually carries the exit summary. The marker names the real size and the
cheap way to get more, so the model can re-ask narrowly instead of guessing —
`tail -n 40` / `wc -l` are now the trained follow-ups.

| Producer | File | Clip site |
|---|---|---|
| e2b exec stream | `internal/sandbox/e2b_executor.go` | `execOn`, after the frames are joined |
| docker exec | `internal/sandbox/docker.go` | `Exec`, after `CombinedOutput` |
| host exec (sandbox-mode agent) | `internal/agent/tools/exec.go` | after `CombinedOutput` |
| host exec (CLI bridge) | `internal/agent/tools/exec.go` | `registerHostExec` |
| **every** tool, both loops | `internal/agent/loop.go` | the `for … range results` result handler |
| every sub-agent tool | `internal/agent/subagent.go` | the sub-agent result handler |

The last two are a backstop, not the fix. They exist because a producer can be
added without a clip — a plugin tool, an MCP server that returns a dump,
`read_file` on a multi-GB CSV — and the loop is the one place that sees them
all. Producers clip first; the loop catches what they miss.

`bash_output` needs no clip: the background-exec probe already limits one
payload to 64 KiB (`sandboxJobOutputCap`) and reports the unread remainder
separately.

## Observability

One log line per clipping, and only when something was actually dropped:

```text
WARN tool output truncated where=exec/e2b bytes=74792960 kept=131254 omitted=74661706
```

`where` names the producer (`exec/e2b`, `exec/docker`, `exec/host`,
`tool/<name>`, `subagent/<name>`), so a grep for `tool output truncated`
answers "which path is dumping megabytes" without a debug build.

The e2b completion line keeps both numbers: `outputLen` stays the **pre-clip**
size — that is the figure operators watch for a runaway command — and
`resultLen` is what actually left the sandbox. Truncating the log field instead
of the payload would have hidden this incident rather than bounded it.

## What is deliberately not done

* **No config knob.** Two constants, no environment variable. A deployment that
  needs different caps has a design problem, not a tuning problem; and the
  failure mode of a wrong knob (unbounded again) is the one we are fixing.
* **No streaming rewrite here.** Reading the e2b body frame-by-frame and
  emitting what fits (rather than assembling and then clipping) would lower the
  peak further, from "body + decoded + copy" to "within the cap". That is a
  separate change with its own review; it is not needed to stop the OOM.
* **No truncation of the model's own text.** Only tool results. Assistant
  output is bounded by `maxTokens` already.

## Tests

| Test | Pins |
|---|---|
| `TestClipOutputLeavesSmallOutputAlone` | small output is byte-identical, and the cap is inclusive |
| `TestClipOutputKeepsBothEndsAndSaysWhatItDropped` | head + tail survive, the marker carries a unit and points at `tail -n 40` |
| `TestClipAndLogLogsExactlyOnce` | nothing logged when nothing dropped; exactly one line naming the producer otherwise |
| `TestE2BExecClipsHugeOutput` | 400 KB through the real `execOn` frame assembly comes back clipped |
| `TestHostExecClipsHugeOutput` | the host exec path clips too |
| `TestHostExecLeavesNormalOutputAlone` | …without touching normal output |
| `TestToolResultIsClippedBeforeItReachesHistory` | a tool that bypasses every producer still lands in session history clipped, both ends intact |

The last one is the sensitivity check for the loop backstop: with
`ClipAndLog` removed from `loop.go` it fails with
`315020 bytes of tool output were stored unclipped`.

## Where the code lives

* `internal/sandbox/output_clip.go` — `ClipOutput`, `ClipAndLog`, the two caps.
* `internal/sandbox/output_clip_test.go` — the three unit cases.
* `internal/sandbox/e2b_output_clip_test.go` — `execOn` end-to-end.
* `internal/agent/tools/output_clip_unix_test.go` — host exec path.
* `internal/agent/tool_result_clip_test.go` — the loop backstop, through
  session history.

## Advice to the job scripts (not a code change)

The producer that triggered this incident was the model reading a job log
through `exec`. The scripts should stop making that attractive: write
per-item progress to a file (or a single summary line), so no `ls`, `tail` or
`cat` of them can grow without bound. See
[Background execution inside a sandbox](sandbox-background-exec.md) for the
handle-based way to start and observe long jobs.
