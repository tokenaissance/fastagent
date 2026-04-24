"use client";

import { useState, useEffect } from "react";
import { isLoggedIn, logout } from "@/lib/auth";
import { apiFetch } from "@/lib/api";
import { LoginScreen } from "./login-screen";

interface AuthGuardProps {
  children: React.ReactNode;
}

export function AuthGuard({ children }: AuthGuardProps) {
  const [checked, setChecked] = useState(false);
  const [authed, setAuthed] = useState(false);

  useEffect(() => {
    // Always probe an auth-required endpoint — even if a token exists, it
    // may be stale (left over from a prior deployment or rotated admin
    // token) and every subsequent API call would 401. We can't use
    // /api/status here because it's intentionally unauthenticated-friendly
    // and returns 200 without a user, which would look like local mode.
    const probe = isLoggedIn()
      ? apiFetch("/api/config")   // includes bearer if present
      : fetch("/api/config");     // bare — detects local mode (no authToken)

    probe
      .then((res) => {
        if (res.ok) {
          setAuthed(true);
        } else if (res.status === 401) {
          // Token is missing or invalid — drop it so LoginScreen starts clean.
          logout();
        }
        setChecked(true);
      })
      .catch(() => {
        setChecked(true);
      });
  }, []);

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
