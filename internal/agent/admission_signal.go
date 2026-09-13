package agent

import "context"

// admissionSignalKey carries the channel closed once a turn holds the slot.
type admissionSignalKey struct{}

// WithAdmissionSignal returns a context that reports when the turn started
// running, plus the channel it closes at that moment.
//
// The dashboard uses it to tell a *queued* turn (still waiting for the
// session's slot, so it can still be withdrawn) from a *started* one (past
// the point of no return). Nothing else in the loop needs this: it exists so
// the HTTP layer can offer an honest "cancel queued message".
func WithAdmissionSignal(ctx context.Context) (context.Context, <-chan struct{}) {
	started := make(chan struct{})
	return context.WithValue(ctx, admissionSignalKey{}, started), started
}

// signalAdmission closes the channel installed by WithAdmissionSignal, if any.
// Safe to call on contexts that never installed one, and safe to call once per
// turn (a second close would panic, so callers must call it exactly once — the
// acquire site).
func signalAdmission(ctx context.Context) {
	if ch, ok := ctx.Value(admissionSignalKey{}).(chan struct{}); ok {
		close(ch)
	}
}
