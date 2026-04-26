package store

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// New creates a Store based on the storage config.
//
// Defaults to SQLite at ~/.fastclaw/fastclaw.db when called with a
// nil/empty config — that's the zero-config dev path. "file" is still
// supported for back-compat with old single-user installations but is
// no longer the default.
func New(cfg *StorageConfig, homeDir string) (Store, error) {
	if cfg == nil || cfg.Type == "" {
		cfg = &StorageConfig{Type: StorageSQLite, AutoMigrate: true}
	}
	if cfg.Type == StorageFile {
		slog.Info("using file-based storage", "dir", homeDir)
		return NewFileStore(homeDir), nil
	}

	switch cfg.Type {
	case StoragePostgres, StorageSQLite:
		dsn := cfg.DSN
		if cfg.Type == StorageSQLite && dsn == "" {
			// Default location lives next to the rest of FastClaw state
			// so a single rm -rf ~/.fastclaw blows away everything for
			// dev resets. WAL journal lets reads happen during writes —
			// matters for the gateway + sandbox sharing the DB.
			if err := os.MkdirAll(homeDir, 0o755); err != nil {
				return nil, fmt.Errorf("create %s: %w", homeDir, err)
			}
			dsn = "file:" + filepath.Join(homeDir, "fastclaw.db") + "?_journal=WAL&_fk=1"
		}
		slog.Info("using database storage", "dialect", cfg.Type, "dsn", maskDSN(dsn))
		db, err := NewDBStore(string(cfg.Type), dsn)
		if err != nil {
			return nil, fmt.Errorf("open database: %w", err)
		}
		if cfg.AutoMigrate {
			slog.Info("running database migrations")
			if err := db.Migrate(context.Background()); err != nil {
				db.Close()
				return nil, fmt.Errorf("migrate: %w", err)
			}
		}
		return db, nil

	default:
		return nil, fmt.Errorf("unsupported storage type: %s", cfg.Type)
	}
}

// maskDSN masks passwords in DSN strings for logging.
func maskDSN(dsn string) string {
	if len(dsn) > 20 {
		return dsn[:10] + "***" + dsn[len(dsn)-5:]
	}
	return "***"
}
