// Package store provides a pluggable storage backend for FastClaw.
// Default: file-based. For production: database-backed (postgres/sqlite).
// All persistent state is agent-scoped.
package store

import (
	"context"
	"encoding/json"
	"time"
)

// Store is the unified interface for all persistent data.
type Store interface {
	// Config (global)
	GetConfig(ctx context.Context) (*GlobalConfig, error)
	SaveConfig(ctx context.Context, cfg *GlobalConfig) error

	// Agents
	ListAgents(ctx context.Context) ([]AgentRecord, error)
	GetAgent(ctx context.Context, agentID string) (*AgentRecord, error)
	SaveAgent(ctx context.Context, agent *AgentRecord) error
	DeleteAgent(ctx context.Context, agentID string) error

	// Sessions (per agent)
	GetSession(ctx context.Context, agentID, sessionKey string) (*SessionRecord, error)
	SaveSession(ctx context.Context, agentID, sessionKey string, session *SessionRecord) error
	ListSessions(ctx context.Context, agentID string) ([]SessionMeta, error)
	DeleteSession(ctx context.Context, agentID, sessionKey string) error
	RenameSession(ctx context.Context, agentID, sessionKey, title string) error

	// Memory (per agent)
	GetMemory(ctx context.Context, agentID string) (string, error)
	SaveMemory(ctx context.Context, agentID, content string) error

	// Workspace files (per agent: SOUL.md, etc.)
	GetWorkspaceFile(ctx context.Context, agentID, filename string) ([]byte, error)
	SaveWorkspaceFile(ctx context.Context, agentID, filename string, data []byte) error
	DeleteWorkspaceFile(ctx context.Context, agentID, filename string) error
	ListWorkspaceFiles(ctx context.Context, agentID string) ([]string, error)

	// API keys (control plane: programmatic access tokens). Storage holds the
	// SHA256 of the token, never plaintext — callers receive the plaintext
	// once at create/rotate and are responsible for capturing it.
	ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error)
	GetAPIKey(ctx context.Context, id string) (*APIKeyRecord, error)
	CreateAPIKey(ctx context.Context, ak *APIKeyRecord) error
	DeleteAPIKey(ctx context.Context, id string) error
	// RotateAPIKey replaces the existing key_hash + key_prefix in-place. The
	// id stays the same; previously-issued tokens become invalid immediately.
	RotateAPIKey(ctx context.Context, id, keyHash, keyPrefix string) error
	// LookupAPIKeyByHash is the auth hot path. SHA256(token) → id, ok.
	LookupAPIKeyByHash(ctx context.Context, keyHash string) (string, bool, error)

	// Agent bindings (which API key owns which agent). Each agent has at
	// most one owner; admin token bypasses this layer entirely.
	ListAgentBindings(ctx context.Context) (map[string]string, error)
	SetAgentBinding(ctx context.Context, agentID, ownerKeyID string) error
	DeleteAgentBinding(ctx context.Context, agentID string) error

	// Cron Jobs
	ListCronJobs(ctx context.Context) ([]CronJobRecord, error)
	GetCronJob(ctx context.Context, jobID string) (*CronJobRecord, error)
	SaveCronJob(ctx context.Context, job *CronJobRecord) error
	DeleteCronJob(ctx context.Context, jobID string) error
	GetDueCronJobs(ctx context.Context, now time.Time) ([]CronJobRecord, error)
	LockCronJob(ctx context.Context, jobID, instanceID string) (bool, error)
	UpdateCronJobRun(ctx context.Context, jobID string, lastRun, nextRun time.Time) error

	Close() error
}

// CronJobRecord holds a scheduled job.
type CronJobRecord struct {
	ID        string     `json:"id"`
	AgentID   string     `json:"agentId"`
	Name      string     `json:"name"`
	Type      string     `json:"type"`
	Schedule  string     `json:"schedule"`
	Message   string     `json:"message"`
	Channel   string     `json:"channel"`
	ChatID    string     `json:"chatId"`
	AccountID string     `json:"accountId"`
	Timezone  string     `json:"timezone"`
	Enabled   bool       `json:"enabled"`
	LastRun   *time.Time `json:"lastRun,omitempty"`
	NextRun   *time.Time `json:"nextRun,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// GlobalConfig holds the global configuration.
type GlobalConfig struct {
	Data      map[string]interface{} `json:"data"`
	CreatedAt time.Time              `json:"createdAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

// APIKeyRecord is one entry in the API key registry.
//
// `KeyHash` is SHA256(token) — what the auth hot path looks up. `KeyPrefix`
// is a small slice of the plaintext (e.g. "fc_a1b2c3") kept for UI display
// of an otherwise unrecoverable hash. The full plaintext is shown to the
// caller exactly once at creation/rotation and never persisted.
type APIKeyRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	KeyHash   string    `json:"keyHash"`
	KeyPrefix string    `json:"keyPrefix,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// AgentRecord is the persisted state for one agent.
type AgentRecord struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Model     string                 `json:"model"`
	Config    map[string]interface{} `json:"config"`
	CreatedAt time.Time              `json:"createdAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

// SessionRecord holds a conversation session.
type SessionRecord struct {
	Messages  []SessionMessage `json:"messages"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// SessionMessage is a single message in a session.
type SessionMessage struct {
	Role         string                 `json:"role"`
	Content      string                 `json:"content"`
	ToolCalls    interface{}            `json:"toolCalls,omitempty"`
	ToolCallID   string                 `json:"toolCallId,omitempty"`
	Name         string                 `json:"name,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	Timestamp    time.Time              `json:"timestamp"`
	Thinking     string                 `json:"thinking,omitempty"`
	RawAssistant json.RawMessage        `json:"rawAssistant,omitempty"`
}

// SessionMeta is summary info for a session (for listing).
type SessionMeta struct {
	Key          string    `json:"key"`
	Title        string    `json:"title,omitempty"`
	MessageCount int       `json:"messageCount"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// MemoryEntry is one searchable memory log entry.
type MemoryEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	SessionID string    `json:"sessionId,omitempty"`
}

// StorageType identifies the storage backend.
type StorageType string

const (
	StorageFile     StorageType = "file"
	StoragePostgres StorageType = "postgres"
	StorageSQLite   StorageType = "sqlite"
)

// StorageConfig is the config block for choosing and configuring the store.
type StorageConfig struct {
	Type        StorageType `json:"type"`
	DSN         string      `json:"dsn,omitempty"`
	AutoMigrate bool        `json:"autoMigrate,omitempty"`
}
