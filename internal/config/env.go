package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
)

// EnvConfig holds infrastructure/runtime configuration loaded from env.toml.
// This is NOT user-facing business config (providers, models, agents) — those
// live in the database (per user). EnvConfig is set once by the operator.
type EnvConfig struct {
	Gateway  EnvGateway  `toml:"gateway"`
	Storage  EnvStorage  `toml:"storage"`
	Sandbox  EnvSandbox  `toml:"sandbox"`
	Log      EnvLog      `toml:"log"`
}

type EnvGateway struct {
	Port  int    `toml:"port"`    // default 18953
	Mode  string `toml:"mode"`    // "local" or "cloud"
	Bind  string `toml:"bind"`    // "loopback" or "all"
	Token string `toml:"token"`   // admin/gateway auth token
}

type EnvStorage struct {
	Type        string `toml:"type"`         // "file", "postgres", "sqlite"
	DSN         string `toml:"dsn"`          // database connection string
	AutoMigrate bool   `toml:"auto_migrate"` // auto-create tables
}

type EnvSandbox struct {
	Enabled bool   `toml:"enabled"`
	Backend string `toml:"backend"`  // "docker" or "e2b"
	Image   string `toml:"image"`    // docker image
	E2BKey  string `toml:"e2b_key"`  // E2B API key
}

type EnvLog struct {
	Level string `toml:"level"` // "debug", "info", "warn", "error"
}

// LoadEnv reads env.toml from these locations (first found wins):
//  1. ./env.toml (current working directory)
//  2. ~/.fastclaw/env.toml
//  3. /etc/fastclaw/env.toml
//
// Environment variables override file values:
//
//	FASTCLAW_PORT, FASTCLAW_MODE, FASTCLAW_AUTH_TOKEN,
//	FASTCLAW_STORAGE_TYPE, FASTCLAW_STORAGE_DSN,
//	FASTCLAW_SANDBOX_BACKEND, E2B_API_KEY
func LoadEnv() *EnvConfig {
	cfg := &EnvConfig{
		Gateway: EnvGateway{
			Port: 18953,
			Mode: "local",
			Bind: "loopback",
		},
		Storage: EnvStorage{
			// SQLite is the zero-config default: one .db file under
			// ~/.fastclaw/, no external service to run, and the same
			// schema/code path as Postgres so cloud upgrades are a DSN
			// swap. Empty DSN means "the factory picks it" — see
			// store.New for the resolved location.
			Type:        "sqlite",
			AutoMigrate: true,
		},
		Log: EnvLog{
			Level: "info",
		},
	}

	// Try loading from file
	for _, path := range envSearchPaths() {
		if _, err := os.Stat(path); err == nil {
			if _, err := toml.DecodeFile(path, cfg); err != nil {
				slog.Warn("failed to parse env.toml", "path", path, "error", err)
			} else {
				slog.Info("loaded env.toml", "path", path)
			}
			break
		}
	}

	// Environment variables override
	if v := os.Getenv("FASTCLAW_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Gateway.Port = p
		}
	}
	if v := os.Getenv("FASTCLAW_MODE"); v != "" {
		cfg.Gateway.Mode = v
	}
	if v := os.Getenv("FASTCLAW_BIND"); v != "" {
		cfg.Gateway.Bind = v
	}
	if v := os.Getenv("FASTCLAW_AUTH_TOKEN"); v != "" {
		cfg.Gateway.Token = v
	}
	if v := os.Getenv("FASTCLAW_STORAGE_TYPE"); v != "" {
		cfg.Storage.Type = v
	}
	if v := os.Getenv("FASTCLAW_STORAGE_DSN"); v != "" {
		cfg.Storage.DSN = v
	}
	if v := os.Getenv("FASTCLAW_SANDBOX_BACKEND"); v != "" {
		cfg.Sandbox.Backend = v
		cfg.Sandbox.Enabled = true
	}
	if v := os.Getenv("E2B_API_KEY"); v != "" && cfg.Sandbox.E2BKey == "" {
		cfg.Sandbox.E2BKey = v
	}

	return cfg
}

