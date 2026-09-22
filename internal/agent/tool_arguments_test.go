package agent

// A tool call whose arguments do not parse is not the tool's business.
//
// Model output is capped (max_tokens, 8192 here). A long write_file is the shape
// that hits it: the call arrives cut off mid-string while everything else about
// it looks normal — id, name, and a JSON prefix that starts with {"path". On
// 2026-09-22 prod shipped two of those; the bridge handed the tool
// {"_raw": <the broken text>} and let it run, so the model was told "write_file:
// path is required and must include a filename" for a call that *did* carry a
// path (seq 59) — and set off to re-emit the whole 17 KB document it had just
// failed to emit, hitting the same cap again.
//
// These cases pin the replacement: the call fails naming its own cause, the tool
// never runs, its siblings in the same batch still do, and the call keeps its id
// so no round leaves an orphan tool_use behind.
//
// Falsification: restore the _raw fallback in sdkbridge and the first case fails
// on both counts — the tool ran, and the message blames a missing key.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// cutOffArguments is a tool call that ran out of output budget: a valid prefix,
// no closing quote or brace. The shape prod produced, at a size that fits a test.
const cutOffArguments = `{"path": "quantconnect-2026-09.md", "content": "# QuantConnect 深度调研（2026-09-22 全量重核）\n\n> 核验时间：2026-09-22 15:30 Asia/`

func recordingRegistry(t *testing.T, ran *int, lastArgs *string) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry(t.TempDir(), t.TempDir())
	reg.Register("write_file", "records that it ran", nil,
		func(_ context.Context, raw json.RawMessage) (string, error) {
			*ran++
			*lastArgs = string(raw)
			return "wrote something", nil
		})
	return reg
}

func TestToolCallWithUnparseableArgumentsNeverReachesItsTool(t *testing.T) {
	var ran int
	var lastArgs string
	reg := recordingRegistry(t, &ran, &lastArgs)

	eng := newSDKEngine("sess-truncated-args")
	res := eng.executeToolsConcurrently(context.Background(), reg, []provider.ToolCall{{
		ID:       "call_cut_off",
		Type:     "function",
		Function: provider.FunctionCall{Name: "write_file", Arguments: cutOffArguments},
	}}, t.TempDir(), nil)

	if ran != 0 {
		t.Fatalf("the tool ran %d times on arguments that do not parse (it was handed %q)", ran, lastArgs)
	}
	if len(res) != 1 {
		t.Fatalf("results = %d; the batch still owes the model one paired tool_result", len(res))
	}
	if res[0].toolCallID != "call_cut_off" || res[0].toolName != "write_file" {
		t.Fatalf("result is not paired with its call: %+v", res[0])
	}
	if res[0].err == nil {
		t.Fatalf("the failure is silent: %+v", res[0])
	}

	msg := res[0].result
	if !strings.Contains(msg, "write_file") {
		t.Fatalf("the message does not name the tool whose call was cut off: %q", msg)
	}
	if !strings.Contains(msg, "not valid JSON") {
		t.Fatalf("the message does not name the cause: %q", msg)
	}
	if want := strconv.Itoa(len(cutOffArguments)); !strings.Contains(msg, want) {
		t.Fatalf("the message does not carry the evidence (%s bytes received): %q", want, msg)
	}
	if !strings.Contains(msg, "cut off") {
		t.Fatalf("the message does not say the call was cut off: %q", msg)
	}
	if strings.Contains(msg, "path is required") {
		t.Fatalf("the message is a tool's guess about a key, not the cause: %q", msg)
	}
}

func TestATruncatedCallDoesNotStopItsSiblings(t *testing.T) {
	var ran int
	var lastArgs string
	reg := recordingRegistry(t, &ran, &lastArgs)

	eng := newSDKEngine("sess-truncated-siblings")
	var reported []string
	res := eng.executeToolsConcurrently(context.Background(), reg, []provider.ToolCall{
		{
			ID:       "call_cut_off",
			Type:     "function",
			Function: provider.FunctionCall{Name: "write_file", Arguments: cutOffArguments},
		},
		{
			ID:       "call_fine",
			Type:     "function",
			Function: provider.FunctionCall{Name: "write_file", Arguments: `{"path":"notes.md","content":"hi"}`},
		},
	}, t.TempDir(), func(r toolCallResult) { reported = append(reported, r.toolCallID) })

	if ran != 1 {
		t.Fatalf("the healthy call ran %d times; want exactly once", ran)
	}
	// Asked by key, not by byte: the bridge round-trips arguments through a map,
	// so key order is not part of what arrived. What must hold is that the
	// sibling's failure neither dropped a key nor rewrote a value.
	var got map[string]string
	if err := json.Unmarshal([]byte(lastArgs), &got); err != nil {
		t.Fatalf("the healthy call's arguments are not readable JSON: %q (%v)", lastArgs, err)
	}
	if len(got) != 2 || got["path"] != "notes.md" || got["content"] != "hi" {
		t.Fatalf("the healthy call's arguments changed: %q", lastArgs)
	}
	if len(res) != 2 || res[0].err == nil || res[1].err != nil {
		t.Fatalf("results = %+v; want the cut-off call failed and its sibling clean", res)
	}
	// The streamed half of the contract: every call reports once, by id, so the
	// panel can close both rows and the next API call has no orphan tool_use.
	if len(reported) != 2 || reported[0] != "call_cut_off" || reported[1] != "call_fine" {
		t.Fatalf("onResult saw %q; want each call reported once, in order", reported)
	}
}

// The other side of the same line: no arguments is a valid call, not a broken
// one. Some providers send "" for a call that takes no parameters, and the
// failure above must not swallow it.
func TestAToolCallWithNoArgumentsIsStillACall(t *testing.T) {
	var ran int
	var lastArgs string
	reg := tools.NewRegistry(t.TempDir(), t.TempDir())
	reg.Register("list_cron_jobs", "records that it ran", nil,
		func(_ context.Context, raw json.RawMessage) (string, error) {
			ran++
			lastArgs = string(raw)
			return "no jobs", nil
		})

	eng := newSDKEngine("sess-no-args")
	res := eng.executeToolsConcurrently(context.Background(), reg, []provider.ToolCall{{
		ID:       "call_no_args",
		Type:     "function",
		Function: provider.FunctionCall{Name: "list_cron_jobs", Arguments: ""},
	}}, t.TempDir(), nil)

	if ran != 1 {
		t.Fatalf("a call with empty arguments ran %d times; want it to run (arguments=%q)", ran, lastArgs)
	}
	if len(res) != 1 || res[0].err != nil {
		t.Fatalf("results = %+v; want one clean result", res)
	}
}
