"use client";

import { createContext, useCallback, useContext, useEffect, useSyncExternalStore } from "react";

export type Theme = "dark" | "light" | "system";

const STORAGE_KEY = "fastagent-theme";

const ThemeContext = createContext<{
  theme: Theme;
  setTheme: (t: Theme) => void;
  toggleTheme: () => void;
  resolvedTheme: "dark" | "light";
}>({
  theme: "dark",
  setTheme: () => {},
  toggleTheme: () => {},
  resolvedTheme: "dark",
});

export function useTheme() {
  return useContext(ThemeContext);
}

function readSystem(): "dark" | "light" {
  if (typeof window === "undefined") return "dark";
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function apply(resolved: "dark" | "light") {
  document.documentElement.classList.toggle("dark", resolved === "dark");
}

/**
 * The persisted theme is external state — it lives in localStorage, and for
 * `system` it also lives in the OS. Reading it in an effect and writing it back
 * into React state is what react-hooks/set-state-in-effect flags, and a lazy
 * useState initializer has the same value on the client as on the server but a
 * different one from localStorage, i.e. a hydration mismatch.
 *
 * useSyncExternalStore is the API for exactly this: the server snapshot is the
 * default, the client snapshot is the stored value, and React reconciles them
 * after hydration rather than erroring. We also get OS-change freshness and
 * cross-tab sync for free, without a listener per provider.
 */
const listeners = new Set<() => void>();
const notifyThemeChange = () => {
  for (const listener of listeners) listener();
};

function readStoredTheme(): Theme {
  const stored = localStorage.getItem(STORAGE_KEY);
  return stored === "light" || stored === "dark" || stored === "system" ? stored : "dark";
}

function subscribeToTheme(onChange: () => void) {
  const mql = window.matchMedia("(prefers-color-scheme: dark)");
  listeners.add(onChange);
  window.addEventListener("storage", notifyThemeChange);
  mql.addEventListener("change", notifyThemeChange);
  return () => {
    listeners.delete(onChange);
    window.removeEventListener("storage", notifyThemeChange);
    mql.removeEventListener("change", notifyThemeChange);
  };
}

/** "theme:resolved" — one stable primitive, so React can compare snapshots. */
function themeSnapshot(): string {
  const stored = readStoredTheme();
  return `${stored}:${stored === "system" ? readSystem() : stored}`;
}

/** Server (and pre-hydration) answer: the same default the old state used. */
const serverThemeSnapshot = () => "dark:dark";

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const snapshot = useSyncExternalStore(subscribeToTheme, themeSnapshot, serverThemeSnapshot);
  const [theme, resolvedTheme] = snapshot.split(":") as [Theme, "dark" | "light"];

  // The DOM write is a real effect (it touches something outside React) — the
  // rule only objects to setState in an effect body.
  useEffect(() => {
    apply(resolvedTheme);
  }, [resolvedTheme]);

  const setTheme = useCallback((next: Theme) => {
    localStorage.setItem(STORAGE_KEY, next);
    notifyThemeChange();
  }, []);

  // toggleTheme is kept for the existing nav-user dropdown — cycles
  // dark → light → dark; "system" can only be selected from /settings.
  const toggleTheme = useCallback(() => {
    setTheme(resolvedTheme === "dark" ? "light" : "dark");
  }, [resolvedTheme, setTheme]);

  return (
    <ThemeContext.Provider value={{ theme, setTheme, toggleTheme, resolvedTheme }}>
      {children}
    </ThemeContext.Provider>
  );
}
