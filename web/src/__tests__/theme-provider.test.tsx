/**
 * The theme lives outside React (localStorage, plus the OS preference for
 * "system"), which is why the provider reads it through useSyncExternalStore
 * rather than hydrating it into state in an effect. These cases pin the
 * capabilities that came with that move — the stored value wins over the
 * default, another tab's write is picked up, and our own write persists — so a
 * future refactor back to an effect has to keep them.
 */
import { describe, it, expect, beforeEach } from "vitest";
import { render, screen, act, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { ThemeProvider, useTheme } from "@/components/theme-provider";

const STORAGE_KEY = "fastagent-theme";

function Probe() {
  const { theme, resolvedTheme, setTheme } = useTheme();
  return (
    <div>
      <span data-testid="state">{`${theme}|${resolvedTheme}`}</span>
      <button onClick={() => setTheme("light")}>light</button>
    </div>
  );
}

/** Another tab writing, as the browser reports it. */
async function otherTabWrites(value: string) {
  await act(async () => {
    localStorage.setItem(STORAGE_KEY, value);
    window.dispatchEvent(new Event("storage"));
  });
}

describe("ThemeProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    // The snapshot is cached in module scope for cheapness, so a case that
    // starts from a different stored value has to wake the store first.
    window.dispatchEvent(new Event("storage"));
    document.documentElement.classList.remove("dark");
  });

  it("takes the stored theme over the default, and applies it", async () => {
    localStorage.setItem(STORAGE_KEY, "light");
    window.dispatchEvent(new Event("storage"));

    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );

    await waitFor(() => expect(screen.getByTestId("state")).toHaveTextContent("light|light"));
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("follows another tab's write", async () => {
    localStorage.setItem(STORAGE_KEY, "light");
    window.dispatchEvent(new Event("storage"));

    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );
    await waitFor(() => expect(screen.getByTestId("state")).toHaveTextContent("light|light"));

    await otherTabWrites("dark");

    await waitFor(() => expect(screen.getByTestId("state")).toHaveTextContent("dark|dark"));
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("persists what the user picks", async () => {
    localStorage.setItem(STORAGE_KEY, "dark");
    window.dispatchEvent(new Event("storage"));

    render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );
    await waitFor(() => expect(screen.getByTestId("state")).toHaveTextContent("dark|dark"));

    await userEvent.click(screen.getByRole("button", { name: "light" }));

    await waitFor(() => expect(screen.getByTestId("state")).toHaveTextContent("light|light"));
    expect(localStorage.getItem(STORAGE_KEY)).toBe("light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });
});
