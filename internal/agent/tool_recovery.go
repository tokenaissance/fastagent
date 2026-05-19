package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// maybeRecoverToolCalls runs recoverToolCallsFromContent on the
// response when the model returned no native tool_calls but did emit
// text. On a successful recovery it mutates the response in place:
// splices the parsed calls into resp.ToolCalls, replaces resp.Content
// with the residual (any human-readable preamble before the XML), and
// clears RawAssistant so the next round's history replay rebuilds the
// assistant message from the now-recovered fields instead of replaying
// the bad XML payload back into the model's context.
//
// Logs once at info level with the agent + model + recovered tool names
// so an operator can see how often the path triggers and for which
// (model, prompt) combinations — without this signal the recovery
// silently papers over genuine prompt/tool-definition bugs that should
// surface.
func (a *Agent) maybeRecoverToolCalls(resp *provider.Response) {
	if resp == nil || resp.HasToolCalls() || resp.Content == "" {
		return
	}
	recovered, residual := recoverToolCallsFromContent(resp.Content)
	if len(recovered) == 0 {
		return
	}
	names := make([]string, 0, len(recovered))
	for _, tc := range recovered {
		names = append(names, tc.Function.Name)
	}
	slog.Info("recovered_tool_calls from assistant content",
		"agent", a.name, "model", a.model, "count", len(recovered),
		"tools", names)
	resp.ToolCalls = recovered
	resp.Content = residual
	resp.RawAssistant = nil
}

// recoverToolCallsFromContent parses tool-call attempts that some open-
// source models (DeepSeek, Qwen variants) emit as XML in the assistant
// `content` field instead of using the OpenAI Chat Completions
// `tool_calls` schema. The shape we recognize is the Anthropic
// function_calls XML many of those models were trained on:
//
//	<invoke name="exec">
//	  <parameter name="command" string="true">echo hi</parameter>
//	  <parameter name="timeout" string="false">15</parameter>
//	</invoke>
//
// Returned tool calls have synthetic IDs (`recovered_…`) so the
// downstream tool_result message can be paired with the original
// assistant message — IDs the model itself proposed would collide with
// real OpenAI-style IDs from later turns.
//
// The second return is the original content with every matched invoke
// (and any optional <tool_calls>/<function_calls>/<DSML> wrapper) block
// stripped, so the saved assistant message doesn't keep the raw XML
// alongside the recovered structured calls — that would double-bill the
// tool call in the chat UI and confuse the next round.
//
// Returns (nil, content) when no invoke blocks match; the caller can
// then fall through to the normal "model didn't ask for a tool" branch
// without the recovery path adding any cost.
func recoverToolCallsFromContent(content string) ([]provider.ToolCall, string) {
	if !strings.Contains(content, "<invoke") {
		return nil, content
	}
	matches := invokeRE.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return nil, content
	}
	calls := make([]provider.ToolCall, 0, len(matches))
	for i, m := range matches {
		// m: [whole-lo, whole-hi, name-lo, name-hi, body-lo, body-hi]
		name := content[m[2]:m[3]]
		body := content[m[4]:m[5]]
		args := parseInvokeParameters(body)
		argJSON, err := json.Marshal(args)
		if err != nil {
			// Marshal of map[string]any can only fail on cycles, which
			// our scalar map can't produce — but keep the fail-open
			// behavior anyway: skip this one rather than panicking and
			// killing the whole turn.
			continue
		}
		calls = append(calls, provider.ToolCall{
			ID:   fmt.Sprintf("recovered_%d", i),
			Type: "function",
			Function: provider.FunctionCall{
				Name:      name,
				Arguments: string(argJSON),
			},
		})
	}
	if len(calls) == 0 {
		return nil, content
	}
	// Strip the recovered XML out of the content. We pull every
	// <invoke> block, plus the common outer wrappers (tool_calls,
	// function_calls, DSML) so the residual text is just the model's
	// human-readable preamble — if any — without dangling tags.
	stripped := stripRE.ReplaceAllString(content, "")
	stripped = strings.TrimSpace(stripped)
	return calls, stripped
}

// invokeRE pulls one <invoke name="..."> ... </invoke> block at a time.
//   - non-greedy `(?s).*?` so two adjacent invokes don't merge into one.
//   - tolerates `<invoke>` with no name attribute by demanding a quote-
//     delimited name= attribute up front; the parser is recovery-only
//     and a name-less invoke can't be turned into a tool call anyway.
var invokeRE = regexp.MustCompile(`(?s)<invoke\s+name="([^"]+)"\s*>(.*?)</invoke>`)

// parameterRE matches `<parameter name="key" string="true|false">VALUE</parameter>`.
// The `string` attribute is the type hint:
//   - string="true"  → VALUE is the JSON string contents (we re-quote it).
//   - string="false" → VALUE is raw JSON (number, bool, array, object).
//
// Absent attribute defaults to treating VALUE as a string — that's the
// safest interpretation when the model omits the hint, and it's what
// human-readable XML would imply.
var parameterRE = regexp.MustCompile(`(?s)<parameter\s+name="([^"]+)"(?:\s+string="(true|false)")?\s*>(.*?)</parameter>`)

// stripRE finds:
//   - every invoke block (so we can drop it after a successful parse)
//   - the optional outer <tool_calls> / <function_calls> / <DSML>
//     wrappers some models add (open AND close tags), so the residual
//     content doesn't keep a dangling `</tool_calls>`.
var stripRE = regexp.MustCompile(`(?s)<invoke\s+name="[^"]+"\s*>.*?</invoke>|</?(?:tool_calls|function_calls|DSML)\s*/?>`)

// parseInvokeParameters walks the parameters inside one invoke body and
// returns the assembled arguments map. Unknown/malformed parameters are
// silently skipped — we prefer to call the tool with whatever args we
// could parse rather than reject the recovery wholesale.
func parseInvokeParameters(body string) map[string]any {
	out := map[string]any{}
	for _, p := range parameterRE.FindAllStringSubmatch(body, -1) {
		// p[1]=name, p[2]="true"/"false"/"", p[3]=raw VALUE
		name := p[1]
		typeHint := p[2]
		raw := p[3]
		if typeHint == "false" {
			// Raw JSON value. If it doesn't parse, fall back to string —
			// better to ship a wrong-typed arg than drop the parameter
			// entirely (the tool itself can usually coerce numeric
			// strings, and the loop's BeforeToolCall hook logs the args
			// so an operator can see what came through).
			var v any
			if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &v); err == nil {
				out[name] = v
				continue
			}
		}
		out[name] = raw
	}
	return out
}
