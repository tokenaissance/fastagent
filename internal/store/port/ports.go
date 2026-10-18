// Package port holds the capability ports that consumers of the relational
// store declare. The adapter package internal/store implements them.
//
// The aggregate Store interface and row types still live in internal/store.
// These ports are the narrowed consumer-facing contracts; a caller that needs
// six methods should not depend on the whole 120-method Store.
package port

import (
	"context"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store/model"
)

type ConfigRecord = model.ConfigRecord
type ConfigValue = model.ConfigValue
type ConfigMirror = model.ConfigMirror
type ConfigMirrorReconcile = model.ConfigMirrorReconcile
type AgentRecord = model.AgentRecord
type AgentFileVersion = model.AgentFileVersion
type KnowledgeDoc = model.KnowledgeDoc
type KnowledgeChunkRecord = model.KnowledgeChunkRecord
type CronJobRecord = model.CronJobRecord
type SessionEventRecord = model.SessionEventRecord

type ConfigReader interface {
	GetConfigByName(ctx context.Context, kind, userID, agentID, name string) (*ConfigRecord, error)
	ListConfigs(ctx context.Context, kind, userID, agentID string) ([]ConfigRecord, error)
	ListConfigValues(ctx context.Context, kind, scope, scopeID, namePrefix string) (map[string]ConfigValue, error)
	BatchGetConfigsByAgentIDs(ctx context.Context, kind, name string, agentIDs []string) ([]ConfigRecord, error)
}

type MirrorReader interface {
	GetConfigMirror(ctx context.Context, kind, scope, scopeID, name string) (ConfigMirror, bool, error)
	ListConfigMirrors(ctx context.Context, kind, scope, scopeID string) (map[string]ConfigMirror, error)
}

type ConfigReadStore interface {
	ConfigReader
	MirrorReader
}

type ConfigWriter interface {
	SaveConfig(ctx context.Context, c *ConfigRecord) error
	DeleteConfig(ctx context.Context, id string) error
	SetConfigValue(ctx context.Context, kind, scope, scopeID, name string, value ConfigValue) error
	DeleteConfigValue(ctx context.Context, kind, scope, scopeID, name string) error
	DeleteConfigPrefix(ctx context.Context, kind, scope, scopeID, namePrefix string) error
}

type ConfigRowWriter interface {
	SaveConfig(ctx context.Context, c *ConfigRecord) error
}

type ConfigEpochWriter interface {
	BumpConfigEpoch(ctx context.Context) (int64, error)
}

type ConfigTxStore interface {
	ConfigStore
	ConfigEpochWriter
}

type ConfigMirrorStore interface {
	SaveConfigMirror(ctx context.Context, kind, scope, scopeID, name string, m ConfigMirror) error
	GetConfigMirror(ctx context.Context, kind, scope, scopeID, name string) (ConfigMirror, bool, error)
	DeleteConfigMirror(ctx context.Context, kind, scope, scopeID, name string) error
}

type ConfigMirrorReconciler interface {
	ReconcileConfigMirrors(ctx context.Context, repair bool) (ConfigMirrorReconcile, error)
}

type ConfigStore interface {
	ConfigReader
	ConfigWriter
	ConfigMirrorStore
}

type KVStore interface {
	ListConfigValues(ctx context.Context, kind, scope, scopeID, namePrefix string) (map[string]ConfigValue, error)
	SetConfigValue(ctx context.Context, kind, scope, scopeID, name string, value ConfigValue) error
	DeleteConfigValue(ctx context.Context, kind, scope, scopeID, name string) error
	DeleteConfigPrefix(ctx context.Context, kind, scope, scopeID, namePrefix string) error
	ConfigMirrorStore
}

type AgentReader interface {
	GetAgent(ctx context.Context, agentID string) (*AgentRecord, error)
}

type AgentFileStore interface {
	GetAgentFile(ctx context.Context, agentID, userID, filename string) ([]byte, error)
	GetAgentFileExact(ctx context.Context, agentID, userID, filename string) ([]byte, error)
	SaveAgentFile(ctx context.Context, agentID, userID, filename string, data []byte) error
	SaveAgentFileIfVersion(ctx context.Context, agentID, userID, filename string, data []byte, expected AgentFileVersion) error
}

type AgentKnowledgeStore interface {
	ListAgentKnowledgeDocs(ctx context.Context, agentID, userID string) ([]KnowledgeDoc, error)
	SearchAgentKnowledgeChunks(ctx context.Context, agentID, userID, query string, limit int) ([]KnowledgeChunkRecord, error)
}

type AgentMCPServerStore interface {
	AddMCPServer(ctx context.Context, agentID, serverName string, cfg config.MCPServerConfig) error
	DeleteMCPServer(ctx context.Context, agentID, serverName string) error
	ListMCPServers(ctx context.Context, agentID string) (map[string]config.MCPServerConfig, error)
}

type CronStore interface {
	ListCronJobsByAgent(ctx context.Context, agentID string) ([]CronJobRecord, error)
	GetCronJob(ctx context.Context, jobID string) (*CronJobRecord, error)
	SaveCronJob(ctx context.Context, job *CronJobRecord) error
	DeleteCronJob(ctx context.Context, jobID string) error
}

type ChatterCounter interface {
	CountChatterUserMessages(ctx context.Context, agentID, chatterUserID string) (int, error)
}

type SessionEventReader interface {
	ListSessionEventsSince(ctx context.Context, userID, agentID, sessionKey string, sinceSeq int64) ([]SessionEventRecord, error)
}

type ConfigValueReader interface {
	GetConfigValue(ctx context.Context, kind, scope, scopeID, name string) (ConfigValue, error)
}

type AgentRuntimeStore interface {
	AgentReader
	AgentFileStore
	AgentKnowledgeStore
	AgentMCPServerStore
	CronStore
	ChatterCounter
	SessionEventReader
	ConfigReadStore
	ConfigValueReader
	ConfigWriter
	ConfigMirrorStore
}
