#!/usr/bin/env python3
"""Extract every model-facing prompt string into docs/prompt-inventory/.

The prompt text of this repo lives in six places (see docs/prompt-inventory.md):
system-prompt modules, per-turn system blocks, tool descriptions, tool-result
text, sub-process prompts, and skills. Nobody can review that in one sitting
from the source alone, and somebody has to notice when the wording drifts.

This script writes the VERBATIM text — Go string literals joined the way the
compiler joins them, with `%s`/`%d` placeholders left in place — into
docs/prompt-inventory/*.md, each block carrying its `file:line` provenance.

Regenerate after touching any prompt:

    python3 scripts/extract-prompt-inventory.py

Then update docs/prompt-inventory.md if the inventory's structure changed (a new
tool, a new module, a new nudge).
"""

import os
import re
import subprocess

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "docs", "prompt-inventory")


def read(rel):
    with open(os.path.join(ROOT, rel), "r", encoding="utf-8") as fh:
        return fh.read().split("\n")


def join_lines(lines):
    return "\n".join(lines)


def find_block(text, lines, symbol):
    """Return (startIdx, endIdxExclusive) of a top-level func/const/var."""
    # Methods carry a receiver: `func (j *sandboxJob) startedMessage() string`.
    pattern = re.compile(
        r"^(func\s+(\([^)]*\)\s*)?|const\s+|var\s+)" + re.escape(symbol) + r"\b"
    )
    for idx, line in enumerate(lines):
        if not pattern.match(line):
            continue
        depth = 0
        started = False
        for j in range(idx, len(lines)):
            depth += lines[j].count("{") + lines[j].count("(") + lines[j].count("[")
            depth -= lines[j].count("}") + lines[j].count(")") + lines[j].count("]")
            if not started and depth > 0:
                started = True
            if started and depth <= 0:
                return idx, j + 1
            if not started and j > idx + 40:  # multi-line signature guard
                break
        return idx, min(len(lines), idx + 60)
    raise SystemExit("symbol not found: %s" % symbol)


def unescape(go_str):
    out = []
    i = 0
    while i < len(go_str):
        c = go_str[i]
        if c != "\\":
            out.append(c)
            i += 1
            continue
        i += 1
        if i >= len(go_str):
            break
        e = go_str[i]
        simple = {"n": "\n", "t": "\t", "r": "\r", '"': '"', "\\": "\\", "'": "'"}
        if e in simple:
            out.append(simple[e])
            i += 1
        elif e == "x":
            out.append(chr(int(go_str[i + 1 : i + 3], 16)))
            i += 3
        elif e == "u":
            out.append(chr(int(go_str[i + 1 : i + 5], 16)))
            i += 5
        else:
            out.append(e)
            i += 1
    return "".join(out)


def literals(source):
    """Every Go string literal in source, in order, as the compiler sees them."""
    found = []
    i = 0
    while i < len(source):
        c = source[i]
        if c == "`":
            j = source.find("`", i + 1)
            if j < 0:
                break
            found.append(source[i + 1 : j])
            i = j + 1
        elif c == '"':
            j = i + 1
            while j < len(source):
                if source[j] == "\\":
                    j += 2
                    continue
                if source[j] == '"':
                    break
                j += 1
            found.append(unescape(source[i + 1 : j]))
            i = j + 1
        elif c == "'":
            j = source.find("'", i + 1)
            i = (j + 1) if j > 0 else i + 1
        elif c == "/" and i + 1 < len(source) and source[i + 1] == "/":
            i = source.find("\n", i)
            if i < 0:
                break
        else:
            i += 1
    return found


def block_text(rel, symbol):
    lines = read(rel)
    start, end = find_block(join_lines(lines), lines, symbol)
    body = join_lines(lines[start:end])
    text = "".join(literals(body))
    return start + 1, text, branchy(body)


def branchy(body):
    """True when a block picks between strings — the joined text is then a rough
    rendering (branches concatenated in source order), not what any single call
    prints. Labelled so nobody quotes it as the runtime wording."""
    for line in body.split("\n"):
        stripped = line.strip()
        if stripped.startswith("if ") or stripped.startswith("switch ") or stripped.startswith("} else"):
            return True
    return False


