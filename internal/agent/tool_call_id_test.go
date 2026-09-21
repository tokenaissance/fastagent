package agent

// The rule half of `subagent_progress.id`: a tool can name the call it is
// running, and the model's arguments do not grow a key the model never wrote.
//
// The SDK's executor gives a tool Call(ctx, input, tCtx) and one tCtx pointer
// shared by the whole batch, so the call's arguments are the only per-call
// channel there is. The bridge puts the id there under a reserved key and the
// adapter takes it back out — this test pins both ends, because an injection
// that is not stripped is a tool seeing an argument it did not ask for.
//
// Falsification: drop the delete in toolAdapter.Call and the argument assertion
// fails; drop the stamp and the id assertion fails.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func TestToolCallIDReachesTheToolAndNotItsArguments(t *testing.T) {
	var (
		id      string
		haveID  bool
		rawArgs string
	)
	reg := tools.NewRegistry(t.TempDir(), t.TempDir())
	reg.Register("probe", "records the call id and the arguments it was handed", nil,
		func(ctx context.Context, raw json.RawMessage) (string, error) {
			id, haveID = tools.ToolCallID(ctx)
			rawArgs = string(raw)
			return "ok", nil
		})

	eng := newSDKEngine("sess-tool-call-id")
	res := eng.executeToolsConcurrently(context.Background(), reg, []provider.ToolCall{{
		ID:   "call_abc123",
		Type: "function",
		Function: provider.FunctionCall{
			Name:      "probe",
			Arguments: `{"task":"x"}`,
		},
	}}, t.TempDir())

	if len(res) != 1 || res[0].err != nil {
		t.Fatalf("tool run = %+v, want one clean result", res)
	}
	if res[0].toolCallID != "call_abc123" {
		t.Fatalf("result carries id %q, want the call's own", res[0].toolCallID)
	}
	if !haveID || id != "call_abc123" {
		t.Fatalf("tool saw call id %q (present=%v); want the call's own", id, haveID)
	}
	if strings.Contains(rawArgs, tools.ToolCallIDInputKey) {
		t.Fatalf("the tool was handed the bridge's reserved key: %s", rawArgs)
	}
	if rawArgs != `{"task":"x"}` {
		t.Fatalf("the tool's arguments changed: %s", rawArgs)
	}
}
