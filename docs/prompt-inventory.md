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
| `failedRoundsNudge` | `loop.go:3608` | N rounds all failed | stop calling tools (main answers the user, sub-agent delivers) |
| `loopDetectedWarning` | `loop.go:3596` | same call 3× | change approach (main) / hand back what you have (sub-agent) |
| deferred tool result | `loop.go:2716` | over parallel cap | re-issue next round |
| `capReachedNudge` | `loop.go:3546` | rounds exhausted, no continuation | tools off, synthesize, badge |
| `iterationContinueNudge` | `loop.go:3562` | rounds exhausted **with** progress | keep going, don't repeat |
| `budgetNudge` | `loop.go:3582` | sub-agent wall clock out | deliverable now |
| `subagentSystemSuffix` | `subagent.go:342` | sub-agent | deliverable only, no preamble |

The three budget messages live in one block (`loop.go:3546-3608`) and are pinned
by `TestBudgetNudgesStateTheirOwnReason`; the two same-shape/two-audience warnings
are `loopDetectedWarning` / `failedRoundsNudge`, pinned by their own cases. They
are **not** merged: a sub-agent must not chat and a continued turn must not
synthesize yet.

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
   per-turn blocks dilute a Chinese-default SOUL.md. **There is no language rule
   anywhere in the corpus today** — SOUL.md is the only place a default language
   is stated — so the question is measurement first: run
   `scripts/prompt-language-audit.sql` (`psql "$FASTAGENT_STORAGE_DSN" -f …`) and
   compare `zh_in_en_out_pct` across channels before touching any wording. The
   query is itself tested against known fixtures by
   `TestPromptLanguageAuditQueryOnFixtures`.
4. **Weight is concentrated** (measured on this snapshot, not estimated):
   `toolDisciplineContent` 5.3K chars + `modSandbox` 4.8K + `taskDelegationContent`
   4.2K ≈ **14.3K of the 23.0K-char agent system prompt (≈3.6K of its ≈5.7K
   tokens, 62%)**, all of it sent on every request. Identity is bookended by two
   ~400-char anchors.
5. **Recently unified** (2026-09-14): the wait guard, both exec clock hints and
   the two `run_in_background` descriptions now name exactly one supported way to
   wait, pinned by `TestLongWaitRefusalNamesOnlyTheBackgroundPrimitive` and
   `TestE2BExecClockHints`.

### Rules with one owner (2026-09-14)

Two rules were stated twice and paid for twice on every request. Both cuts are
pinned by a test that reads the corpus, so a later edit that re-states them fails
the build:

| Rule | Owner (kept) | Copy removed | Pinned by |
|---|---|---|---|
| File delivery: binary output → workspace, reference by path, never inline base64 | `modSandbox › Delivering Files to the User` (always-on) | the `exec` description's 327-char restatement (schema is sent every request) | `TestToolDescriptionsDoNotRestateTheDeliveryRule` + `TestExecDescriptionDoesNotRestateTheDeliveryRule` + `TestExecDescriptionsStayInSync` |
| todo.md procedure (write once, flip with `edit_file`, bare filename) | `taskDelegationContent › Progress tracking via todo.md` | `planModeNudge`'s "first action writes todo.md" sentence (plan mode only) | `TestTodoRulesHaveOneOwner` |
| When to delegate / how to write the `task` arg / the worked example | `taskDelegationContent` (always-on) | ~1.6 K chars of the `delegate_task` schema (it is sent with every request) | `TestDelegateTaskSchemaKeepsCallTimeFacts` + `TestDelegateTaskSchemaStaysUnderItsBudget` |
| web_search-vs-web_fetch routing (when to search, never fetch a search-results page, browser fallback on 403) | `toolDisciplineContent` + `modChatbotTools` | ~0.9 K chars of the `web_fetch` schema (a third copy) | `TestWebFetchSchemaKeepsCallTimeFacts` (asserts the routing text is gone) |
| Background-job contract (`bash_output` / `kill_shell` semantics) | **the tool schemas — there is no other home** | nothing to remove: the corpus mentions `bash_output`/`kill_shell`/`run_in_background` zero times | `TestBashOutputSchemaCarriesTheWholeContract` + `TestKillShellSchemaCarriesItsContract` (locks, not budgets) |
| The five POSIX-only shell limits (`<<<`, `[[ ]]`, process substitution) | `sandboxShellQuirks`, emitted for docker/boxlite only | nothing for e2b: it runs every exec through `/bin/bash -c` (`sandbox/e2b_executor.go`), so the warning was **false** there | `TestSandboxPromptShellQuirksFollowTheBackend` |
| A worked PIL drawing example | `Multi-line Scripts` + `Visual/Graphics Tasks` | the 585-char example itself (it illustrated rules that remain) | `TestSandboxPromptKeepsItsRulesWithoutTheWorkedExample` |

The todo.md rules are load-bearing rather than verbose: the chat panel re-fetches
on every `write_file`/`edit_file` that touches the file and hides itself when the
file is empty (`web/src/components/chat-screen.tsx:517-525`), and the write-once
rule has a traced origin — `04c33d5` *"dedup todo.md items, tighten prompt against
duplicate writes"*, where a second `write_file` stacked an old plan on a partial
new one and the panel showed the same step twice. So the rules stay and the prose
shrank: that section is now **1,540 chars (was 2,212)** with all nine rules
intact, pinned by `TestTodoPromptKeepsEveryRule` and
`TestTodoPromptStaysUnderItsBudget`. Going further would mean dropping a rule, not
words.

### Measured per-request payload (2026-09-14)

Marshaling the real agent's tool definitions — not counting the description
strings, counting what goes on the wire — put the tool schema at **13,159 chars
(≈3.3 K tokens)** with `delegate_task` alone at 3,457 chars, the single most
expensive line in the whole payload. After the trim above:

| | before | after |
|---|---|---|
| `delegate_task` | 3,457 chars | **1,865 chars** |
| `web_fetch` | 1,687 chars | **~760 chars** |
| `modSandbox` (e2b rendering) | 4,764 chars | **3,657 chars** |
| `modSandbox` (docker rendering, keeps the shell quirks) | 4,764 chars | 3,966 chars |
| tool schema (13 tools, real agent) | 13,159 chars ≈ 3.3 K tokens | **10,655 chars ≈ 2.7 K tokens** |
| system prompt + tool schema (e2b, agent mode) | ≈9.0 K tokens | **≈8.1 K tokens** |

For scale: Codex's own harness pays ≈1.9 K tokens of model prompt plus ≈2.3 K of
tool specs (its everyday set), or ≈5.4 K + 2.3 K on a generic model — so
fastagent's fixed overhead now sits between those two configurations.

Two tools were checked and deliberately left alone: `bash_output` and `kill_shell`
carry the entire background-job contract (what "new output since the last call"
means, which status line guarantees completion, how a rolled buffer announces
itself) and the prompt corpus mentions that machinery zero times, so budgeting
them would delete documentation rather than duplication. What did change there is
accuracy: their `bash_id` docs now name both id shapes — a host shell (`bash_3`)
and a sandbox job (`sbg_1a2b_3`).

One more pair of copies is a *maintenance* risk rather than a token cost:
`toolDisciplineContent` (agent mode, 5.4 K chars) and `modChatbotTools` (chatbot
mode, 3.2 K chars) state the same routing / skill / blocked-page rules for their
respective modes. They never appear in one request, but editing one and forgetting
the other is how the two halves of the product drift, so
`TestAgentAndChatbotDisciplineStayInSync` fails when a rule is present in only one
of them.
