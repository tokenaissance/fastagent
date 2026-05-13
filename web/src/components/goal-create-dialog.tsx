"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

// GoalCreateDialog is the form variant of `/goal <objective>`. Adds:
//   - a token-budget input (slash doesn't take one — only REST / tool do)
//   - a hint about objective quality (mirrors the slash weak-objective
//     scaffold so users discover the criteria without having to fail
//     once first)
//
// Wires through the parent's onCreate callback rather than calling
// REST directly so the chat panel's useGoal hook can apply the
// optimistic update path consistently with other mutations.

interface GoalCreateDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreate: (objective: string, tokenBudget?: number) => Promise<void>;
}

export function GoalCreateDialog({ open, onOpenChange, onCreate }: GoalCreateDialogProps) {
  const [objective, setObjective] = useState("");
  const [budget, setBudget] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = objective.trim();
    if (!trimmed) {
      setError("Objective is required");
      return;
    }
    let tokenBudget: number | undefined;
    if (budget.trim() !== "") {
      const n = Number(budget);
      if (!Number.isFinite(n) || n <= 0) {
        setError("Token budget must be a positive number");
        return;
      }
      tokenBudget = Math.floor(n);
    }
    setSubmitting(true);
    setError(null);
    try {
      await onCreate(trimmed, tokenBudget);
      setObjective("");
      setBudget("");
      onOpenChange(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to create goal");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>Set a goal for this chat</DialogTitle>
          <DialogDescription>
            The agent will keep planning, executing, and self-auditing until it marks the goal
            complete, the budget runs out, or you pause / clear it.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="goal-objective">Objective</Label>
            <Textarea
              id="goal-objective"
              value={objective}
              onChange={(e) => setObjective(e.target.value)}
              placeholder="Translate README.md into English and save to /tmp/readme.en.md; verify the line count matches the original with wc -l."
              rows={5}
              autoFocus
            />
            <p className="text-xs text-muted-foreground">
              For best results, include: scoped target, expected end state, explicit non-goals, and
              a verification path. Short or verification-less objectives are auto-paused for
              review.
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="goal-budget">Token budget (optional)</Label>
            <Input
              id="goal-budget"
              type="number"
              inputMode="numeric"
              min={1}
              value={budget}
              onChange={(e) => setBudget(e.target.value)}
              placeholder="e.g. 200000 — leave blank for unbounded"
            />
            <p className="text-xs text-muted-foreground">
              Counts non-cached input + output tokens. The goal flips to budget_limited and runs
              one wrap-up turn when this is exceeded.
            </p>
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting}>
              {submitting ? "Creating…" : "Create goal"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
