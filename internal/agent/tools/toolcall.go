package tools

import "context"

// The tool_use id, for the one tool that has to name its own call.
//
// The SDK's executor hands a tool: Call(ctx, input, tCtx) — and tCtx is ONE
// pointer shared by every call in the batch. So there is no per-call channel
// from the executor to the tool body except the call's own arguments. The id
// therefore rides a reserved key in the input map (ToolCallIDInputKey, stripped
// by the adapter before the tool sees its arguments) and is re-stamped on the
// tool's context here.
//
// Why it is needed at all: delegate_task's sub-agent emits heartbeats
// (`subagent_progress`) that the dashboard renders on one tool row. Without an
// id the dashboard can only guess which call a heartbeat belongs to, and its
// guess — "the first call with no result yet" — is wrong for a fan-out, because
// every tool result in a round is emitted only after the whole round returns
// (2026-09-21: three serial sub-agents, and all three heartbeats were drawn on
// the first row).
type toolCallIDKey struct{}

// ToolCallIDInputKey is the reserved argument key the SDK bridge puts a call's
// id under. Tools never see it: the adapter deletes it before invoking them.
const ToolCallIDInputKey = "_fastagent_tool_call_id"

// WithToolCallID stamps a tool's context with the id of the call it is running.
// The SDK bridge is the only caller.
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

// ToolCallID is the call id stamped above, if the caller had one.
func ToolCallID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(toolCallIDKey{}).(string)
	return id, ok && id != ""
}
