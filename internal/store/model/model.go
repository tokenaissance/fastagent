// Package model holds the row types and value objects that cross the store
// ports. It imports no database driver and no adapter package.
package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	ValueKindString = "string"
	ValueKindNumber = "number"
	ValueKindBool   = "bool"
	ValueKindNull   = "null"
	ValueKindObject = "object"
	ValueKindArray  = "array"
)

type ConfigValue struct {
	Value string
	Kind  string
}

func (v ConfigValue) Decode() interface{} {
	switch v.Kind {
	case ValueKindString:
		return v.Value
	case ValueKindNumber:
		return json.Number(v.Value)
	case ValueKindBool:
		return v.Value == "true"
	case ValueKindNull:
		return nil
	case ValueKindObject, ValueKindArray:
		dec := json.NewDecoder(strings.NewReader(v.Value))
		dec.UseNumber()
		var out interface{}
		if err := dec.Decode(&out); err != nil {
			return v.Value
		}
		return out
	default:
		return decodeLegacyValue(v.Value)
	}
}

func (v ConfigValue) DecodeLegacyStructure() interface{} {
	if len(v.Value) > 0 && (v.Value[0] == '{' || v.Value[0] == '[') {
		return decodeLegacyValue(v.Value)
	}
	return v.Value
}

func decodeLegacyValue(s string) interface{} {
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	if len(s) > 0 && (s[0] == '[' || s[0] == '{') {
		var v interface{}
		if json.Unmarshal([]byte(s), &v) == nil {
			return v
		}
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%g", &f); err == nil {
		check := fmt.Sprintf("%g", f)
		if check == s {
			return f
		}
	}
	return s
}

type ConfigRecord struct {
	ID            string                 `json:"id"`
	Kind          string                 `json:"kind"`
	Scope         string                 `json:"scope,omitempty"`
	UserID        string                 `json:"userId,omitempty"`
	AgentID       string                 `json:"agentId,omitempty"`
	Name          string                 `json:"name"`
	Enabled       bool                   `json:"enabled"`
	CredentialKey string                 `json:"credentialKey,omitempty"`
	Data          map[string]interface{} `json:"data,omitempty"`
	CreatedAt     time.Time              `json:"createdAt"`
	UpdatedAt     time.Time              `json:"updatedAt"`
}

func (r ConfigRecord) LegacyScope() string {
	if r.Scope != "" {
		return r.Scope
	}
	switch {
	case r.UserID != "" && r.AgentID != "":
		return "user-agent"
	case r.UserID != "":
		return "user"
	case r.AgentID != "":
		return "agent"
	default:
		return "system"
	}
}

func (r ConfigRecord) LegacyScopeID() string {
	switch {
	case r.UserID != "" && r.AgentID != "":
		return r.UserID + "/" + r.AgentID
	case r.UserID != "":
		return r.UserID
	case r.AgentID != "":
		return r.AgentID
	default:
		return ""
	}
}

type AgentRecord struct {
	ID        string                 `json:"id"`
	UserID    string                 `json:"userId"`
	Name      string                 `json:"name"`
	Config    map[string]interface{} `json:"config,omitempty"`
	IsPublic  bool                   `json:"isPublic"`
	CreatedAt time.Time              `json:"createdAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

type KnowledgeDoc struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type KnowledgeChunkRecord struct {
	AgentID    string    `json:"agentId"`
	UserID     string    `json:"userId"`
	Path       string    `json:"path"`
	Hash       string    `json:"hash"`
	ChunkIndex int       `json:"chunkIndex"`
	Content    string    `json:"content"`
	Score      int       `json:"score,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type CronJobRecord struct {
	ID            string     `json:"id"`
	UserID        string     `json:"userId,omitempty"`
	CreatorUserID string     `json:"creatorUserId,omitempty"`
	AgentID       string     `json:"agentId"`
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	Schedule      string     `json:"schedule"`
	Message       string     `json:"message"`
	Channel       string     `json:"channel"`
	ChatID        string     `json:"chatId"`
	AccountID     string     `json:"accountId"`
	Timezone      string     `json:"timezone"`
	Enabled       bool       `json:"enabled"`
	LastRun       *time.Time `json:"lastRun,omitempty"`
	NextRun       *time.Time `json:"nextRun,omitempty"`
	FailureCount  int        `json:"failureCount,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

func (j CronJobRecord) Creator() string {
	if j.CreatorUserID != "" {
		return j.CreatorUserID
	}
	return j.UserID
}

type SessionEventRecord struct {
	UserID     string    `json:"userId,omitempty"`
	AgentID    string    `json:"agentId,omitempty"`
	SessionKey string    `json:"sessionKey,omitempty"`
	Seq        int64     `json:"seq"`
	Type       string    `json:"type"`
	Data       []byte    `json:"data,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

type ConfigMirror struct {
	Prefix      string
	KeyCount    int
	Fingerprint string
	Enabled     *bool
}

type ConfigMirrorGap struct {
	Kind     string
	Scope    string
	ScopeID  string
	Name     string
	WantKeys int
	GotKeys  int
	Missing  []string
	Extra    []string
	Changed  []string
}

type ConfigMirrorReconcile struct {
	Examined  int
	Certified int
	Untyped   int
	Retagged  int
	Repaired  int
	Rewritten int
	Gaps      []ConfigMirrorGap
}

type AgentFileVersion struct {
	Absent  bool
	Content []byte
}