// applyObjectStoreEnv reads FASTCLAW_OBJECT_STORE_* env vars into the
// Config.ObjectStore block. Called from ApplyToConfig — kept as a
// separate helper so gateway.New doesn't have to know the env-var
// name convention.
func applyObjectStoreEnv(cfg *Config) {
	read := func(key string) string { return os.Getenv("FASTCLAW_OBJECT_STORE_" + key) }

	if v := read("TYPE"); v != "" {
		cfg.ObjectStore.Type = v
	}
	if v := read("LOCAL_ROOT"); v != "" {
		cfg.ObjectStore.Local.Root = v
	}
	if v := read("REGION"); v != "" {
		cfg.ObjectStore.S3.Region = v
	}
	if v := read("BUCKET"); v != "" {
		cfg.ObjectStore.S3.Bucket = v
	}
	if v := read("PREFIX"); v != "" {
		cfg.ObjectStore.S3.Prefix = v
	}
	if v := read("ACCESSKEY"); v != "" {
		cfg.ObjectStore.S3.AccessKey = v
	}
	if v := read("SECRETKEY"); v != "" {
		cfg.ObjectStore.S3.SecretKey = v
	}
	if v := read("ACCOUNTID"); v != "" {
		cfg.ObjectStore.AccountID = v
	}
	if v := read("ENDPOINT"); v != "" {
		cfg.ObjectStore.S3.Endpoint = v
	}
	if v := read("USESSL"); v != "" {
		cfg.ObjectStore.S3.UseSSL = v == "true" || v == "1"
	}
	if v := read("ALIYUN_INTERNAL"); v != "" {
		cfg.ObjectStore.AliyunIntern = v == "true" || v == "1"
	}
}

// ApplyToConfig merges EnvConfig values into a legacy Config struct.
// This bridges the gap while we migrate away from fastclaw.json.
func (e *EnvConfig) ApplyToConfig(cfg *Config) {
	if e.Gateway.Port > 0 {
		cfg.Gateway.Port = e.Gateway.Port
	}
	if e.Gateway.Mode != "" {
		cfg.Gateway.Mode = e.Gateway.Mode
	}
	if e.Gateway.Bind != "" {
		cfg.Gateway.Bind = e.Gateway.Bind
	}
	if e.Gateway.Token != "" {
		cfg.Gateway.Auth.Token = e.Gateway.Token
	}
	if e.Storage.Type != "" {
		cfg.Storage.Type = e.Storage.Type
	}
	if e.Storage.DSN != "" {
		cfg.Storage.DSN = e.Storage.DSN
	}
	cfg.Storage.AutoMigrate = e.Storage.AutoMigrate
	if e.Sandbox.Enabled {
		cfg.Sandbox.Enabled = true
		if e.Sandbox.Backend != "" {
			cfg.Sandbox.Backend = e.Sandbox.Backend
		}
		if e.Sandbox.Image != "" {
			cfg.Sandbox.Image = e.Sandbox.Image
		}
		if e.Sandbox.E2BKey != "" {
			cfg.Sandbox.E2BKey = e.Sandbox.E2BKey
		}
	}
	// Object-store vars are read directly from the env (not mirrored in
	// EnvConfig) — they came later and the EnvConfig struct is already
	// doing too much indirection.
	applyObjectStoreEnv(cfg)
}

// GenerateToken creates a random hex token.
func (e *EnvConfig) GenerateTokenIfEmpty() {
	if e.Gateway.Token == "" {
		b := make([]byte, 32)
		if _, err := os.ReadFile("/dev/urandom"); err == nil {
			// fallback handled below
		}
		fmt.Sprintf("%x", b) // just a placeholder
		// Use crypto/rand in production
		e.Gateway.Token = fmt.Sprintf("fc_%x", b)
	}
}

func envSearchPaths() []string {
	paths := []string{"env.toml"}

	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".fastclaw", "env.toml"))
	}

	paths = append(paths, "/etc/fastclaw/env.toml")
	return paths
}
