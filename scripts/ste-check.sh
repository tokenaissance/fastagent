#!/bin/bash
# Check the structural rules of ASD-STE100 (asd-ste100 skill) over this repo's
# agent-read documents, and compare the result with docs/ste-baseline.txt.
#
#   scripts/ste-check.sh            # fail when any file is worse than its baseline
#   scripts/ste-check.sh --update   # rewrite the baseline from the current tree
#   scripts/ste-check.sh --list     # print the current hard counts and exit 0
#   scripts/ste-check.sh --staged   # check only the files staged for this commit
#
# The baseline can only go down. A file that gains a hard violation fails the
# check, so new text cannot add one and every edit has to hold the line. The
# hard rules are the mechanical ones: semicolons (English and Chinese), long
# sentences, nominalization, marketing adjectives, and synonym rotation.
# Advisory findings (passive voice, compound tenses) never fail the run: they
# are a worklist, not a gate.
#
# `--staged` is the shape the pre-commit hook calls: a commit that touches no
# document costs nothing, and a document that is not part of this commit is not
# this commit's business. In that mode a missing linter is a warning, not a
# block, so a fresh clone without the workspace skill still commits.
#
# STE_LINT overrides the linter path. The default is the workspace-level
# asd-ste100 skill, which is a sibling of this repo.
set -euo pipefail

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
LINT=${STE_LINT:-"$REPO/../.agents/skills/asd-ste100/scripts/ste-lint.py"}
FILES="$REPO/docs/ste-files.txt"
BASELINE="$REPO/docs/ste-baseline.txt"

# Read the hard-violation count for one file. The linter exits 1 when a file
# exceeds its own baseline of 0, so read the count out of its report and let the
# pipeline finish (a closed pipe here would trip pipefail and end the script).
count_file() {
  local file=$1 hard
  hard=$({ python3 "$LINT" "$file" 2>/dev/null || true; } \
    | sed -nE 's/^[0-9]+ violations \(([0-9]+) hard.*/\1/p' \
    | tr -d '\n')
  [ -n "$hard" ] || hard=0
  printf '%s\t%s\n' "$hard" "${file#"$REPO"/}"
}

# Every file that matches a pattern in $FILES, one "<hard>\t<rel>" per line.
current() {
  while read -r pattern; do
    [ -n "$pattern" ] || continue
    case "$pattern" in \#*) continue ;; esac
    # shellcheck disable=SC2086
    for file in $REPO/$pattern; do
      [ -f "$file" ] || continue
      count_file "$file"
    done
  done < "$FILES" | sort -k2
}

# The staged subset of $FILES, one "<hard>\t<rel>" per line.
staged() {
  git -C "$REPO" diff --cached --name-only --diff-filter=ACMR | while IFS= read -r rel; do
    [ -n "$rel" ] || continue
    file="$REPO/$rel"
    [ -f "$file" ] || continue
    while read -r pattern; do
      [ -n "$pattern" ] || continue
      case "$pattern" in \#*) continue ;; esac
      # Expand the pattern the same way `current` does. A `case` pattern is not
      # the same test: its `*` crosses a slash, so it would pull in a directory
      # the scope file left out.
      # shellcheck disable=SC2086
      for match in $REPO/$pattern; do
        if [ "$match" = "$file" ]; then count_file "$file"; break 2; fi
      done
    done < "$FILES"
  done | sort -k2
}

# Compare a "<hard>\t<rel>" stream against the baseline. Prints one line per
# regression or new file, and returns non-zero when it printed any.
compare() {
  local rows=$1 status=0 hard rel base
  while IFS=$'\t' read -r hard rel; do
    [ -n "$rel" ] || continue
    base=$(awk -F'\t' -v f="$rel" '$2 == f {print $1}' "$BASELINE")
    if [ -z "$base" ]; then
      echo "NEW FILE (no baseline): $rel has $hard hard finding(s)"
      status=1
      continue
    fi
    if [ "$hard" -gt "$base" ]; then
      echo "REGRESSION: $rel has $hard hard finding(s); the baseline is $base"
      status=1
    fi
  done < "$rows"
  return "$status"
}

mode="${1:-check}"
case "$mode" in
  --list|--update)
    if [ "$mode" = "--update" ]; then
      current > "$BASELINE"
      echo "baseline updated: $BASELINE"
    else
      current
    fi
    exit 0
    ;;
  --staged)
    if [ ! -f "$LINT" ]; then
      echo "ste-check: linter not found ($LINT) — skipping (set STE_LINT to enable)"
      exit 0
    fi
    [ -f "$FILES" ] || { echo "file list not found: $FILES" >&2; exit 2; }
    [ -f "$BASELINE" ] || { echo "baseline not found: $BASELINE" >&2; exit 2; }
    tmp=$(mktemp -t ste-staged)
    trap 'rm -f "$tmp"' EXIT
    staged > "$tmp"
    [ -s "$tmp" ] || { echo "ste-check: no staged document is under the gate"; exit 0; }
    if ! compare "$tmp"; then
      echo "ste-check: a staged document is worse than its baseline" >&2
      exit 1
    fi
    echo "ste-check: no staged document is worse than its baseline"
    exit 0
    ;;
esac

[ -f "$LINT" ] || { echo "linter not found: $LINT (set STE_LINT)" >&2; exit 2; }
[ -f "$FILES" ] || { echo "file list not found: $FILES" >&2; exit 2; }
[ -f "$BASELINE" ] || { echo "baseline not found: $BASELINE" >&2; exit 2; }

tmp=$(mktemp -t ste-current)
trap 'rm -f "$tmp"' EXIT
current > "$tmp"

if ! compare "$tmp"; then
  echo "ste-check: a file is worse than its baseline" >&2
  exit 1
fi
echo "ste-check: no file is worse than its baseline"
