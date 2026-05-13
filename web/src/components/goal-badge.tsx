"use client";

import { Pause, Play, Target, Trash2 } from "lucide-react";
import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { Goal, GoalStatus } from "@/lib/api";

// GoalBadge is the persistent goal indicator pinned at the top of the
// chat panel. Shows the goal's status + budget at a glance and exposes
// pause / resume / clear controls so users don't have to type slash
// commands for routine state changes. Hidden when no goal exists for
// the current session.

interface GoalBadgeProps {
  goal: Goal | null;
  iterationRound: number;
  onPause: () => Promise<void> | void;
  onResume: () => Promise<void> | void;
  onClear: () => Promise<void> | void;
}

const statusLabel: Record<GoalStatus, string> = {
  active: "Active",
  paused: "Paused",
  budget_limited: "Budget hit",
  complete: "Complete",
};

const statusVariant: Record<GoalStatus, "default" | "secondary" | "destructive" | "outline"> = {
  active: "default",
  paused: "secondary",
  budget_limited: "destructive",
  complete: "outline",
};

export function GoalBadge({ goal, iterationRound, onPause, onResume, onClear }: GoalBadgeProps) {
  const [pending, setPending] = useState<"pause" | "resume" | "clear" | null>(null);

  if (!goal) return null;

  async function run(action: "pause" | "resume" | "clear", fn: () => Promise<void> | void) {
    setPending(action);
    try {
      await fn();
    } catch (e) {
      console.error(`goal ${action} failed`, e);
    } finally {
      setPending(null);
    }
  }

  const budgetText = formatBudget(goal);

  return (
    <div className="border-b bg-muted/40 px-4 py-2">
      <div className="flex items-start gap-3">
        <Target className="mt-1 size-4 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <Badge variant={statusVariant[goal.status]}>{statusLabel[goal.status]}</Badge>
            <span className="text-xs text-muted-foreground">
              {budgetText} · {goal.iterations} model calls · round {Math.max(iterationRound, 1)}
            </span>
          </div>
          <div className="mt-1 text-sm truncate" title={goal.objective}>
            {goal.objective}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {goal.status === "active" && (
            <Button
              size="icon"
              variant="ghost"
              disabled={pending !== null}
              onClick={() => run("pause", onPause)}
              aria-label="Pause continuations"
              title="Pause continuations"
            >
              <Pause className="size-4" />
            </Button>
          )}
          {goal.status === "paused" && (
            <Button
              size="icon"
              variant="ghost"
              disabled={pending !== null}
              onClick={() => run("resume", onResume)}
              aria-label="Resume continuations"
              title="Resume continuations"
            >
              <Play className="size-4" />
            </Button>
          )}
          <Button
            size="icon"
            variant="ghost"
            disabled={pending !== null}
            onClick={() => run("clear", onClear)}
            aria-label="Clear goal"
            title="Clear (delete) goal"
          >
            <Trash2 className="size-4" />
          </Button>
        </div>
      </div>
    </div>
  );
}

// formatBudget renders the budget snapshot the way the goal-view
// status block does in the terminal — "12.3k / 200k tokens" when a
// budget is set, "12.3k tokens" otherwise. K-suffix because the
// numbers get big fast and exact counts aren't useful in a badge.
function formatBudget(goal: Goal): string {
  const used = formatTokens(goal.tokensUsed);
  if (goal.tokenBudget != null) {
    return `${used} / ${formatTokens(goal.tokenBudget)} tokens`;
  }
  return `${used} tokens`;
}

function formatTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}
