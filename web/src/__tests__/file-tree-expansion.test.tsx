/**
 * The file tree's expansion is derived, not seeded once (see
 * docs/webui-lint-cleanup.md §3.1). The visible consequence, and the only
 * behaviour change in that cleanup that a user can see, is the one pinned here:
 * a folder that appears in a LATER tree is open, where the old "initialise on
 * the first tree" ref left it shut.
 */
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { FileTreeView } from "@/components/chat-screen";

const file = (path: string) => ({ path, size: 1, modTime: 1 });

describe("FileTreeView expansion", () => {
  it("opens the folders of whatever tree is on screen", () => {
    render(
      <FileTreeView
        files={[file("session/notes.md")]}
        rootPrefix="session"
        onSelect={vi.fn()}
      />,
    );

    // depth 0 < defaultExpandDepth (1) → the root folder is open.
    expect(screen.getByText("notes.md")).toBeInTheDocument();
  });

  it("opens a folder that appears in a later tree, and still honours a user toggle", () => {
    const { rerender } = render(
      <FileTreeView files={[file("session/notes.md")]} rootPrefix="session" onSelect={vi.fn()} />,
    );
    expect(screen.queryByText("late.md")).not.toBeInTheDocument();

    // A refresh brings a new top-level folder. Derived expansion means it is
    // open immediately — the behaviour the cleanup changed on purpose.
    rerender(
      <FileTreeView
        files={[file("session/notes.md"), file("output/late.md")]}
        rootPrefix=""
        onSelect={vi.fn()}
      />,
    );
    expect(screen.getByText("late.md")).toBeInTheDocument();
  });
});
