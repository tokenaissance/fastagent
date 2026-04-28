"use client";

import { useState, useEffect } from "react";
import { useRouter, usePathname } from "next/navigation";
import { isLoggedIn, logout } from "@/lib/auth";
import { apiFetch } from "@/lib/api";
import { LoginScreen } from "./login-screen";

interface AuthGuardProps {
  children: React.ReactNode;
}

export function AuthGuard({ children }: AuthGuardProps) {
  const router = useRouter();
  const pathname = usePathname();
  const [checked, setChecked] = useState(false);
  const [authed, setAuthed] = useState(false);

  useEffect(() => {
    let aborted = false;

    (async () => {
      // Step 1: ask whether the system is configured. /api/status is the
      // unauthenticated public probe — when configured=false there is no
      // admin token to validate yet, so showing LoginScreen here would be
      // a dead end (the user has nothing to type). Send them to /onboard
      // instead, which is what RootPage would do if we let it render.
      let configured = false;
      try {
        const res = await fetch("/api/status");
        if (res.ok) {
          const status = await res.json();
          configured = !!status.configured;
        }
      } catch {
        // network/server down — fall through to the LoginScreen path so
        // the existing "Cannot reach server" UX applies.
      }
      if (aborted) return;

      if (!configured) {
        // Drop any stale token from a prior install so we don't carry it
        // into the new onboarding session.
        logout();
        const onOnboard = pathname === "/onboard" || pathname.startsWith("/onboard/");
        if (!onOnboard) {
          router.replace("/onboard/");
          return;
        }
        // Already on the onboard route — render children so the wizard
        // can run. checked stays false → spinner during the redirect.
        setAuthed(true);
        setChecked(true);
        return;
      }

      // Step 2: configured — validate the token by hitting an auth-required
      // endpoint. Even when a token is present in localStorage it may be
      // stale (left over from a prior deployment or rotated admin token)
      // and every subsequent API call would 401.
      try {
        const probe = isLoggedIn()
          ? await apiFetch("/api/config")
          : await fetch("/api/config");
        if (probe.ok) {
          setAuthed(true);
        } else if (probe.status === 401) {
          logout();
        }
      } catch {
        // network failure — fall through to LoginScreen
      }
      if (!aborted) setChecked(true);
    })();

    return () => {
      aborted = true;
    };
  }, [router, pathname]);

  if (!checked) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-zinc-950">
        <div className="h-8 w-8 animate-spin rounded-full border-2 border-zinc-700 border-t-violet-500" />
      </div>
    );
  }

  if (!authed) {
    return <LoginScreen onSuccess={() => setAuthed(true)} />;
  }

  return <>{children}</>;
}
