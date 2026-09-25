package agent

import "context"

/**
 * [INPUT]: nothing; a context key local to this package.
 * [OUTPUT]: ContextWithTurnID / TurnIDFromContext — carry one client-supplied
 *           turn id from the HTTP handler down to the message it becomes.
 * [POS]: Sibling of events.go's ContextWithChatEvents, and for the same reason:
 *        at the HTTP boundary there is no message yet, so a per-turn fact that
 *        must outlive the request has to ride the context until something can
 *        hold it. Here that something is the user message (`bus.InboundMessage.
 *        TurnID` → `provider.Message.Metadata["turnId"]`), which is where every
 *        later reader looks — nothing downstream re-reads the context.
 * [PROTOCOL]: On change, update this header and check docs/session-turn-integrity.md
 *        (the turn facts named there) before adding a second key of this shape.
 */

type turnIDKey struct{}

// ContextWithTurnID attaches the id the client generated for this POST
// (`chatRequest.TurnID`). Empty is a no-op: a turn without one keeps today's
// behaviour exactly (nothing is stamped, nothing is surfaced).
func ContextWithTurnID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, turnIDKey{}, id)
}

// TurnIDFromContext returns the id attached by ContextWithTurnID, or "".
func TurnIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(turnIDKey{}).(string)
	return id
}
