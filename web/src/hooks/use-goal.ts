"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  createAgentGoal,
  deleteAgentGoal,
  getAgentGoal,
  pauseAgentGoal,
  resumeAgentGoal,
  type ChatStreamEvent,
  type CreateGoalRequest,
  type Goal,
} from "@/lib/api";

// useGoal owns the goal state for the active (agentId, sessionKey)
// pair. It does two things:
//
//   1. Fetches the current goal from REST on mount + when the session
//      changes. That seeds the badge so a hard reload shows the right
//      state without waiting for an SSE event.
//
//   2. Exposes consume(evt) so the chat panel can feed in the
//      goal_* events its SSE EventSource is already reading. Cheaper
//      than opening a second SSE just for goals.
//
// Mutations (create/pause/resume/clear) call REST and optimistically
// update local state — the resulting goal_* event from the server
// then re-converges via consume(). If REST fails the optimistic
// update is rolled back.
export interface UseGoal {
  goal: Goal | null;
  loading: boolean;
  // Round counter the iteration event ticks. Resets when the goal
  // changes (clear / new objective). Used by the chat panel to show
  // "Goal continuing… (round N)" between assistant turns.
  iterationRound: number;
  create: (req: Omit<CreateGoalRequest, "sessionKey">) => Promise<void>;
  pause: () => Promise<void>;
  resume: () => Promise<void>;
  clear: () => Promise<void>;
  consume: (evt: ChatStreamEvent) => void;
  refresh: () => Promise<void>;
}

export function useGoal(agentId: string | undefined, sessionKey: string | undefined): UseGoal {
  const [goal, setGoal] = useState<Goal | null>(null);
  const [loading, setLoading] = useState(false);
  const [iterationRound, setIterationRound] = useState(0);

  // sessionKeyRef so consume() doesn't capture a stale closure when
  // the SSE handler hangs onto the function across session switches.
  const sessionKeyRef = useRef(sessionKey);
  useEffect(() => {
    sessionKeyRef.current = sessionKey;
  }, [sessionKey]);

  const refresh = useCallback(async () => {
    if (!agentId || !sessionKey) {
      setGoal(null);
      return;
    }
    setLoading(true);
    try {
      const g = await getAgentGoal(agentId, sessionKey);
      setGoal(g);
      setIterationRound(g?.iterations ?? 0);
    } finally {
      setLoading(false);
    }
  }, [agentId, sessionKey]);

  // Refresh on session swap. Don't refresh on every keystroke / event —
  // the SSE consumer keeps state live between refreshes.
  useEffect(() => {
    void refresh();
  }, [refresh]);

  const create = useCallback(
    async (req: Omit<CreateGoalRequest, "sessionKey">) => {
      if (!agentId || !sessionKey) throw new Error("agent or session not ready");
      const created = await createAgentGoal(agentId, { ...req, sessionKey });
      setGoal(created);
      setIterationRound(created.iterations ?? 0);
    },
    [agentId, sessionKey],
  );

  const pause = useCallback(async () => {
    if (!agentId || !sessionKey) return;
    const prev = goal;
    setGoal(prev ? { ...prev, status: "paused" } : prev);
    try {
      const g = await pauseAgentGoal(agentId, sessionKey);
      setGoal(g);
    } catch (e) {
      setGoal(prev);
      throw e;
    }
  }, [agentId, sessionKey, goal]);

  const resume = useCallback(async () => {
    if (!agentId || !sessionKey) return;
    const prev = goal;
    setGoal(prev ? { ...prev, status: "active" } : prev);
    try {
      const g = await resumeAgentGoal(agentId, sessionKey);
      setGoal(g);
    } catch (e) {
      setGoal(prev);
      throw e;
    }
  }, [agentId, sessionKey, goal]);

  const clear = useCallback(async () => {
    if (!agentId || !sessionKey) return;
    const prev = goal;
    setGoal(null);
    setIterationRound(0);
    try {
      await deleteAgentGoal(agentId, sessionKey);
    } catch (e) {
      setGoal(prev);
      throw e;
    }
  }, [agentId, sessionKey, goal]);

  const consume = useCallback(
    (evt: ChatStreamEvent) => {
      const incoming = evt.data?.goal;
      switch (evt.type) {
        case "goal_created":
          if (incoming) {
            setGoal(incoming);
            setIterationRound(incoming.iterations ?? 0);
          }
          return;
        case "goal_status_changed":
          if (incoming) setGoal(incoming);
          return;
        case "goal_iteration":
          // Iteration ticks: bump the round counter (chat shows
          // "round N") and fold in any updated tokens/iterations from
          // the snapshot. The server already incremented iterations
          // via FoldUsage, so reading the snapshot is authoritative.
          if (incoming) {
            setGoal((current) => (current ? { ...current, ...incoming } : incoming));
            setIterationRound((r) => Math.max(r + 1, incoming.iterations ?? r + 1));
          } else {
            setIterationRound((r) => r + 1);
          }
          return;
        case "goal_cleared":
          setGoal(null);
          setIterationRound(0);
          return;
        default:
          return;
      }
    },
    [],
  );

  return { goal, loading, iterationRound, create, pause, resume, clear, consume, refresh };
}
