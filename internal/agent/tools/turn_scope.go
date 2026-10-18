package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// TurnScope holds every mutable value that belongs to one admitted turn.
//
// The Registry carries only boot-time configuration. A tool call reads its
// session, project, chatter, message address, executor and failure memory from
// the TurnScope carried on its context, so two turns of the same agent can run
// at the same time without observing each other's values.
type TurnScope struct {
	SessionID string
	ProjectID string
	Executor  sandbox.Executor
	// SkillDirs is the layered skill search path resolved for this turn. It
	// carries the chatter's personal layer, so load_skill and exec must read
	// it from here rather than from a registry-wide closure that the last
	// refresh overwrote. The slice is immutable after bindSession builds the
	// scope.
	SkillDirs []string
	// SkillEnv resolves a skill's environment variables against this turn's
	// skill set. It carries the chatter's personal layer and is nil outside an
	// admitted turn, where exec falls back to the registry's boot-time provider.
	SkillEnv SkillEnvProvider

	MessageChannel   string
	MessageAccountID string
	MessageChatID    string

	GoalSessionKey string
	ChatterUserID  string
	CallerIsAdmin  bool

	mu           sync.RWMutex
	codingSubdir string
	turnFails    map[turnFailKey]string
}

type turnScopeKey struct{}

// WithTurnScope returns a context that carries scope. A nil scope leaves ctx
// unchanged, so callers outside a chat turn keep the documented empty-scope
// behaviour instead of panicking.
func WithTurnScope(ctx context.Context, scope *TurnScope) context.Context {
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, turnScopeKey{}, scope)
}

// TurnScopeFrom returns the turn scope carried by ctx, or nil when the call is
// not inside an admitted turn.
func TurnScopeFrom(ctx context.Context) *TurnScope {
	if ctx == nil {
		return nil
	}
	scope, _ := ctx.Value(turnScopeKey{}).(*TurnScope)
	return scope
}

// CodingSubdir returns the active app subfolder for this turn.
func (s *TurnScope) CodingSubdir() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.codingSubdir
}

// SetCodingSubdir redirects this turn's file tools into dir. It is safe to
// call while sibling tools of the same turn run concurrently.
func (s *TurnScope) SetCodingSubdir(dir string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codingSubdir = dir
}

// ScopeSessionID is the session segment file tools pass to the workspace
// store. workspace.WriteScope owns the rule that a project collapses the
// segment; this method only supplies the turn's two inputs.
func (s *TurnScope) ScopeSessionID() string {
	if s == nil {
		return ""
	}
	return workspace.WriteScope(s.ProjectID, s.SessionID).SessionID
}

// WSPath maps a tool-supplied path into the active coding subdir. It is
// idempotent: a path already under the subdir is returned unchanged.
func (s *TurnScope) WSPath(p string) string {
	subdir := s.CodingSubdir()
	if subdir == "" {
		return p
	}
	clean := strings.TrimLeft(filepath.ToSlash(p), "/")
	if clean == subdir || strings.HasPrefix(clean, subdir+"/") {
		return clean
	}
	return subdir + "/" + clean
}

// RecordToolFailure stashes a short error summary keyed by (toolName, args)
// for the rest of this turn.
func (s *TurnScope) RecordToolFailure(toolName string, rawArgs string, errSummary string) {
	if s == nil || errSummary == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnFails == nil {
		s.turnFails = map[turnFailKey]string{}
	}
	s.turnFails[turnFailKey{tool: toolName, hash: sha256.Sum256([]byte(rawArgs))}] = errSummary
}

// PriorFailure returns the summary of the previous failure for (toolName,
// args) within this turn, or "" when the call was not seen.
func (s *TurnScope) PriorFailure(toolName string, rawArgs string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.turnFails == nil {
		return ""
	}
	return s.turnFails[turnFailKey{tool: toolName, hash: sha256.Sum256([]byte(rawArgs))}]
}

// sessionID returns the chat/session identifier of the in-flight turn.
func (r *Registry) sessionID(ctx context.Context) string {
	if scope := TurnScopeFrom(ctx); scope != nil {
		return scope.SessionID
	}
	return ""
}

// projectID returns the project scope of the in-flight turn.
func (r *Registry) projectID(ctx context.Context) string {
	if scope := TurnScopeFrom(ctx); scope != nil {
		return scope.ProjectID
	}
	return ""
}

// chatterUserID returns the per-turn chatter, falling back to the UserSpace
// owner when the turn did not resolve one.
func (r *Registry) chatterUserID(ctx context.Context) string {
	if scope := TurnScopeFrom(ctx); scope != nil && scope.ChatterUserID != "" {
		return scope.ChatterUserID
	}
	return r.userID
}

