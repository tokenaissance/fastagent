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

import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "docs", "prompt-inventory")

# Rendered content by filename; main() writes it or — with --check — verifies that
# what is on disk still matches the source.
DOCS = {}


def read(rel):
    with open(os.path.join(ROOT, rel), "r", encoding="utf-8") as fh:
        return fh.read().split("\n")


def join_lines(lines):
    return "\n".join(lines)


def find_block(text, lines, symbol):
    """Return (startIdx, endIdxExclusive) of a top-level func/const/var.

    Braces inside string literals and comments must not count: a raw-string
    description containing `{...}` (every tool schema does) otherwise swallows the
    next declaration, and the snapshot quietly stops being verbatim.
    """
    # Methods carry a receiver: `func (j *sandboxJob) startedMessage() string`.
    pattern = re.compile(
        r"^(func\s+(\([^)]*\)\s*)?|const\s+|var\s+)" + re.escape(symbol) + r"\b"
    )
    for idx, line in enumerate(lines):
        if not pattern.match(line):
            continue
        kind = line.split(" ", 1)[0]  # func | const | var
        depth = 0
        started = False
        state = "code"  # code | dquote | raw
        saw_literal = False
        for j in range(idx, len(lines)):
            i = 0
            body = lines[j]
            while i < len(body):
                c = body[i]
                if state == "code":
                    if c == "/" and i + 1 < len(body) and body[i + 1] == "/":
                        break
                    if c == '"':
                        state, saw_literal = "dquote", True
                    elif c == "`":
                        state, saw_literal = "raw", True
                    elif c == "{":
                        depth += 1
                        started = True
                    elif c in "([":
                        # Signature parens must not open the block: `func f(x) string {`
                        # would otherwise "end" at `)`.
                        depth += 1
                    elif c in ")]}":
                        depth -= 1
                        if started and depth <= 0 and c == "}":
                            return idx, j + 1
                    elif c == "'":
                        k = body.find("'", i + 1)
                        if k > 0:
                            i = k
                elif state == "dquote":
                    if c == "\\":
                        i += 1
                    elif c == '"':
                        state = "code"
                elif state == "raw":
                    if c == "`":
                        state = "code"
                i += 1
            # A const/var holding only string literals: the declaration ends at
            # the end of the expression. "This line is quiet" is not enough: the
            # prompt constants are written as `... ` + "`todo.md`" + ` ...`, so the
            # continuation often starts on the NEXT line with `+`.
            if not started and saw_literal and state == "code":
                trailing = body.rstrip()
                nxt = next((l for l in lines[j + 1 :] if l.strip()), None)
                continues = trailing.endswith(("+", "(", ",", "[", "{")) or (
                    nxt is not None and nxt.lstrip().startswith(("+", ")", ",", "]", "}"))
                )
                if not continues:
                    return idx, j + 1
            # Guard only a multi-line FUNCTION signature. A const/var may legitimately
            # run for a hundred lines (taskDelegationContent is 104); breaking on a
            # line budget is how that block got truncated to 17 lines once already.
            if kind == "func" and not started and j > idx + 40:
                break
        print("warning: could not find the end of %s (%s:%d) — falling back" % (symbol, "?", idx + 1), file=sys.stderr)
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
    """How many places a block chooses between strings. The joined text is a
    rough rendering then (branches concatenated in source order), so the number is
    printed: a one-arm guard reads almost like the runtime text, a three-way intro
    does not — and a blanket "branchy" flag on every block with an `if` would cry
    wolf until nobody reads the note."""
    count = 0
    for line in body.split("\n"):
        stripped = line.strip()
        if stripped.startswith("if ") or stripped.startswith("switch ") or stripped.startswith("} else"):
            count += 1
    return count


def range_text(rel, start, end):
    lines = read(rel)
    body = join_lines(lines[start - 1 : end])
    return "".join(literals(body)), branchy(body)


# Blocks whose pinned range holds no string literal render as a placeholder
# sentence. That is right for the module entries (their text is assembled from
# constants listed elsewhere in this inventory) and wrong for a text that is
# supposed to be captured here — a drifted line number then erases a prompt
# silently, which is how the tool-result error suffix left the inventory twice
# (registry.go 864 → 918 → 931). Track them so the run says which ones they are.
PLACEHOLDERS = []


def normalize_ws(text):
    """Collapse every run of whitespace to one space.

    `gofmt` aligns the `=` of a const block the moment a longer name joins it, so
    `StoppedToolResult = "…"` becomes `StoppedToolResult      = "…"` while the
    prompt text is unchanged. A pin that breaks on that reports a content change
    where none happened — which is what happened on 2026-09-23: the CI job went
    red on `pin 'StoppedToolResult =' matches 0 lines` and the prompt was fine.
    """
    return " ".join(text.split())