def range_text(rel, start, end):
    lines = read(rel)
    body = join_lines(lines[start - 1 : end])
    return "".join(literals(body)), branchy(body)


def section(title, blocks, out):
    chunks = ["# %s\n" % title]
    for label, loc, text, note in blocks:
        if not text.strip():
            text = "(no literal text in this block — it delegates to a constant listed above/below)"
        chunk = "## %s\n\n<!-- source: %s -->\n" % (label, loc)
        if note:
            chunk += "\n<!-- NOTE: branchy block — literals concatenated in source order, not as printed -->\n"
        chunk += "\n```text\n%s\n```\n" % text.strip("\n")
        chunks.append(chunk)
    with open(os.path.join(OUT, out), "w", encoding="utf-8") as fh:
        fh.write("\n".join(chunks))
    print("%-32s %5d chars" % (out, sum(len(c) for c in chunks)))


def symbols(rel, names):
    out = []
    for name in names:
        line, text, note = block_text(rel, name)
        out.append((name, "%s:%d" % (rel, line), text, note))
    return out


def registers(rel_glob):
    """Every `r.Register(name, description, ...)` as (name, description source)."""
    files = [
        f
        for f in subprocess.check_output(["git", "-C", ROOT, "ls-files", rel_glob], text=True).split()
        if not f.endswith("_test.go")
    ]
    found = []
    for rel in files:
        src = join_lines(read(rel))
        for m in re.finditer(r"r\.Register(?:Serial)?\(\s*(\"(?:\\.|[^\"])*\")\s*,\s*", src):
            name = unescape(m.group(1)[1:-1])
            i = m.end()
            depth = 0
            j = i
            while j < len(src):
                ch = src[j]
                if ch in "([{":
                    depth += 1
                elif ch in ")]}":
                    if depth == 0:
                        break
                    depth -= 1
                elif ch == '"':
                    j += 1
                    while j < len(src) and src[j] != '"':
                        j += 2 if src[j] == "\\" else 1
                elif ch == "`":
                    j = src.find("`", j + 1)
                elif ch == "," and depth == 0:
                    break
                j += 1
            expr = src[i:j].strip()
            line = src[: i - len(expr)].count("\n") + 1
            if re.fullmatch(r"[A-Za-z_]\w*", expr):
                try:
                    sym_line, text, note = block_text(rel, expr)
                    found.append((name, "%s:%d (via %s)" % (rel, sym_line, expr), text, note))
                    continue
                except SystemExit:
                    pass
            found.append((name, "%s:%d" % (rel, line), "".join(literals(expr)), branchy(expr)))
    return found


