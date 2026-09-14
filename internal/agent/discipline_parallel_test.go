package agent

// toolDisciplineContent (agent mode) and modChatbotTools (chatbot mode) are
// parallel copies of the same three rules: which tool to reach for first, how
// skills are invoked, and what to do when a page blocks the fetcher. They never
// appear in one request — the prompt modes are exclusive — so this is not
// duplicated spend. It IS duplicated maintenance: editing one copy and forgetting
// the other is exactly how the two halves of a product drift apart, and nothing
// in the code would say so.
//
// The markers below are the rules both modes must keep. Edit a rule in one copy
// and this fails with the rule's name.

import (
	"strings"
	"testing"
)

func TestAgentAndChatbotDisciplineStayInSync(t *testing.T) {
	agentMode := toolDisciplineContent
	chatbotMode := modChatbotTools(&promptCtx{cb: &ContextBuilder{}})
	if chatbotMode == "" {
		t.Fatal("the chatbot tools module is empty — nothing to compare")
	}

	rules := []struct{ marker, what string }{
		{"web_search", "the search-vs-fetch routing rule"},
		{"search result", "never fetch a search-results page"},
		{"401/403/429", "what to do when the page blocks the fetcher"},
		{"skill", "the skills-before-improvising rule"},
	}
	for _, rule := range rules {
		inAgent := strings.Contains(agentMode, rule.marker)
		inChatbot := strings.Contains(chatbotMode, rule.marker)
		if inAgent != inChatbot {
			t.Errorf("rule %q (%s) is present in agent mode=%v but chatbot mode=%v — the two copies drifted",
				rule.marker, rule.what, inAgent, inChatbot)
		}
		if !inAgent {
			t.Errorf("both copies lost %q (%s)", rule.marker, rule.what)
		}
	}
}
