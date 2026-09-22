package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/codeany-ai/open-agent-sdk-go/costtracker"
	sdktools "github.com/codeany-ai/open-agent-sdk-go/tools"
	sdktypes "github.com/codeany-ai/open-agent-sdk-go/types"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// readOnlyTools lists tools that are safe to run concurrently.
var readOnlyTools = map[string]bool{
	"read_file":     true,
	"list_dir":      true,
	"web_fetch":     true,
	"web_search":    true,
	"memory_search": true,
	"load_skill":    true,
}

// toolAdapter wraps a FastAgent tool as an SDK Tool interface.
type toolAdapter struct {
	name        string
	description string
	params      interface{}
	fn          tools.ToolFunc
}

func (t *toolAdapter) Name() string        { return t.name }
func (t *toolAdapter) Description() string { return t.description }

func (t *toolAdapter) InputSchema() sdktypes.ToolInputSchema {
	// Convert FastAgent params (interface{}) to SDK ToolInputSchema
	if t.params == nil {
		return sdktypes.ToolInputSchema{Type: "object"}
	}
	data, err := json.Marshal(t.params)
	if err != nil {
		return sdktypes.ToolInputSchema{Type: "object"}
	}
	var schema sdktypes.ToolInputSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		return sdktypes.ToolInputSchema{Type: "object"}
	}
	return schema
}

func (t *toolAdapter) Call(ctx context.Context, input map[string]interface{}, tCtx *sdktypes.ToolUseContext) (*sdktypes.ToolResult, error) {
	// The call's id arrives as a reserved argument (see tools.ToolCallIDInputKey:
	// the executor has no other per-call channel). Strip it before the tool's
	// own arguments are marshalled, and hand it to the tool as a context value —
	// so a tool that must name its own call (delegate_task's heartbeats) can,
	// and no tool ever sees an argument it did not ask for.
	if id, ok := input[tools.ToolCallIDInputKey].(string); ok {
		delete(input, tools.ToolCallIDInputKey)
		ctx = tools.WithToolCallID(ctx, id)
	}
	// Convert input map to JSON for FastAgent's ToolFunc
	argsJSON, err := json.Marshal(input)
	if err != nil {
		return &sdktypes.ToolResult{IsError: true, Error: err.Error()}, nil
	}

	result, err := t.fn(ctx, json.RawMessage(argsJSON))
	if err != nil {
		errText := result
		if errText != "" {
			errText += "\n"
		}
		errText += err.Error()
		return &sdktypes.ToolResult{
			IsError: true,
			Error:   errText,
			Content: []sdktypes.ContentBlock{{
				Type: sdktypes.ContentBlockText,
				Text: errText,
			}},
		}, nil
	}

	return &sdktypes.ToolResult{
		Content: []sdktypes.ContentBlock{{
			Type: sdktypes.ContentBlockText,
			Text: result,
		}},
	}, nil
}

func (t *toolAdapter) IsConcurrencySafe(input map[string]interface{}) bool {
	return readOnlyTools[t.name]
}

func (t *toolAdapter) IsReadOnly(input map[string]interface{}) bool {
	return readOnlyTools[t.name]
}

// sdkEngine wraps SDK components for concurrent tool execution and cost tracking.
type sdkEngine struct {
	costTracker *costtracker.Tracker
	sessionID   string
}

// newSDKEngine creates a new SDK engine with cost tracking.
func newSDKEngine(sessionID string) *sdkEngine {
	return &sdkEngine{
		costTracker: costtracker.NewTracker(sessionID),
		sessionID:   sessionID,
	}
}

// buildSDKRegistry converts FastAgent's tool registry into an SDK registry.
func buildSDKRegistry(fcRegistry *tools.Registry) *sdktools.Registry {
	sdkReg := sdktools.NewRegistry()
	for _, def := range fcRegistry.Definitions() {
		fn := fcRegistry.GetFunc(def.Function.Name)
		if fn == nil {
			continue
		}
		sdkReg.Register(&toolAdapter{
			name:        def.Function.Name,
			description: def.Function.Description,
			params:      def.Function.Parameters,
			fn:          fn,
		})
	}
	return sdkReg
}

