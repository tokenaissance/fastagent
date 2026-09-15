"use client";

import { useEffect, useState } from "react";
import { getAgents } from "@/lib/api";

// useAgentName resolves an agent id to its display name. While the agent
// list is loading, or if the id isn't in the list, it returns the id so
// page chrome doesn't flicker between empty and resolved states. Pass an
// empty string to skip the fetch entirely.
export function useAgentName(agentId: string): string {
  // Which id the resolved name belongs to, rather than the name alone: if the
  // caller switches agents, the previous agent's name must not survive even
  // for one frame. Deriving the fallback (below) instead of seeding state in
  // the effect is what removes the two synchronous writes the effect used to
  // make — `setState` in an effect body is what react-hooks/set-state-in-effect
  // flags, and neither write was needed: the id IS the fallback.
  const [resolved, setResolved] = useState<{ id: string; name: string } | null>(null);
  useEffect(() => {
    if (!agentId) return;
    let aborted = false;
    getAgents()
      .then((list) => {
        if (aborted) return;
        const me = list.find((a) => a.id === agentId);
        setResolved({ id: agentId, name: me?.name || agentId });
      })
      .catch(() => {
        // Leave the id as the fallback — the derived value below already is it.
      });
    return () => {
      aborted = true;
    };
  }, [agentId]);
  return resolved?.id === agentId ? resolved.name : agentId;
}