def pinned_text(rel, needle):
    """Find the one line holding `needle` and return its number.

    Content, not position: these pins name a single line of prompt text, and a
    line number breaks on every insertion above it. Ambiguity is an error too —
    a needle matching two lines would pin whichever came first.

    Whitespace is normalized on BOTH sides before comparing (see
    `normalize_ws`), so reformatting cannot read as a prompt change, while the
    needle still has to be specific enough to hit one line.
    """
    want = normalize_ws(needle)
    hits = [i + 1 for i, line in enumerate(read(rel)) if want in normalize_ws(line)]
    if len(hits) != 1:
        raise SystemExit(
            "%s: pin %r matches %d lines (want exactly one) — the text moved or was "
            "duplicated" % (rel, needle, len(hits)))
    return hits[0]


def pinned(rel, needle, label, note_override=None):
    """A one-line prompt entry that finds its line by content, not by number."""
    line = pinned_text(rel, needle)
    text, note = range_text(rel, line, line)
    return (label, "%s:%d" % (rel, line), text, note if note_override is None else note_override)


def section(title, blocks, out):
    chunks = ["# %s\n" % title]
    for label, loc, text, note in blocks:
        if not text.strip():
            text = "(no literal text in this block — it delegates to a constant listed above/below)"
            PLACEHOLDERS.append("%s (%s)" % (label, loc))
        chunk = "## %s\n\n<!-- source: %s -->\n" % (label, loc)
        if note:
            chunk += "\n<!-- NOTE: %d branch point(s) — literals concatenated in source order, not rendered -->\n" % note
        # Four backticks: prompt text legitimately contains ``` examples,
        # and a three-backtick wrapper would close on them.
        chunk += "\n````text\n%s\n````\n" % text.strip("\n")
        chunks.append(chunk)
    DOCS[out] = "\n".join(chunks)
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
                "iterationContinueNudge", "loopDetectedWarning", "failedRoundsNudge",
            ],
        )
        # Single literals inside a function body move with every nearby edit, so
        # these follow the text. (The multi-line block pins further down are
        # still line ranges: a shift that lands on different text is the one
        # drift this file cannot yet see, and a shift that lands on no literal
        # fails the run — see PLACEHOLDERS.)
        + [pinned("internal/agent/loop.go", "Deferred — this turn's parallel-tool cap",
                  "deferred tool result")]
        + symbols("internal/agent/subagent.go", ["subagentSystemSuffix"]),
        "b-per-turn-blocks.md",
    )

    section("C. Tool descriptions (the schema the model reads)", registers("internal/agent/tools/*.go"), "c-tool-descriptions.md")

    def sym(rel, symbol, label, note_override=None):
        line, text, note = block_text(rel, symbol)
        return (label, "%s:%d" % (rel, line), text, note if note_override is None else note_override)

    section(
        "D. Text injected into tool results",
        [
            # Both of these name a single line of text, and both have already
            # been lost once to a line number that drifted — so they follow the
            # text instead.
            pinned("internal/agent/tools/registry.go", "Analyze the error above and try a different approach.",
                   "error suffix on every failed tool"),
            sym("internal/agent/tools/exec.go", "longWaitRefusal", "long foreground wait refusal"),
            sym("internal/sandbox/e2b_executor.go", "execCancelledHintText", "exec cancelled hint", False),
            sym("internal/sandbox/e2b_executor.go", "execStalledHint", "exec stalled hint"),
            pinned("internal/agent/tools/exec.go", "this looks like a sandbox-environment miss",
                   "sandbox-absence hint"),
            sym("internal/agent/tools/sandbox_background.go", "startedMessage", "background job started"),
            ("background poll status lines", "internal/agent/tools/sandbox_background.go:330-360", *range_text("internal/agent/tools/sandbox_background.go", 330, 360)),
            pinned("internal/provider/provider.go", "StoppedToolResult =",
                   "interrupted-call placeholder"),
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
    DOCS["f-skills.md"] = body
    print("%-32s %5d chars" % ("f-skills.md", len(body)))

    if PLACEHOLDERS:
        print("rendered as a placeholder instead of text (expected for the module entries):")
        for p in PLACEHOLDERS:
            print("  - %s" % p)

    if "--check" in sys.argv:
        check()
        return
    os.makedirs(OUT, exist_ok=True)
    for name, text in DOCS.items():
        with open(os.path.join(OUT, name), "w", encoding="utf-8") as fh:
            fh.write(text)


def strip_provenance(text):
    """Provenance comments carry file:line, and line numbers move whenever code
    above a prompt changes. The CI gate compares TEXT, so a comment edit does not
    fail a build while a prompt edit does."""
    return "\n".join(l for l in text.split("\n") if not l.startswith("<!-- source:"))


def check():
    stale = []
    for name, text in DOCS.items():
        path = os.path.join(OUT, name)
        current = open(path, encoding="utf-8").read() if os.path.exists(path) else ""
        if strip_provenance(current) != strip_provenance(text):
            stale.append(name)
    if stale:
        print("prompt snapshot is stale: " + ", ".join(stale))
        print("run: python3 scripts/extract-prompt-inventory.py")
        sys.exit(1)
    print("prompt inventory matches the source (%d files)" % len(DOCS))


if __name__ == "__main__":
    main()
