"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";

// Static-export only generates /agents/default/..., so useParams() always
// returns "default" when an app is served for a non-default agent via the
// Go spaHandler fallback. Read the real id from window.location on the
// client, fall back to useParams() for the initial SSR/prerender pass.
export function useAgentIdFromURL(): string {
  const params = useParams<{ id: string }>();
  const fallback = params?.id ?? "default";
  const [id, setId] = useState<string>(fallback);
  useEffect(() => {
    const m = window.location.pathname.match(/\/agents\/([^/]+)\//);
    if (m && m[1] !== id) setId(m[1]);
  }, [id]);
  return id;
}
