<goal_context>
The user updated the active goal's objective. The previous objective
is superseded by the one below.

<objective>
{{.Objective}}
</objective>

Budget snapshot (unchanged across the edit):
- Tokens consumed: {{.TokensUsed}}
- Token budget: {{.TokenBudget}}
- Tokens remaining: {{.RemainingTokens}}

Reorient this turn around the updated objective. Do not continue work
that only served the old objective unless it also helps the new one.

Same completion discipline applies: only call update_goal with
status="complete" when the updated objective is proven done by
current evidence.
</goal_context>
