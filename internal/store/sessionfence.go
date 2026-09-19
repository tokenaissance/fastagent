package store

import "errors"

// The session write fence: the (holder, epoch) pair a transcript write must
// still own when it lands.
//
// Obligation L4(a) of docs/文件系统形式化证明/12-lease-formal-design.md says the
// token has to be checked by the RESOURCE, in the same atomic step as the
// effect — a "read the lease, then write" guard in the caller leaves a TOCTOU
// window exactly the width of one round trip, which is the window a TTL race
// needs. So the fence travels with the write and the write statement itself
// carries the predicate; a refused write reports zero rows.
//
// It is an explicit argument on the two fenced write methods, never a context
// value: the precondition of a write belongs in the write's signature, where a
// reader cannot miss it. The session layer owns the matching type
// (session.TurnFence) and its adapter translates between the two, so no inner
// interface names an outer package's type.
type SessionFence struct {
	HolderID string
	Epoch    int64
}

// ErrSessionFenceLost reports that a fenced write was refused: the lease moved
// to another holder (or expired), so this writer is superseded. The turn must
// stop writing and say so; it must NOT retry — retrying would have to steal the
// lease, which is the other turn's decision.
var ErrSessionFenceLost = errors.New("store: session write refused — this turn no longer holds the session")