def main():
    os.makedirs(OUT, exist_ok=True)
    pm = "internal/agent/prompt_modules.go"

    section(
        "A. System prompt modules (verbatim, in assembly order)",
        symbols(
            pm,
            [
                "buildDateLine", "modIdentityAnchor", "modDateOnly", "modAgentIntro",
                "modChatbotIntro", "modBootstrapFiles", "modMemory", "modConfidentiality",
                "modSandbox", "modTaskDelegation", "modSkills", "modGroupChat",
                "modThinking", "modToolDiscipline", "modWorkspaceUpdate", "modChatbotTools",
                "modIdentityTail", "taskDelegationContent", "toolDisciplineContent",
                "workspaceUpdateContent", "agentBootstrapFiles", "chatbotBootstrapFiles",
            ],
        ) + symbols("internal/agent/knowledge.go", ["modKnowledge"]),
        "a-system-prompt.md",
    )

    section(
        "B. Per-turn system blocks and nudges",
        symbols(
            "internal/agent/loop.go",
            [
                "renderClientParams", "renderChatbotPersistenceReminder", "renderChannelHints",
                "renderSender", "planModeNudge", "buildToolCatalogForPlan", "capReachedNudge",
                "iterationContinueNudge",
            ],
        )
        + [("failed-rounds nudge", "internal/agent/loop.go:2551-2561", *range_text("internal/agent/loop.go", 2551, 2561))]
        + [("loop-detected warning", "internal/agent/loop.go:2658-2666", *range_text("internal/agent/loop.go", 2658, 2666))]
        + [("deferred tool result", "internal/agent/loop.go:2716-2721", *range_text("internal/agent/loop.go", 2716, 2721))]
        + symbols("internal/agent/subagent.go", ["subagentSystemSuffix", "budgetNudge"])
        + [("subagent failed-rounds nudge", "internal/agent/subagent.go:168-178", *range_text("internal/agent/subagent.go", 168, 178))]
        + [("subagent loop-detected warning", "internal/agent/subagent.go:237-244", *range_text("internal/agent/subagent.go", 237, 244))],
        "b-per-turn-blocks.md",
    )

    section("C. Tool descriptions (the schema the model reads)", registers("internal/agent/tools/*.go"), "c-tool-descriptions.md")

    def sym(rel, symbol, label, note_override=None):
        line, text, note = block_text(rel, symbol)
        return (label, "%s:%d" % (rel, line), text, note if note_override is None else note_override)

    section(
        "D. Text injected into tool results",
        [
            ("error suffix on every failed tool", "internal/agent/tools/registry.go:864", *range_text("internal/agent/tools/registry.go", 864, 864)),
            sym("internal/agent/tools/exec.go", "longWaitRefusal", "long foreground wait refusal"),
            sym("internal/sandbox/e2b_executor.go", "execCancelledHintText", "exec cancelled hint", False),
            sym("internal/sandbox/e2b_executor.go", "execStalledHint", "exec stalled hint"),
            ("sandbox-absence hint", "internal/agent/tools/exec.go:922-941", *range_text("internal/agent/tools/exec.go", 922, 941)),
            sym("internal/agent/tools/sandbox_background.go", "startedMessage", "background job started"),
            ("background poll status lines", "internal/agent/tools/sandbox_background.go:330-360", *range_text("internal/agent/tools/sandbox_background.go", 330, 360)),
            ("interrupted-call placeholder", "internal/provider/provider.go:59", *range_text("internal/provider/provider.go", 59, 59)),
        ],
        "d-tool-result-text.md",
    )

    section(
        "E. Sub-process prompts (background calls the user never sees)",
        [
            ("conversation summarizer", "internal/agent/compaction.go:181-190", *range_text("internal/agent/compaction.go", 181, 190)),
            ("skills learner wrapper + JSON discipline", "internal/agent/skills_learner.go:137-141", *range_text("internal/agent/skills_learner.go", 137, 141)),
        ],
        "e-subprocess-prompts.md",
    )

    skills = sorted(os.listdir(os.path.join(ROOT, "skills"))) if os.path.isdir(os.path.join(ROOT, "skills")) else []
    bundled = sorted(os.listdir(os.path.join(ROOT, "internal/agent/bundled_skills"))) if os.path.isdir(os.path.join(ROOT, "internal/agent/bundled_skills")) else []
    body = "# F. Skills (the source files are the prompt)\n\n" \
        "Skill text is not a Go string: it is `SKILL.md` inside each skill directory, loaded on\n" \
        "demand by `load_skill` and summarised into the system prompt by the `skills` module.\n" \
        "Reviewing a skill means reviewing its file; nothing is duplicated here on purpose.\n\n" \
        "## repo `skills/` (shipped with the binary)\n\n" + "".join("- `skills/%s/`\n" % s for s in skills) + \
        "\n## embedded `internal/agent/bundled_skills/` (injected into every agent home)\n\n" + "".join("- `internal/agent/bundled_skills/%s`\n" % s for s in bundled) + \
        "\n## the skills-learner prompt\n\n`internal/agent/skills_learner.go` reads its instruction from the\n" \
        "`fastagent-skill-learner` skill (`loadSkillLearnerPrompt`), so that file is the prompt —\n" \
        "review `skills/fastagent-skill-learner/SKILL.md`.\n"
    with open(os.path.join(OUT, "f-skills.md"), "w", encoding="utf-8") as fh:
        fh.write(body)
    print("%-32s %5d chars" % ("f-skills.md", len(body)))


if __name__ == "__main__":
    main()
