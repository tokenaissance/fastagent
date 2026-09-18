r"""One-off cleanup for the duplicates decision A left behind.

Background — docs/文件系统形式化证明/10-harness-state-audit.md §4 G17:

  Before 2026-09-18 the sandbox sync wrote its snapshot back under the CONTAINER's
  scope, so every project file the sandbox held was copied into
  `<agent>/projects/<pid>/<chat>/…`. Decision A collapsed the write-back to the
  project root (one tree), and deliberately did NOT migrate the copies that
  already existed. This script finds them.

The safety rule, and the reason this script is an analyzer with an optional
delete plan rather than a "cleaner":

  a copy is deleted ONLY when its content provably survives at the project root —
  same relative path `<pid>/<rel>`, same content identity (ETag), and a plain
  (non-multipart) ETag so it really is a content hash.

Everything else is reported and left alone:

  * `<pid>/<chat>/<rel>` with NO `<pid>/<rel>` at the project root
        → not a duplicate. Deleting it would LOSE content; it may be a perfectly
          legitimate chat-scoped file (uploads land there, see 01 §3.1.1).
  * the same relative path with DIFFERENT content
        → a human decides; both copies may be somebody's work.
  * a copy of ANOTHER chat's file (`<pid>/<chatA>/<chatB>/x`, produced when a
    sandbox holding the whole project tree synced) → same rule applies, reported
    in its own bucket so the shape is visible.

Inputs are TSV listings; the script never holds credentials. Produce them with:

  # 1. Objects (S3 / Spaces) — one call, includes ETag and size:
  aws s3api list-objects-v2 --bucket "$BUCKET" [--prefix "$PREFIX"] \
      --query 'Contents[].[Key,Size,ETag,LastModified]' --output text > /tmp/ws_objects.tsv

  # 2. The project's chat ids — WITHOUT this the script refuses to authorize
  #    deletion, because `<pid>/<seg>/<rel>` cannot tell a chat from a plain
  #    directory by inspection (`projects/p1/app/x.tsx` is a normal layout).
  psql "$FASTAGENT_DATABASE_URL" -At -F $'\t' \
      -c "select project_id, session_key from sessions where project_id <> ''" > /tmp/ws_chats.tsv

  # LocalFS deployments — same four columns, ETag replaced by the md5:
  find "$FASTAGENT_HOME/workspaces" -type f -print0 | xargs -0 md5sum ... > /tmp/ws_objects.tsv

Usage:

  python3 scripts/workspace_project_chat_duplicate_cleanup.py --objects /tmp/ws_objects.tsv
  python3 scripts/workspace_project_chat_duplicate_cleanup.py --objects … --chats /tmp/ws_chats.tsv \
      --emit-deletes --bucket my-bucket
  python3 scripts/workspace_project_chat_duplicate_cleanup.py --selftest

`--emit-deletes` prints ready-to-run `aws s3 rm` lines and writes a manifest
(`--manifest`, default `workspace_dup_cleanup_manifest.json`) recording exactly
what was proposed. Running those commands is a separate, deliberate step.

ORDERING — run this AFTER decision A is deployed.

The duplicates are produced by the OLD sync (it wrote the snapshot back under the
container's scope). If the deployed build still has that behaviour, cleaning now
is a treadmill: the next exec recreates what you just removed. Check the deployed
build first (docs 10's "Deployment status" banner: `HEAD` = 16a7532 has the old
behaviour; the working tree has A).
"""

from __future__ import annotations

import argparse
import collections
import json
import re
import sys

MD5 = re.compile(r"^[0-9a-f]{32}$")


def load_listing(path: str) -> dict[str, tuple[int, str]]:
    """key → (size, etag). Rows that are not four columns are skipped."""
    out: dict[str, tuple[int, str]] = {}
    with open(path) as fh:
        for line in fh:
            parts = line.rstrip("\n").split("\t")
            if len(parts) < 3:
                continue
            key = parts[0].strip().strip('"')
            try:
                size = int(parts[1])
            except ValueError:
                continue
            etag = parts[2].strip().strip('"').lower()
            if key:
                out[key] = (size, etag)
    return out


