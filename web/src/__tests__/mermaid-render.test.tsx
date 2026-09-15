/**
 * package.json forces two versions through pnpm overrides: mermaid to
 * ^11.17.2 and dompurify to ^3.4.15, because @streamdown/mermaid pins mermaid
 * at exactly 11.15.0 (which pins dompurify 3.4.8) and neither has a newer
 * release to move to. The advisories those two bumps close — prototype
 * pollution in mermaid's config API, CSS injection and an XSS in dompurify —
 * are all in the diagram path, and a forced version of a rendering library is
 * precisely what a build cannot check: it compiles, it bundles, and it shows up
 * as a blank box in the chat.
 *
 * So this renders a fenced block through the same component the chat uses, and
 * asserts an SVG came out the other end.
 */
import { describe, it, expect } from "vitest";
import { render, waitFor } from "@testing-library/react";
import { readFileSync } from "node:fs";
import path from "node:path";

import { ChatMarkdown } from "@/components/chat-markdown";

const FLOWCHART = "```mermaid\nflowchart TD\n  A[question] --> B[answer]\n```";
const SEQUENCE = "```mermaid\nsequenceDiagram\n  Alice->>Bob: hello\n  Bob-->>Alice: hi\n```";

describe("mermaid after the version override", () => {
  it.each([
    ["flowchart", FLOWCHART],
    ["sequence diagram", SEQUENCE],
  ])("renders a fenced %s block as an svg", async (_name, md) => {
    const { container } = render(<ChatMarkdown text={md} />);

    await waitFor(
      () => {
        const svg = container.querySelector("[data-streamdown=mermaid-block] svg");
        expect(svg).toBeTruthy();
      },
      { timeout: 10_000 },
    );
  });
});

/**
 * The render test above is necessary but not sufficient: it passes on the
 * vulnerable version too. What would silently undo this work is the override
 * going away, so the floor each package has to clear is asserted against the
 * version actually installed.
 */
/**
 * Reads the version out of pnpm-lock.yaml rather than node_modules: pnpm keeps
 * transitive packages out of the top level, and the lockfile is also the exact
 * file the override rewrites (and the one CI installs from, frozen).
 */
function readLock(): string {
  return readFileSync(path.join(process.cwd(), "pnpm-lock.yaml"), "utf8");
}

function lockedVersion(lock: string, pkg: string): string {
  const name = pkg.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const found = [...lock.matchAll(new RegExp(`^ {2}'?${name}@(\\d+\\.\\d+\\.\\d+[^':]*)'?:`, "gm"))]
    .map((m) => m[1]);
  if (found.length === 0) throw new Error(`pnpm-lock.yaml has no entry for ${pkg}`);
  return found.sort((a, b) => compareSemver(a, b))[found.length - 1];
}

function compareSemver(a: string, b: string): number {
  const pa = a.split(/[.-]/).map((p) => Number(p) || 0);
  const pb = b.split(/[.-]/).map((p) => Number(p) || 0);
  for (let i = 0; i < 3; i++) {
    if ((pa[i] ?? 0) !== (pb[i] ?? 0)) return (pa[i] ?? 0) - (pb[i] ?? 0);
  }
  return 0;
}

function atLeast(actual: string, floor: string): boolean {
  const a = actual.split(".").map(Number);
  const f = floor.split(".").map(Number);
  for (let i = 0; i < 3; i++) {
    if ((a[i] ?? 0) !== (f[i] ?? 0)) return (a[i] ?? 0) > (f[i] ?? 0);
  }
  return true;
}

describe("the overrides hold the versions that close the advisories", () => {
  it("compares versions the way the floors assume", () => {
    expect(atLeast("11.17.2", "11.16.1")).toBe(true);
    expect(atLeast("11.15.0", "11.16.1")).toBe(false); // the version that shipped before
    expect(atLeast("3.4.15", "3.4.13")).toBe(true);
    expect(atLeast("3.4.8", "3.4.13")).toBe(false); // ditto
  });

  it.each([
    ["mermaid", "11.16.1", "GHSA-c4c3-pg64-4m4v"],
    ["dompurify", "3.4.13", "GHSA-55q2-fjhq-7xh7"],
    ["browserslist", "4.28.7", "GHSA-73wf-gq98-2v4g"],
    ["baseline-browser-mapping", "2.11.0", "GHSA-w5vr-8v7q-w6rv"],
    ["@babel/core", "7.29.6", "GHSA-4x5r-pxfx-6jf8"],
  ])("%s clears the floor set by %s (%s)", (pkg, floor, ghsa) => {
    const actual = lockedVersion(readLock(), pkg);
    expect(atLeast(actual, floor), `${pkg}@${actual} is below the patched ${floor} (${ghsa})`).toBe(true);
  });

  // The check above is only worth having if it fails on the version that was
  // there before the override. Rewriting the real lockfile text (rather than
  // uninstalling) keeps the assertion honest without perturbing the tree.
  it("fails on the version this override replaced", () => {
    // The version is rewritten rather than named: the pinned one moves whenever
    // the override is retargeted (11.17.2 became 11.16.1 the day pnpm 11's
    // release-age policy ruled out the newer one), and a test that hard-codes it
    // fails for the wrong reason. Every occurrence, since the lockfile names the
    // version twice and the reader takes the highest.
    const asBefore = readLock().replace(/mermaid@\d+\.\d+\.\d+/g, "mermaid@11.15.0");
    expect(atLeast(lockedVersion(asBefore, "mermaid"), "11.16.1")).toBe(false);
  });
});
