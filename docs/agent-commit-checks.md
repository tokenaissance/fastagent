# Agent Commit Checks

Two hooks ask an agent to review work at the boundary where it stops being
private: `.codex/hooks/pre_tool_use.py` (inside a Codex session, before the
commit exists) and `.githooks/pre-push` (before the commits leave the machine).

## Scope: this repo checks one skill, and only this repo has the hooks

`.codex/commit-check.conf` names `clean-architecture` alone. The TanStack skills
belong to `tokenaissance-cloud`, which carries the same two hook files with a
five-skill list; no other repo or folder in the workspace gets these hooks.

Both repos keep the hook scripts byte-identical and differ only in their
`.codex/commit-check.conf`, so a change to the mechanism has to be made twice.
`shasum` the two `pre-push` files if a divergence is ever suspected.

The mechanism is the same in both: **a hook is a shell entry point, so "run a
skill" means shelling out to a headless agent CLI with a prompt that names the
skill.** A hook cannot invoke a skill the way a model does - `SKILL.md` is prose
the model reads, not an executable - so the hook's job is to start an agent that
will.

## What each trigger is for

| Trigger | Fires | Cost | Half of the gate |
|---|---|---|---|
| `.githooks/pre-commit` | every commit, staged `web/**` only | milliseconds | deterministic (eslint `--max-warnings=0`) |
| `.codex/hooks/pre_tool_use.py` | a Codex session is about to run `git commit` | one context injection | instruction |
| `.githooks/pre-push` | `git push`, opt-in | one model call, tens of seconds | judgement |

The pre-commit hook documents its own scope, and the model half is deliberately
kept out of it. A pre-commit slot that can take two minutes is a slot people
learn to bypass with `--no-verify`, and a bypassed hook is worse than no hook.
Push is the last cheap moment instead: the code is finished, the wait is
affordable, and nothing has left the machine.

## Enabling it

Both triggers read `.codex/commit-check.conf` and ship dormant.

```ini
enabled=1          # pre-push runs for every clone; leave empty for opt-in
skill=clean-architecture  # skill(s) the agent is told to run, comma-separated
focus=             # optional one-line extra criteria
```

With `skill` empty, both hooks stay silent. With `enabled` empty, the pre-push
half runs only when asked:

```bash
FASTAGENT_COMMIT_CHECK=1 git push
```

The Codex hook has no `enabled` switch: it costs one line of context and never
blocks, so it is on whenever `skill` is set. Turn it off for one session with
`FASTAGENT_COMMIT_CHECK=0`.

Project hooks in `.codex/hooks.json` are read by Codex but **not trusted by
default** - confirm them once when Codex prompts, or the injection never
arrives.

## What actually blocks

Only an explicit verdict does. The pre-push hook asks the agent to end its
answer with `VERDICT: PASS` or `VERDICT: FAIL: <reason>` and reads that one
line; everything else is a delivery problem, not a review result:

| Situation | Default | `FASTAGENT_COMMIT_CHECK_ON_ERROR=block` |
|---|---|---|
| `VERDICT: FAIL` | push refused, reason printed | push refused |
| `VERDICT: PASS` | push proceeds | push proceeds |
| CLI missing, auth expired, timeout, no verdict line | push proceeds with a warning | push refused |

A hook that converts a network outage into a blocked release is worse than the
check it replaces, which is why the failure mode is open and loud rather than
closed and silent.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `FASTAGENT_COMMIT_CHECK` | from config | `1` forces the pre-push check on, `0` turns both triggers off |
| `FASTAGENT_COMMIT_CHECK_SKILL` | from config | skill name, overriding the file |
| `FASTAGENT_COMMIT_CHECK_CLI` | auto | `codex` or `claude` |
| `FASTAGENT_COMMIT_CHECK_TIMEOUT` | `600` | seconds, when `timeout`/`gtimeout` exists |
| `FASTAGENT_COMMIT_CHECK_ON_ERROR` | `allow` | `block` to fail closed on delivery problems |
| `FASTAGENT_COMMIT_CHECK_MAX_COMMITS` | `20` | how many of the newest commits are reviewed |
| `FASTAGENT_COMMIT_CHECK_MAX_BYTES` | `400000` | diff bytes past which the export is truncated |
| `FASTAGENT_COMMIT_CHECK_ACTIVE` | unset | set by the hook for its own child process; a run that sees it exits immediately |

## The parts that are easy to get wrong

**Recursion.** The hook starts an agent, and that agent can run `git push`. It
also runs inside a Codex session whose PreToolUse hook fires on `git commit`.
Both directions are cut by exporting `FASTAGENT_COMMIT_CHECK_ACTIVE=1` to the
child: the hook sees its own marker and does nothing.

**Staged versus working tree.** An agent reads files, not the index. The Codex
hook therefore points at `git diff --cached`, and the pre-push hook exports the
pushed commits into a temp file and tells the agent to review that file rather
than reconstruct the change set. A dirty checkout would otherwise quietly widen
the review to unstaged work.

**Judging only new problems.** Reviews that report pre-existing issues block
unrelated pushes and teach people to bypass the hook. The prompt scopes the
review to regressions introduced by the pushed commits.

**Non-interactive execution.** `codex exec -s read-only --ephemeral` runs with
no approvals and no session files. Both CLIs are invoked with an explicit
timeout when `timeout`/`gtimeout` is on PATH; without it a stuck CLI hangs the
push, so `FASTAGENT_COMMIT_CHECK_TIMEOUT` is best effort on a stock macOS.

**It is slow, and it is supposed to be.** A real review of a single commit
against `clean-architecture` measured around four minutes end to end. That is
the reason the default timeout is 600 seconds and the reason `enabled=` ships
empty: the Codex half costs a line of context, while this half costs real wall
clock on every push that runs it.

**`--no-verify` always works.** Local hooks cannot be made authoritative. If a
check matters, run the same skill in CI where the flag does not exist.

## Why an agent instead of a script

`pre-commit` stays deterministic on purpose: a linter answers the same question
the same way, in milliseconds, with no auth and no network. The agent half earns
its cost only where judgement is the point - whether a change contradicts its
own commit message, whether a new code path skips an existing guard, whether a
doc claims something the diff no longer does. If a check can be written as a
script, write it as a script and put it in `pre-commit`; the model call is the
last resort, not the first.

Before this is enabled for everyone, the skill it names has to exist and be
worth running. Set `skill=` to nothing and the whole mechanism goes quiet.