def load_chats(path: str | None) -> dict[str, set[str]]:
    """project_id → set(session_key). Empty dict means "not supplied"."""
    out: dict[str, set[str]] = {}
    if not path:
        return out
    with open(path) as fh:
        for line in fh:
            parts = line.rstrip("\n").split("\t")
            if len(parts) < 2:
                continue
            pid, key = parts[0].strip(), parts[1].strip()
            if pid and key:
                out.setdefault(pid, set()).add(key)
    return out


def split_key(key: str) -> tuple[str, str] | None:
    """<agent>/<tail> — the layout every backend writes (01 §3.5)."""
    agent, _, tail = key.partition("/")
    if not agent or not tail:
        return None
    return agent, tail


def analyze(objects: dict[str, tuple[int, str]], chats: dict[str, set[str]]) -> dict[str, list[dict]]:
    """Classify every `<pid>/<chat>/…` object. Pure; no I/O.

    `chats` is required to AUTHORISE a deletion. Path shape alone cannot tell a
    chat directory from a normal one (`projects/p1/app/x.tsx` is the ordinary
    layout for a project whose app lives in `app/`), so without the list this
    reports candidates and deletes nothing.
    """
    findings: dict[str, list[dict]] = {
        "delete_ok": [],
        "kept_no_root": [],
        "kept_differs": [],
        "kept_unverifiable": [],
        "kept_no_chatlist": [],
    }
    for key, (size, etag) in sorted(objects.items()):
        parts = split_key(key)
        if not parts:
            continue
        agent, tail = parts
        # only project scopes: loose chats never had the problem (their
        # container scope IS their store scope).
        if not tail.startswith("projects/"):
            continue
        segs = tail.split("/")
        if len(segs) < 4:  # projects/<pid>/<chat>/<rel…>
            continue
        pid, chat, rel = segs[1], segs[2], "/".join(segs[3:])
        if not rel or rel.startswith("skills/"):
            continue
        if not chats:
            findings["kept_no_chatlist"].append({
                "key": key, "agent": agent, "project": pid, "chat": chat,
                "rel": rel, "size": size,
                "why": "no --chats list supplied; path shape alone cannot say whether this segment is a chat",
            })
            continue
        if chat not in chats.get(pid, set()):
            continue  # an ordinary directory inside the project, not a chat
        root_key = f"{agent}/projects/{pid}/{rel}"
        if root_key == key:
            continue
        entry = {
            "key": key,
            "agent": agent,
            "project": pid,
            "chat": chat,
            "rel": rel,
            "size": size,
            "etag": etag,
            "root_key": root_key,
        }
        root = objects.get(root_key)
        if root is None:
            findings["kept_no_root"].append(entry)
            continue
        root_size, root_etag = root
        entry["root_size"] = root_size
        entry["root_etag"] = root_etag
        if not MD5.match(etag) or not MD5.match(root_etag):
            # A multipart ETag is not a content hash. Refuse to guess.
            findings["kept_unverifiable"].append(entry)
            continue
        if etag != root_etag or size != root_size:
            findings["kept_differs"].append(entry)
            continue
        findings["delete_ok"].append(entry)
    return findings


