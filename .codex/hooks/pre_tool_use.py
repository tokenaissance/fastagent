#!/usr/bin/env python3
"""Codex PreToolUse hook: hand `git commit` to the project's commit check.

Codex fires this before a Bash tool call. When the command creates a commit and
.codex/commit-check.conf names a skill, we inject a short instruction telling the
agent to run that skill against the staged diff *before* committing. The commit
itself is not blocked: this is a nudge placed while the change is still staged,
which is the one moment the agent can still act on what the skill finds.

Advisory by construction. Every failure path (no config, no payload, unexpected
schema, unreadable file) exits 0 with no output, so a broken hook cannot break a
session. The deterministic half of the gate lives in .githooks/pre-commit, the
judgement half in .githooks/pre-push; this hook only carries the instruction.

Dormant until .codex/commit-check.conf sets `skill=`.
Opt out with FASTAGENT_COMMIT_CHECK=0; nested runs set
FASTAGENT_COMMIT_CHECK_ACTIVE=1 and are ignored.
"""

from __future__ import annotations

import json
import os
import re
import sys
from pathlib import Path

CONF_RELATIVE = Path(".codex/commit-check.conf")

# `git commit` in any of its dressings - leading `-C <dir>`, `-c key=value`,
# `--git-dir=...` - while `git commit-tree` stays out of scope.
GIT_COMMIT = re.compile(
    r"(?:^|[;&|(]\s*|\s)git\s+"
    r"(?:-C\s+\S+\s+|-c\s+\S+\s+|--\S+\s+)*"
    r"commit(?![-a-zA-Z])"
)


def read_payload() -> dict:
    try:
        raw = sys.stdin.read()
    except (OSError, ValueError):
        return {}
    if not raw.strip():
        return {}
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError:
        return {}
    return payload if isinstance(payload, dict) else {}


def conf_value(text: str, key: str) -> str:
    """First wins, so an override can sit at the top of the file."""
    for line in text.splitlines():
        line = line.split("#", 1)[0].strip()
        if "=" not in line:
            continue
        name, value = line.split("=", 1)
        if name.strip() == key:
            return value.strip()
    return ""


def load_conf(root: Path) -> tuple[str, str]:
    try:
        text = (root / CONF_RELATIVE).read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return "", ""
    return conf_value(text, "skill"), conf_value(text, "focus")


def command_of(payload: dict) -> str:
    tool_input = payload.get("tool_input")
    if isinstance(tool_input, dict):
        command = tool_input.get("command")
        if isinstance(command, str):
            return command
    return ""


def context(skill: str, focus: str) -> str:
    lines = [
        "[commit-check] This command creates a commit. Run the project's commit "
        "check first - before the commit exists, while the change is still staged.",
        "",
        f"Skill(s) to run, in order: {skill}",
    ]
    if focus:
        lines.append(f"Extra focus from .codex/commit-check.conf: {focus}")
    lines += [
        "",
        "Review the staged snapshot (`git diff --cached`), not the working tree: "
        "unstaged edits are not part of this commit.",
        "Act on what the skill reports - fix it, or say plainly what you are "
        "knowingly committing past - and only then create the commit.",
        "If you already ran this skill against this exact staged diff in this "
        "session and nothing has changed since, skip the repeat and commit.",
    ]
    return "\n".join(lines)


def main() -> None:
    if os.environ.get("FASTAGENT_COMMIT_CHECK_ACTIVE") == "1":
        return
    if os.environ.get("FASTAGENT_COMMIT_CHECK") == "0":
        return

    payload = read_payload()
    if payload.get("hook_event_name") not in (None, "PreToolUse"):
        return
    if payload.get("tool_name") not in (None, "Bash"):
        return

    command = command_of(payload)
    if not command or not GIT_COMMIT.search(command):
        return

    cwd = payload.get("cwd")
    root = Path(cwd) if isinstance(cwd, str) and cwd else Path.cwd()

    conf_skill, focus = load_conf(root)
    skill = os.environ.get("FASTAGENT_COMMIT_CHECK_SKILL") or conf_skill
    if not skill:
        return

    json.dump(
        {
            "systemMessage": "commit check: run the project's commit skills on the staged diff first",
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "additionalContext": context(skill, focus),
            },
        },
        sys.stdout,
        ensure_ascii=True,
    )
    sys.stdout.write("\n")


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:  # pragma: no cover - a hook must never break a session
        print(f"[commit-check] {exc}", file=sys.stderr)
    raise SystemExit(0)
