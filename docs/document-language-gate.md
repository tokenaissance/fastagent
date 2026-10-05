# Document Language Gate

> 状态：已生效 · 日期：2026-10-05 · 说明只在本页（`scripts/ste-check.sh` 的头注释指向这里）

An agent reads these documents to work on this repo. Nobody resolves a
misreading for it, so a dense sentence costs more here than it costs in a
document that only a human reads. This gate holds the mechanical half of the
`asd-ste100` skill over the documents the repo treats as agent-read. The
judgement half stays with the agent at the commit boundary, and
[agent-commit-checks.md](./agent-commit-checks.md) describes that half.

## 1. What the gate covers

`docs/ste-files.txt` names the files, one glob per line. To put a new family of
documents under the gate, add a glob there. `scripts/ste-check.sh` lints every
match. The scope is authored prose, and the file itself names what stays out
and why, so a reader sees the boundary instead of guessing it. A transcription
of a live prompt is the clearest example: a script regenerates it, so a hand
edit is undone.

## 2. The two classes of finding

The linter is `asd-ste100`'s `scripts/ste-lint.py`. It reports two classes:

- **Hard** — a semicolon, a sentence over the length cap, a soft phrasal verb,
  a nominalization, a marketing adjective, or a rotated synonym. One hard
  finding fails the gate.
- **Advisory** — passive voice, a compound tense, and the Chinese advisory
  rules. An advisory finding never fails the gate. It is a worklist.

A line that contains Chinese gets the Chinese rules in place of the English
ones. There `；` is hard, and sentence length, nominalization, and marketing
adjectives are advisory. The boundary is honest on both sides: ASD publishes no
Chinese edition of STE100, so those rules carry the structural discipline
across, not a dictionary.

## 3. The baseline only goes down

`docs/ste-baseline.txt` records the current hard count for each file. The gate
fails when a file exceeds its recorded count. A new sentence therefore cannot
add a hard finding, and every edit holds the line or improves it.

Two consequences matter:

- A large count is not an approval. It is the size of the worklist.
- After a real improvement, run `scripts/ste-check.sh --update` to tighten the
  baseline. Without that step the improvement is not protected.

## 4. Where it runs

`.githooks/pre-commit` calls `scripts/ste-check.sh --staged` before the web
lint. The check reads only the staged documents, so a commit that touches no
document costs nothing. A missing linter is a warning in that mode, not a
block, so a fresh clone still commits.

## 5. The linter is a dependency, not a copy

`scripts/ste-check.sh` reads the linter from the workspace skill at
`../.agents/skills/asd-ste100/scripts/ste-lint.py`. Set `STE_LINT` to point at
another copy. This repo does not vendor the linter, so the two copies cannot
drift.

## 6. Commands

```bash
scripts/ste-check.sh            # full check over docs/ste-files.txt
scripts/ste-check.sh --staged   # the subset the pre-commit hook reads
scripts/ste-check.sh --list     # hard count per file
scripts/ste-check.sh --update   # tighten the baseline to the current tree
```