def report(findings: dict[str, list[dict]]) -> None:
    ok = findings["delete_ok"]
    print("=" * 78)
    print("duplicates decision A left behind (project chats)")
    print("=" * 78)
    print(f"  deletable (same bytes survive at the project root): {len(ok):5d} objects, "
          f"{sum(e['size'] for e in ok):,} bytes")
    print(f"  kept — no project-root counterpart (not duplicates): {len(findings['kept_no_root']):5d} objects, "
          f"{sum(e['size'] for e in findings['kept_no_root']):,} bytes")
    print(f"  kept — same path, different content (human decides): {len(findings['kept_differs']):5d} objects")
    print(f"  kept — content identity unverifiable (multipart ETag): {len(findings['kept_unverifiable']):5d} objects")
    if findings.get("kept_no_chatlist"):
        print(f"  kept — no chat list supplied, so nothing is authorised: {len(findings['kept_no_chatlist']):5d} candidate objects")
    print()
    if ok:
        per_project: dict[tuple[str, str, str], int] = collections.Counter()
        for e in ok:
            per_project[(e["agent"], e["project"], e["chat"])] += 1
        print("deletable duplicates by (agent, project, chat):")
        for (agent, pid, chat), n in sorted(per_project.items(), key=lambda kv: -kv[1])[:20]:
            print(f"  {n:5d}  {agent}/{pid}/{chat}")
        print()
        print("largest deletable duplicates:")
        for e in sorted(ok, key=lambda e: -e["size"])[:10]:
            print(f"  {e['size']:>10,}  {e['key']}")
        print()
    for bucket, headline in (
        ("kept_no_root", "kept: no project-root counterpart (deleting these would LOSE content)"),
        ("kept_differs", "kept: same path, different content"),
        ("kept_unverifiable", "kept: can't verify content identity"),
        ("kept_no_chatlist", "kept: run again with --chats (a project's chat ids) to classify these"),
    ):
        rows = findings[bucket]
        if not rows:
            continue
        print(f"{headline}:")
        for e in rows[:10]:
            extra = ""
            if "root_size" in e:
                extra = f" (root {e['root_size']:,} vs copy {e['size']:,})"
            print(f"  {e['key']}{extra}")
        if len(rows) > 10:
            print(f"  … and {len(rows) - 10} more")
        print()


def emit_deletes(findings: dict[str, list[dict]], bucket: str) -> list[str]:
    lines = [
        f'aws s3 rm "s3://{bucket}/{e["key"]}"'
        for e in sorted(findings["delete_ok"], key=lambda e: e["key"])
    ]
    for line in lines:
        print(line)
    return lines