// effectiveUserID returns the user the in-flight turn acts as.
func (r *Registry) effectiveUserID(ctx context.Context) string {
	return r.chatterUserID(ctx)
}

// callerIsAdmin reports the per-turn owner/admin flag. A missing scope is
// fail-closed.
func (r *Registry) callerIsAdmin(ctx context.Context) bool {
	scope := TurnScopeFrom(ctx)
	return scope != nil && scope.CallerIsAdmin
}

// goalSessionKey returns the durable session_key of the in-flight turn.
func (r *Registry) goalSessionKey(ctx context.Context) string {
	if scope := TurnScopeFrom(ctx); scope != nil {
		return scope.GoalSessionKey
	}
	return ""
}

func (r *Registry) messageChannel(ctx context.Context) string {
	if scope := TurnScopeFrom(ctx); scope != nil {
		return scope.MessageChannel
	}
	return ""
}

func (r *Registry) messageAccountID(ctx context.Context) string {
	if scope := TurnScopeFrom(ctx); scope != nil {
		return scope.MessageAccountID
	}
	return ""
}

func (r *Registry) messageChatID(ctx context.Context) string {
	if scope := TurnScopeFrom(ctx); scope != nil {
		return scope.MessageChatID
	}
	return ""
}

// executorFor returns the executor bound to this turn, or nil.
func (r *Registry) executorFor(ctx context.Context) sandbox.Executor {
	if scope := TurnScopeFrom(ctx); scope != nil {
		return scope.Executor
	}
	return nil
}

// unavailableExecutor answers every sandbox operation with one explicit error.
// It lets a sandboxRequired turn whose bind failed keep the store-backed
// branches working while every path that truly needs a sandbox fails closed.
type unavailableExecutor struct{}

const errSandboxUnavailable = "sandbox required but no executor available — check that the sandbox backend (docker / e2b) is reachable and the configured image can start"

func (unavailableExecutor) Exec(context.Context, string, time.Duration) (string, error) {
	return "", errors.New(errSandboxUnavailable)
}

func (unavailableExecutor) ReadFile(context.Context, string) (string, error) {
	return "", errors.New(errSandboxUnavailable)
}

func (unavailableExecutor) WriteFile(context.Context, string, string) (string, error) {
	return "", errors.New(errSandboxUnavailable)
}

func (unavailableExecutor) ListDir(context.Context, string) (string, error) {
	return "", errors.New(errSandboxUnavailable)
}

func (unavailableExecutor) Backend() string { return "unavailable" }
func (unavailableExecutor) Close() error    { return nil }

// executorOrUnavailable returns the turn's executor, or a value that fails
// every sandbox call with errSandboxUnavailable.
func (r *Registry) executorOrUnavailable(ctx context.Context) sandbox.Executor {
	if ex := r.executorFor(ctx); ex != nil {
		return ex
	}
	return unavailableExecutor{}
}

// sandboxTools reports whether file/exec tools must dispatch through the
// sandbox closure for this call. `sandboxRequired` covers a turn whose
// executor bind failed: the call must refuse the host fallback instead of
// silently running on the pod.
func (r *Registry) sandboxTools(ctx context.Context) bool {
	return r.executorFor(ctx) != nil || r.sandboxRequired.Load()
}

// dualTool routes one tool call to its host or sandbox body. The turn's
// executor selects the body at call time, so one registration serves both
// execution sites and the two bodies cannot drift behind separate schemas.
type dualTool struct {
	registry *Registry
	host     ToolFunc
	sandbox  ToolFunc
}

// Call runs the body that matches the turn's execution site.
func (t dualTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if t.registry.sandboxTools(ctx) {
		return t.sandbox(ctx, args)
	}
	return t.host(ctx, args)
}

// scopeSessionID is the session segment the file tools pass to the workspace
// store.
func (r *Registry) scopeSessionID(ctx context.Context) string {
	if scope := TurnScopeFrom(ctx); scope != nil {
		return scope.ScopeSessionID()
	}
	return ""
}

// wsPath maps a tool-supplied path into the active coding subdir.
func (r *Registry) wsPath(ctx context.Context, p string) string {
	if scope := TurnScopeFrom(ctx); scope != nil {
		return scope.WSPath(p)
	}
	return p
}

// RecordToolFailure records a failed call in the turn's failure memory.
func (r *Registry) RecordToolFailure(ctx context.Context, toolName string, rawArgs string, errSummary string) {
	TurnScopeFrom(ctx).RecordToolFailure(toolName, rawArgs, errSummary)
}

// PriorFailure answers whether (toolName, args) already failed in this turn.
func (r *Registry) PriorFailure(ctx context.Context, toolName string, rawArgs string) string {
	return TurnScopeFrom(ctx).PriorFailure(toolName, rawArgs)
}
