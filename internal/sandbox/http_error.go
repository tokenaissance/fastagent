package sandbox

// Provider HTTP failures are classified by status code, never by message text.
//
// Both cloud backends answer "this instance is gone" with a status (e2b: 502
// or 404; boxlite: 404, 502 or 410) and the recovery is to rebuild the sandbox.
// Deciding that from the error string made anything that merely mentioned a
// code — a 500 whose body quotes "HTTP 404", an error wrapping a response —
// cost a sandbox. Every provider response therefore becomes one of these, and
// the classifiers read the status.

import (
	"errors"
	"fmt"
)

type sandboxHTTPError struct {
	// op is the human-readable operation, e.g. "e2b exec" or "boxlite write".
	// Empty produces a bare "HTTP <status>: <body>".
	op     string
	status int
	body   string
	// cause is the transport error this response accompanied (a failed
	// WebSocket upgrade, say). Nil when the response is the whole story.
	cause error
}

func (e *sandboxHTTPError) Error() string {
	switch {
	case e.cause != nil:
		return fmt.Sprintf("%s: %v (HTTP %d: %s)", e.op, e.cause, e.status, e.body)
	case e.op == "":
		return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
	default:
		return fmt.Sprintf("%s HTTP %d: %s", e.op, e.status, e.body)
	}
}

func (e *sandboxHTTPError) Unwrap() error { return e.cause }

// statusCodeOf returns the provider status an error carries, if any.
func statusCodeOf(err error) (int, bool) {
	var httpErr *sandboxHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.status, true
	}
	return 0, false
}