def selftest() -> int:
    """The three shapes, as fixture data — runnable without any deployment."""
    objects = {
        # a plain duplicate: same rel path at the root, same bytes → delete
        "ag1/projects/p1/app/x.tsx": (10, "a" * 32),
        "ag1/projects/p1/chat1/app/x.tsx": (10, "a" * 32),
        # a copy of another chat's file: the root counterpart is that chat's own
        "ag1/projects/p1/chat1/chat2/notes.md": (7, "b" * 32),
        "ag1/projects/p1/chat2/notes.md": (7, "b" * 32),
        # no root counterpart → keep (this is somebody's file)
        "ag1/projects/p1/chat1/only-here.md": (5, "c" * 32),
        # same path, different bytes → keep, report
        "ag1/projects/p1/chat1/app/y.tsx": (10, "d" * 32),
        "ag1/projects/p1/app/y.tsx": (10, "e" * 32),
        # multipart ETag → unverifiable → keep
        "ag1/projects/p1/chat1/app/z.tsx": (10, "f" * 32),
        "ag1/projects/p1/app/z.tsx": (10, "f" * 31 + "-2"),
        # a loose chat is out of scope entirely
        "ag1/sessions/s1/chat-only.md": (3, "1" * 32),
        # an ordinary directory that merely LOOKS like a chat scope: `app/` is
        # not in the chat list, so its files are never candidates
        "ag1/projects/p1/app/keep-me.tsx": (4, "9" * 32),
    }
    chats = {"p1": {"chat1", "chat2"}}
    got = analyze(objects, chats)
    errors = []
    if [e["key"] for e in got["delete_ok"]] != [
        "ag1/projects/p1/chat1/app/x.tsx",
        "ag1/projects/p1/chat1/chat2/notes.md",
    ]:
        errors.append(f"delete_ok = {[e['key'] for e in got['delete_ok']]}")
    # Both of these are somebody's real file: `only-here.md` has no root
    # counterpart at all, and `chat2/notes.md` is the sibling chat's OWN copy
    # (whose root counterpart is `projects/p1/notes.md`, which does not exist).
    # The duplicate is `chat1/chat2/notes.md`, above — not this one.
    if [e["key"] for e in got["kept_no_root"]] != [
        "ag1/projects/p1/chat1/only-here.md",
        "ag1/projects/p1/chat2/notes.md",
    ]:
        errors.append(f"kept_no_root = {[e['key'] for e in got['kept_no_root']]}")
    if [e["key"] for e in got["kept_differs"]] != ["ag1/projects/p1/chat1/app/y.tsx"]:
        errors.append(f"kept_differs = {[e['key'] for e in got['kept_differs']]}")
    if [e["key"] for e in got["kept_unverifiable"]] != ["ag1/projects/p1/chat1/app/z.tsx"]:
        errors.append(f"kept_unverifiable = {[e['key'] for e in got['kept_unverifiable']]}")
    # `ag1/projects/p1/app/keep-me.tsx` must not appear anywhere: `app` is a
    # directory, not one of the project's chats.
    if any("keep-me" in e["key"] for rows in got.values() for e in rows):
        errors.append("an ordinary directory was treated as a chat scope")
    # Without a chat list, nothing is authorised — the same objects land in
    # kept_no_chatlist instead of delete_ok.
    blind = analyze(objects, {})
    if blind["delete_ok"] or not blind["kept_no_chatlist"]:
        errors.append("without --chats the analyzer still authorised deletions")
    if errors:
        for e in errors:
            print("SELFTEST FAIL:", e)
        return 1
    print("selftest ok: the three duplicate shapes are classified as intended "
          "(plain duplicate → deletable; copy of a sibling chat → deletable; "
          "no root / different bytes / unverifiable ETag / loose chat → kept)")
    return 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--objects", help="object listing TSV (see the docstring for the command)")
    ap.add_argument("--chats", help="project_id→session_key TSV; REQUIRED to authorise deletions (see the docstring)")
    ap.add_argument("--emit-deletes", action="store_true", help="print ready-to-run aws s3 rm lines for the deletable set")
    ap.add_argument("--bucket", default="", help="bucket name, needed for --emit-deletes")
    ap.add_argument("--manifest", default="workspace_dup_cleanup_manifest.json",
                    help="where to record the deletable set")
    ap.add_argument("--json", action="store_true", help="print the findings as JSON instead of a report")
    ap.add_argument("--selftest", action="store_true", help="run the built-in fixture and exit")
    args = ap.parse_args(argv)

    if args.selftest:
        return selftest()
    if not args.objects:
        ap.error("--objects is required (or use --selftest)")
    if args.emit_deletes and not args.bucket:
        ap.error("--emit-deletes needs --bucket so the printed commands are complete")

    if args.emit_deletes and not args.chats:
        ap.error("--emit-deletes needs --chats: path shape alone cannot tell a chat directory from an ordinary one")
    findings = analyze(load_listing(args.objects), load_chats(args.chats))
    if args.json:
        print(json.dumps(findings, indent=2, sort_keys=True))
    else:
        report(findings)

    if findings["delete_ok"]:
        with open(args.manifest, "w") as fh:
            json.dump({"deletable": findings["delete_ok"],
                       "kept_no_root": findings["kept_no_root"],
                       "kept_differs": findings["kept_differs"],
                       "kept_unverifiable": findings["kept_unverifiable"]}, fh, indent=2, sort_keys=True)
        print(f"manifest written: {args.manifest}")

    if args.emit_deletes:
        print()
        print("# review the manifest first; these lines delete ONLY copies whose bytes survive at the project root")
        emit_deletes(findings, args.bucket)
    return 0


if __name__ == "__main__":
    sys.exit(main())
