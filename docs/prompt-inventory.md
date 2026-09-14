# Prompt inventory — every model-facing text in one place

Prompt text in this repo lives in **six** places, none of which knew about the
others. This file is the index; the verbatim text sits in
[`prompt-inventory/`](prompt-inventory/) and is extracted from the source by
`scripts/extract-prompt-inventory.py`.

> **Rule:** touching any prompt means regenerating the snapshot
> (`python3 scripts/extract-prompt-inventory.py`) and updating this index if the
> structure changed. A prompt change without a matching snapshot is the same
> class of debt as a documented test that does not exist.

## Overview

| Class | Where the model sees it | Source | Verbatim |
|---|---|---|---|
| A. System prompt modules | the first `system` message | `internal/agent/prompt_modules.go`, `knowledge.go` | [a-system-prompt.md](prompt-inventory/a-system-prompt.md) |
| B. Per-turn system blocks | `system` messages right after it | `internal/agent/loop.go`, `subagent.go` | [b-per-turn-blocks.md](prompt-inventory/b-per-turn-blocks.md) |
| C. Tool descriptions | the tools schema | `internal/agent/tools/*.go` | [c-tool-descriptions.md](prompt-inventory/c-tool-descriptions.md) |
| D. Tool-result text | inside `tool` results | `tools/*.go`, `internal/sandbox/e2b_executor.go`, `internal/provider/provider.go` | [d-tool-result-text.md](prompt-inventory/d-tool-result-text.md) |
| E. Sub-process prompts | background LLM calls | `compaction.go`, `skills_learner.go` | [e-subprocess-prompts.md](prompt-inventory/e-subprocess-prompts.md) |
| F. Skills | loaded on demand by `load_skill` | `skills/*/SKILL.md`, embedded `internal/agent/bundled_skills/` | [f-skills.md](prompt-inventory/f-skills.md) |

## A. System prompt assembly

Order and composition live in `prompt_modules.go` (`agentModules` :92,
`chatbotModules` :114, `customizeModules` :134, router `modulesForMode` :141);
blocks are joined with `\n\n---\n\n` (`sectionSep` :51).

| # | Module | Line | Lines | Delegates to |
|---|---|---|---|---|
| 0 | `buildDateLine` | 159 | 33 | — (branchy: explicit vs inferred timezone) |
| 1 | `modIdentityAnchor` | 192 | 20 | — |
| 2 | `modAgentIntro` | 219 | 75 | — (branchy: hosted vs self-hosted) |
| 3 | `modBootstrapFiles` | 387 | 44 | `agentBootstrapFiles` :62 / `chatbotBootstrapFiles` :74 |
| 4 | `modKnowledge` | `knowledge.go:45` | — | — |
| 5 | `modMemory` | 431 | 16 | — |
| 6 | `modConfidentiality` | 447 | 31 | — |
| 7 | `modSandbox` | 478 | 98 | — (largest single block) |
| 8 | `modTaskDelegation` | 576 | 5 | `taskDelegationContent` :717 (104 lines) |
| 9 | `modSkills` | 581 | 8 | — |
| 10 | `modGroupChat` | 589 | 18 | — |
| 11 | `modThinking` | 607 | 9 | — |
| 12 | `modToolDiscipline` | 616 | 5 | `toolDisciplineContent` :821 (96 lines) |
| 13 | `modWorkspaceUpdate` | 621 | 6 | `workspaceUpdateContent` :917 (13 lines) |
| 14 | `modIdentityTail` | 696 | 21 | — |

Chatbot mode swaps `agent_intro` for `chatbot_intro` (:294) and
`tool_discipline`/`workspace_update` for `chatbot_tools` (:627); customize mode
runs only `date` + `bootstrap_files` + `memory`.

## B. Per-turn system blocks

