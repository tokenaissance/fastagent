package sandbox

import sandboxport "github.com/fastclaw-ai/fastclaw/internal/sandbox/port"

// The cross-pod lease contract lives in internal/sandbox/port so the
// relational store can implement it without importing this adapter package.
type SandboxLeaseRecord = sandboxport.SandboxLeaseRecord
type SandboxLeaseStore = sandboxport.SandboxLeaseStore

const DefaultSandboxLeaseTTL = sandboxport.DefaultSandboxLeaseTTL