// toolCallResult holds the result of a single tool call with metadata.
type toolCallResult struct {
	toolCallID string
	toolName   string
	result     string
	err        error
}

// parseToolCallArguments turns one call's argument text into the map the tool
// sees — or into the failure the model must hear instead of running it.
//
// Empty arguments are a call, not a broken one: providers send "" for a call
// that takes no parameters, and the tool should see the same {} it would see for
// "{}". Everything else has to parse. A truncated call must never reach a tool
// as though the model had said something.
func (e *sdkEngine) parseToolCallArguments(tc provider.ToolCall) (map[string]interface{}, *toolCallResult) {
	raw := tc.Function.Arguments
	if strings.TrimSpace(raw) == "" {
		return map[string]interface{}{}, nil
	}
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		slog.Warn("tool call arguments are not valid JSON; the tool was not run",
			"session", e.sessionID, "tool", tc.Function.Name, "call", tc.ID,
			"bytes", len(raw), "error", err)
		return nil, &toolCallResult{
			toolCallID: tc.ID,
			toolName:   tc.Function.Name,
			result:     uncallableArgumentsMessage(tc.Function.Name, len(raw)),
			err:        fmt.Errorf("%s: arguments are not valid JSON: %w", tc.Function.Name, err),
		}
	}
	if input == nil { // a literal "null" parses cleanly into a nil map
		input = map[string]interface{}{}
	}
	return input, nil
}

// uncallableArgumentsMessage is what the model hears when its own output was cut
// off mid-call. It names the cause and the size, and it may not hand the model a
// downstream tool's guess about a missing key: on 2026-09-22 "write_file: path
// is required" for a call that did carry a path sent the model off to re-emit
// the whole document, straight back into the cap that had cut it off.
func uncallableArgumentsMessage(toolName string, size int) string {
	return fmt.Sprintf(
		"%s was not run: its arguments are not valid JSON (%d bytes received). "+
			"The call was cut off before it finished — most often because the response hit the output token limit. "+
			"Nothing was executed. Re-send the work in smaller pieces; for write_file, write a short first chunk and append the rest with edit_file.",
		toolName, size,
	)
}

