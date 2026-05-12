package goal

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed templates/*.md
var templatesFS embed.FS

var (
	continuationTmpl     = mustParse("continuation.md")
	budgetLimitTmpl      = mustParse("budget_limit.md")
	objectiveUpdatedTmpl = mustParse("objective_updated.md")
)

func mustParse(name string) *template.Template {
	body, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		panic(fmt.Sprintf("goal: embedded template %s not found: %v", name, err))
	}
	t, err := template.New(name).Parse(string(body))
	if err != nil {
		panic(fmt.Sprintf("goal: template %s parse error: %v", name, err))
	}
	return t
}

// promptVars is the view passed to the embedded templates. Field names
// match the {{ .X }} references in templates/*.md.
type promptVars struct {
	Objective       string
	TokensUsed      int64
	TokenBudget     string // rendered as a string so we can show "none" / "unbounded"
	RemainingTokens string
	TimeUsedSeconds int64
}

func newPromptVars(g *Goal) promptVars {
	v := promptVars{
		Objective:       EscapeXMLText(g.Objective),
		TokensUsed:      g.TokensUsed,
		TimeUsedSeconds: g.TimeUsedSeconds,
	}
	if g.TokenBudget == nil {
		v.TokenBudget = "none"
		v.RemainingTokens = "unbounded"
	} else {
		v.TokenBudget = fmt.Sprintf("%d", *g.TokenBudget)
		remaining, _ := g.RemainingTokens()
		v.RemainingTokens = fmt.Sprintf("%d", remaining)
	}
	return v
}

// ContinuationPrompt renders the per-turn audit prompt injected while
// the goal is Active.
func ContinuationPrompt(g *Goal) string {
	return render(continuationTmpl, newPromptVars(g))
}

// BudgetLimitPrompt renders the wrap-up prompt injected once on the
// turn that flips a goal to BudgetLimited.
func BudgetLimitPrompt(g *Goal) string {
	return render(budgetLimitTmpl, newPromptVars(g))
}

// ObjectiveUpdatedPrompt renders the prompt injected when the user
// edits the objective mid-run.
func ObjectiveUpdatedPrompt(g *Goal) string {
	return render(objectiveUpdatedTmpl, newPromptVars(g))
}

func render(t *template.Template, v promptVars) string {
	var buf bytes.Buffer
	if err := t.Execute(&buf, v); err != nil {
		// Templates are embedded and validated at init time — a render
		// error here means the variables struct drifted from the template.
		panic(fmt.Sprintf("goal: %s render: %v", t.Name(), err))
	}
	return buf.String()
}

// EscapeXMLText replaces the three characters that would otherwise let
// user-supplied objective text break out of the <objective> wrapper or
// inject a forged </goal_context> close tag. Mirrors codex-rs/core/src/
// goals.rs:1515-1520.
func EscapeXMLText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
