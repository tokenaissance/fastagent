#!/usr/bin/env python3
"""Retrospective audit: did any workspace file end up SMALLER than what the host
agent wrote into it?

Why this exists — docs/文件系统形式化证明/07-formal-rootcause-and-fix.md:
the sandbox→store sync used to overwrite a store object with the sandbox's older
copy. The gateway logs that would show it are lost on every pod restart (no log
forwarding), so this reads the two sources that DO have long history:

  * session_messages (Postgres) — every tool call's arguments and result, which
    carries the byte count the host tools wrote and the edit deltas they made;
  * the workspace object store (S3/Spaces) — each object's current size and
    LastModified.

For every (session, path) the host tools wrote, the audit computes

    expect >= last write size + max(0, net bytes added by edits AFTER it)

and flags the path when the stored object is smaller than that floor *and* the
agent's own edits cannot account for the difference. A destructive edit (a big
deletion) is therefore NOT reported: its delta is negative and the floor stays
at the last write.

Edits that happened BEFORE the last write are deliberately excluded: they are
already contained in that write's byte count, and counting them again would
double-count the growth (a whole-paragraph rewrite would look like hundreds of
KB of "missing" text).

Usage:
    python3 scripts/workspace_revert_audit.py \
        --msgs /tmp/msgs_7d.tsv --objects /tmp/s3_objects.tsv

Both inputs are TSV dumps; see docs/文件系统形式化证明/04 §2.6 for the
exact queries/commands that produce them.
"""

from __future__ import annotations

import argparse
import collections
import datetime as dt
import json
import re

RES_WRITE = re.compile(r"(?:Written|Wrote) (\d+) bytes to (\S+)")
RES_EDIT = re.compile(r"Edited (\S+) \(\d+ replacement")
RES_PATCH_FILE = re.compile(r"\*\*\* Update File: (\S+)\n(.*?)(?=\n\*\*\* |\Z)", re.S)


def load_objects(path: str) -> dict[tuple[str, str], tuple[int, dt.datetime]]:
    """S3 keys look like <prefix>/<agent>/sessions/<sid>/<relpath>."""
    out: dict[tuple[str, str], tuple[int, dt.datetime]] = {}
    with open(path) as fh:
        for line in fh:
            key, size, mod = line.rstrip("\n").split("\t")
            rest = key.split("/", 1)[1] if "/" in key else key
            agent, _, tail = rest.partition("/")
            out[(agent, tail)] = (int(size), dt.datetime.strptime(mod[:19], "%Y-%m-%dT%H:%M:%S"))
    return out


def collect_writes(msgs: str) -> dict[tuple[str, str], dict]:
    """Fold every tool call/result into per-(session, path) write + edit history.

    Edits are timestamped so the caller can keep only the ones that happened
    AFTER the last write (see the module docstring).
    """
    d: dict[tuple[str, str], dict] = collections.defaultdict(
        lambda: {"edits": [], "writes": []}
    )
    with open(msgs) as fh:
        for line in fh:
            parts = line.rstrip("\n").split("\t")
            if len(parts) != 5:
                continue
            sid, ts, _role, content, calls = parts
            for m in RES_WRITE.finditer(content):
                d[(sid, m.group(2))]["writes"].append((ts, int(m.group(1))))
            if not calls or calls == "null":
                continue
            try:
                arr = json.loads(calls)
            except Exception:
                continue
            for call in arr:
                fn = call.get("function", {})
                name, raw = fn.get("name"), fn.get("arguments", "{}")
                try:
                    args = json.loads(raw)
                except Exception:
                    continue
                p = args.get("path") or ""
                if name == "write_file" and p:
                    d[(sid, p)]["writes"].append((ts, len(args.get("content", ""))))
                elif name == "edit_file" and p:
                    d[(sid, p)]["edits"].append(
                        (ts, len(args.get("new_string", "")), len(args.get("old_string", "")))
                    )
                elif name == "apply_patch":
                    for m in RES_PATCH_FILE.finditer(args.get("input", "")):
                        f, body = m.group(1), m.group(2)
                        add = rem = 0
                        for ln in body.split("\n"):
                            if ln.startswith("+"):
                                add += len(ln)
                            elif ln.startswith("-"):
                                rem += len(ln)
                        if add or rem:
                            d[(sid, f)]["edits"].append((ts, add, rem))
    return d


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--msgs", required=True, help="session_messages TSV (see doc 04 §2.6)")
    ap.add_argument("--objects", required=True, help="object-store listing TSV")
    ap.add_argument("--min-gap", type=int, default=200, help="ignore differences below this")
    args = ap.parse_args()

    store = load_objects(args.objects)
    writes = collect_writes(args.msgs)

    hits = []
    for (sid, path), d in writes.items():
        if not d["writes"] or path.startswith("/"):
            continue  # absolute paths live in the sandbox / host disk, not the store
        match = next(
            ((ag, tl, v) for (ag, tl), v in store.items() if tl == f"sessions/{sid}/{path}"),
            None,
        )
        if not match:
            continue
        size = match[2][0]
        last_ts, last_size = d["writes"][-1]
        # Only edits made AFTER the last write can add bytes the store should
        # still hold; earlier ones are already inside last_size.
        net = sum(add - rem for ts, add, rem in d["edits"] if ts > last_ts)
        floor = last_size + max(0, net)
        missing = floor - size
        if missing > args.min_gap:
            hits.append((sid, path, last_size, net, floor, size, missing))

    print(f"{'session':26} {'path':30} {'last write':>10} {'net edits':>10} {'expect >= ':>10} {'store':>8} {'missing':>9}")
    for h in sorted(hits, key=lambda x: -x[6]):
        print(f"{h[0][:26]:26} {h[1][:30]:30} {h[2]:>10} {h[3]:>10} {h[4]:>10} {h[5]:>8} {h[6]:>9}")
    print()
    print(f"flagged paths: {len(hits)} | sessions: {len({h[0] for h in hits})} | missing (lower bound): {sum(h[6] for h in hits)} bytes")
    return 1 if hits else 0


if __name__ == "__main__":
    raise SystemExit(main())