// executeToolsConcurrently runs tool calls using the SDK's executor.
//
// Each call is launched on its own instead of handing the executor one batch, so
// the caller learns about a result the moment THAT call returns: onResult fires
// per call, while the returned slice stays in declared order — the shape the
// conversation history needs. Which calls overlap does not change; the
// partition below is the executor's own rule (concurrency-safe tools run in
// parallel, the rest strictly one at a time).
func (e *sdkEngine) executeToolsConcurrently(ctx context.Context, fcRegistry *tools.Registry, toolCalls []provider.ToolCall, workspace string, onResult func(toolCallResult)) []toolCallResult {
	sdkReg := buildSDKRegistry(fcRegistry)
	executor := sdktools.NewExecutor(sdkReg, nil, &sdktypes.ToolUseContext{
		WorkingDir: workspace,
		AbortCtx:   ctx,
	})

	// Convert FastAgent tool calls to SDK format. A call whose arguments do not
	// parse is never converted: it fails here, in its own words, and no tool runs
	// on it. Until 2026-09-22 the bridge substituted {"_raw": <the broken text>}
	// and dispatched it anyway, so a call cut off mid-string came back as the
	// tool's guess about a missing key ("write_file: path is required" for a call
	// that carried a path) and the model went off to re-emit the 17 KB document
	// it had just failed to emit.
	calls := make([]sdktools.ToolCallRequest, len(toolCalls))
	preflight := make([]*toolCallResult, len(toolCalls))
	for i, tc := range toolCalls {
		input, failure := e.parseToolCallArguments(tc)
		if failure != nil {
			preflight[i] = failure
			continue
		}
		calls[i] = sdktools.ToolCallRequest{
			ToolUseID: tc.ID,
			ToolName:  tc.Function.Name,
			Input:     input,
		}
		// Carry the call's id to the tool body: the SDK's ToolUseContext is one
		// pointer shared by every call in this batch, so the call's own arguments
		// are the only per-call channel there is. The adapter strips it again.
		input[tools.ToolCallIDInputKey] = tc.ID
	}

	start := time.Now()
	results := make([]toolCallResult, len(toolCalls))
	run := func(i int) {
		r := toolCallResult{}
		if preflight[i] != nil {
			r = *preflight[i]
		} else {
			r = convertToolResponses(toolCalls[i:i+1], executor.RunTools(ctx, calls[i:i+1]))[0]
		}
		results[i] = r
		if onResult != nil {
			onResult(r)
		}
	}

	// Same partition the executor applies internally: concurrency-safe calls run
	// in parallel, the rest one at a time in declared order. Driving the launches
	// here — rather than letting the executor run the whole batch — is what lets
	// a finished call be reported without waiting for the round's join.
	var parallel, serial []int
	for i := range calls {
		if preflight[i] != nil {
			// Nothing to dispatch: the call already failed and is reported at its
			// declared position on the in-order path below.
			serial = append(serial, i)
			continue
		}
		if tool := sdkReg.Get(calls[i].ToolName); tool != nil && tool.IsConcurrencySafe(calls[i].Input) {
			parallel = append(parallel, i)
		} else {
			serial = append(serial, i)
		}
	}
	var wg sync.WaitGroup
	for _, i := range parallel {
		wg.Add(1)
		go func(idx int) { defer wg.Done(); run(idx) }(i)
	}
	wg.Wait()
	for _, i := range serial {
		run(i)
	}
	e.costTracker.AddToolDuration(time.Since(start))

	return results
}

// convertToolResponses maps the executor's responses onto the calls, keyed by
// tool_use id. A call the executor did not answer (context cancel, an executor
// poisoned by a sandbox-creation failure) still gets a paired failure result:
// an orphaned tool_use id makes the next API call 400 invalid_request_error.
func convertToolResponses(toolCalls []provider.ToolCall, responses []sdktools.ToolCallResponse) []toolCallResult {
	byID := make(map[string]sdktools.ToolCallResponse, len(responses))
	for _, resp := range responses {
		byID[resp.ToolUseID] = resp
	}
	results := make([]toolCallResult, len(toolCalls))
	for i, tc := range toolCalls {
		resp, ok := byID[tc.ID]
		if !ok {
			results[i] = toolCallResult{
				toolCallID: tc.ID,
				toolName:   tc.Function.Name,
				result:     "tool execution did not return a result (sandbox or executor failure — check gateway logs)",
				err:        fmt.Errorf("no response from executor for tool_use %s", tc.ID),
			}
			continue
		}
		var resultText string
		if resp.Result != nil {
			if resp.Result.IsError {
				resultText = resp.Result.Error
				if resultText == "" && len(resp.Result.Content) > 0 {
					resultText = resp.Result.Content[0].Text
				}
				results[i] = toolCallResult{
					toolCallID: resp.ToolUseID,
					toolName:   tc.Function.Name,
					result:     resultText + "\n[Analyze the error above and try a different approach.]",
					err:        fmt.Errorf("%s", resultText),
				}
				continue
			}
			// Extract text from content blocks
			var parts []string
			for _, cb := range resp.Result.Content {
				if cb.Text != "" {
					parts = append(parts, cb.Text)
				}
			}
			resultText = strings.Join(parts, "\n")
		}
		if resp.Error != nil {
			results[i] = toolCallResult{
				toolCallID: resp.ToolUseID,
				toolName:   tc.Function.Name,
				result:     resultText,
				err:        resp.Error,
			}
		} else {
			results[i] = toolCallResult{
				toolCallID: resp.ToolUseID,
				toolName:   tc.Function.Name,
				result:     resultText,
			}
		}
	}
	return results
}
