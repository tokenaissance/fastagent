package agent

// The todo.md section of the task-delegation module is a contract with three
// other pieces of code, so its RULES have to survive any rewriting:
//
//   - the chat panel re-fetches on every write that touches the file and hides
//     itself when the file is empty (web/src/components/chat-screen.tsx:517-525);
//   - `parseTodoMarkdown` parses the literal checkbox syntax and merges duplicate
//     step text, which was added in the same commit as the write-once rule
//     (04c33d5 "dedup todo.md items, tighten prompt against duplicate writes"):
//     a second `write_file` stacks an old plan on a partial new one and the panel
//     showed the same step twice;
//   - `handleChatTodo` routes a BARE `todo.md` into the session's workspace, so
//     the "don't path it" rule is load-bearing, not stylistic.
//
// The size budget is the other half: the section is sent on every request, so a
// rewrite that keeps the rules in fewer words is a win, and one that re-inflates it
// fails here.

import (
	"strings"
	"testing"
)

const todoSectionBudget = 1700

func todoSection(t *testing.T) string {
	t.Helper()
	idx := strings.Index(taskDelegationContent, "# Progress tracking via todo.md")
	if idx < 0 {
		t.Fatal("the todo.md section vanished from the task-delegation module")
	}
	return taskDelegationContent[idx:]
}

func TestTodoPromptKeepsEveryRule(t *testing.T) {
	section := todoSection(t)

	rules := []struct {
		marker string
		why    string
	}{
		{"- [ ]", "the pending checkbox syntax parseTodoMarkdown reads"},
		{"- [x]", "the completed checkbox syntax parseTodoMarkdown reads"},
		{"bare", "todo.md must stay a bare path — handleChatTodo routes it by name"},
		{"flat list", "no nested checkboxes: the parser sees only top-level items"},
		{"once per turn", "a second write_file stacks an old plan on a partial new one (04c33d5)"},
		{"edit_file", "later updates target one line instead of rewriting the file"},
		{"progress panel", "the user-visible reason the file exists"},
		{"3+", "when the checklist is required"},
		{"skip", "one-shot and conversational turns are excluded"},
	}
	for _, rule := range rules {
		if !strings.Contains(section, rule.marker) {
			t.Errorf("the todo.md section no longer states %q (%s)", rule.marker, rule.why)
		}
	}
}

func TestTodoPromptStaysUnderItsBudget(t *testing.T) {
	section := todoSection(t)
	if len(section) > todoSectionBudget {
		t.Fatalf("the todo.md section is %d chars (budget %d) — it is sent on every request:\n%s",
			len(section), todoSectionBudget, section)
	}
}