| Block | Line | Fires when | Purpose |
|---|---|---|---|
| `renderClientParams` | `loop.go:1710` | client sent params | parameter legend |
| `renderChatbotPersistenceReminder` | `loop.go:1853` | chatbot mode | "you do have cross-session memory" |
| `renderChannelHints` | `loop.go:1917` | IM + split enabled | one bubble per message, split marker (only Chinese example in the corpus) |
| `renderSender` | `loop.go:1960` | group chats | who sent this turn |
| `planModeNudge` + `buildToolCatalogForPlan` | `loop.go:2013` / `:2059` | plan mode | plan first, fan out via `delegate_task` |
| failed-rounds nudge | `loop.go:2551` | N rounds all failed | stop calling tools, answer |
| loop-detected warning | `loop.go:2658` | same call 3× | change approach |
| deferred tool result | `loop.go:2716` | over parallel cap | re-issue next round |
| `capReachedNudge` | `loop.go:3558` | rounds exhausted, no continuation | tools off, synthesize, badge |
| `iterationContinueNudge` | `loop.go:3574` | rounds exhausted **with** progress | keep going, don't repeat |
| `subagentSystemSuffix` | `subagent.go:364` | sub-agent | deliverable only, no preamble |
| `budgetNudge` | `subagent.go:348` | sub-agent wall clock out | deliverable now |
| sub-agent failed-rounds / loop-detected | `subagent.go:168` / `:237` | as above, sub-agent wording | — |

## C. Tool descriptions

Extracted automatically from every `r.Register*` call, so the list is complete
by construction: 24 tool names, with duplicates where the host closure and the
sandbox closure each register the same tool (`exec`, `apply_patch`,
`read_file`, `write_file`, `list_dir`, `edit_file`, `web_fetch`). Test files are
excluded.

## D. Tool-result text

The `[Analyze the error above …]` suffix (`registry.go:864`) applies to every
failed tool; the rest are targeted: the two exec clock hints, the wait refusal,
the sandbox-absence hint, the background-job start line, the `[status] …` /
`[more output pending]` lines, and `provider.StoppedToolResult`
(`provider.go:59`) which the prompt projection injects for an unanswered call.

## E / F. Sub-process prompts and skills

The summarizer and the skills learner are one-shot background calls; the skills
learner reads its instruction from `skills/fastagent-skill-learner/SKILL.md`, so
that file *is* the prompt. Skill text is never duplicated into this inventory:
the source file is the truth, and F lists the paths.

## Maintaining the snapshot

`python3 scripts/extract-prompt-inventory.py` rewrites `prompt-inventory/*.md`:

* literals are joined the way the compiler joins them, `%s`/`%d` placeholders
  stay as placeholders, and each block carries `<!-- source: file:line -->`;
* blocks that choose between strings get a `NOTE: N branch point(s)` marker —
  their text is the branches concatenated in source order, **not** what a single
  call prints. The count is the point: `modIdentityAnchor` (1 arm) reads almost
  exactly like the runtime text, `modAgentIntro` (hosted vs self-hosted) or
  `buildDateLine` (implicit vs explicit timezone) does not;
* `_test.go` files are excluded, so test fixtures never look like product text.

## Review findings (2026-09-14)

1. **No single source of truth** — the same intent is expressed in several
   classes (tool description vs system module vs tool-result hint). This index is
   the first place they are all visible together.
2. **The same fact is written more than once**: two `exec` descriptions (host +
   sandbox closures), two `run_in_background` blurbs, three "budget is gone"
   messages (`capReachedNudge`, `iterationContinueNudge`, `budgetNudge`), two
   loop-detection warnings with different wording.
3. **Language and identity**: the corpus is English except one Chinese example
   in `renderChannelHints`; `renderSender`'s own comment worries that English
   per-turn blocks dilute a Chinese-default SOUL.md.
4. **Weight is concentrated**: `modSandbox` (98) + `taskDelegationContent` (104)
   + `toolDisciplineContent` (96) are ~300 of the ~700 prompt lines; identity is
   bookended by two ~20-line anchors.
5. **Recently unified** (2026-09-14): the wait guard, both exec clock hints and
   the two `run_in_background` descriptions now name exactly one supported way to
   wait, pinned by `TestLongWaitRefusalNamesOnlyTheBackgroundPrimitive` and
   `TestE2BExecClockHints`.
