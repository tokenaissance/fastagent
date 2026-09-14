package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/fastclaw-ai/fastclaw/internal/doctor"
	"github.com/fastclaw-ai/fastclaw/internal/session"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// doctorCmd groups read-mostly diagnostics of stored state. It exists because
// the 2026-09-13 incident (docs/session-turn-integrity.md) was found by hand:
// scanning sessions.messages for tool-call pairing violations, then repairing
// one row with SQL. This makes that scan a command.
func doctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose stored state (session history, tool-call pairing)",
	}
	cmd.AddCommand(doctorSessionsCmd())
	return cmd
}

type doctorSessionReport struct {
	UserID     string           `json:"userId"`
	AgentID    string           `json:"agentId"`
	SessionKey string           `json:"sessionKey"`
	Messages   int              `json:"messages"`
	Findings   []doctor.Finding `json:"findings"`
	Fixed      int              `json:"fixed,omitempty"`
	BackupPath string           `json:"backupPath,omitempty"`
}

func doctorSessionsCmd() *cobra.Command {
	var agentID, sessionKey, dbPath, backupDir string
	var limit int
	var asJSON, fix bool

	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Scan session history for broken tool-call pairing",
		Long: `Scan every session's working set for the three shapes that break
providers or silently drop context:

  duplicate_tool_reply   one tool_call_id answered twice (pad + real result,
                         or synthetic ids colliding across turns)
  orphan_tool_reply      a reply whose call no assistant message declared
  unanswered_tool_call   a declared call no reply answers

The same shapes are repaired at request time by the prompt projection
(internal/agent/normalize.go) and the wire builder, so findings are not
necessarily user-visible — they are the history debt behind those repairs.

With --fix, duplicate replies (everything after the first reply for a
tool_call_id) are removed from the stored working set; the row is backed up to
<backup-dir>/<sessionKey>.json first. Orphan and unanswered findings are
reported only: the projection handles them without rewriting history.

Exits 1 when findings remain (so a check can gate on it), 0 when clean or
after --fix.

Storage: --db <path> reads a local sqlite file (default ~/.fastagent/fastagent.db);
without --db the store comes from the environment, which is how you point it at
the production Postgres (FASTAGENT_STORAGE_DSN).

Examples:
  fastagent doctor sessions
  fastagent doctor sessions --agent agt_cda27bbfbf4a84e2dfa6 --json
  fastagent doctor sessions --session-key hJKMWwtOp3mJOtqN8Uz2mW --fix`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			db, closeFn, err := openDoctorStore(dbPath)
			if err != nil {
				return err
			}
			defer closeFn()

			snaps, err := db.ListSessionSnapshots(ctx, agentID, limit)
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}

			var reports []doctorSessionReport
			var totalFixed int
			for _, snap := range snaps {
				if sessionKey != "" && snap.SessionKey != sessionKey {
					continue
				}
				msgs := session.ProviderMessages(snap.Messages)
				findings := doctor.Scan(msgs)
				if len(findings) == 0 {
					continue
				}
				report := doctorSessionReport{
					UserID: snap.UserID, AgentID: snap.AgentID, SessionKey: snap.SessionKey,
					Messages: len(snap.Messages), Findings: findings,
				}

				if fix {
					if err := repairDuplicateReplies(ctx, db, snap, findings, backupDir, &report); err != nil {
						return err
					}
					totalFixed += report.Fixed
				}
				reports = append(reports, report)
			}

			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(map[string]any{
					"scanned":     len(snaps),
					"affected":    len(reports),
					"fixed":       totalFixed,
					"generatedAt": time.Now().UTC().Format(time.RFC3339),
					"sessions":    reports,
				}); err != nil {
					return err
				}
			} else {
				printDoctorReport(len(snaps), reports, totalFixed, fix)
			}

			if len(reports) > 0 && !fix {
				return fmt.Errorf("%d session(s) carry tool-call pairing findings", len(reports))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&agentID, "agent", "", "limit the scan to one agent id")
	cmd.Flags().StringVar(&sessionKey, "session-key", "", "limit the scan to one session key")
	cmd.Flags().IntVar(&limit, "limit", 0, "scan at most N sessions (newest first; 0 = all)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable JSON")
	cmd.Flags().BoolVar(&fix, "fix", false, "remove duplicate replies (backs the row up first)")
	cmd.Flags().StringVar(&backupDir, "backup-dir", "", "where --fix writes row backups (default ./fastagent-doctor-backup-<ts>)")
	cmd.Flags().StringVar(&dbPath, "db", "", "sqlite DB path (default ~/.fastagent/fastagent.db, or the env store)")
	return cmd
}

// openDoctorStore prefers an explicit sqlite path, then the environment
// (Postgres in production), then the local default — the same precedence an
// operator expects from the other subcommands.
func openDoctorStore(dbPath string) (*store.DBStore, func(), error) {
	if dbPath == "" {
		if envStore, err := openStoreFromEnv(); err == nil {
			if db, ok := envStore.(*store.DBStore); ok {
				return db, func() { _ = envStore.Close() }, nil
			}
		}
	}
	return openStoreAt(dbPath)
}

// repairDuplicateReplies drops every reply after the first for its
// tool_call_id, backing up the row before the write so the repair is
// reversible. Returns the backup path via the report.
func repairDuplicateReplies(ctx context.Context, db *store.DBStore, snap store.SessionSnapshot, findings []doctor.Finding, backupDir string, report *doctorSessionReport) error {
	drop := map[int]bool{}
	for _, f := range findings {
		if f.Kind == doctor.DuplicateReply {
			drop[f.Index] = true
		}
	}
	if len(drop) == 0 {
		return nil
	}

	if backupDir == "" {
		backupDir = "fastagent-doctor-backup-" + time.Now().UTC().Format("20060102T150405")
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	backupPath := filepath.Join(backupDir, snap.SessionKey+".json")
	payload, err := json.MarshalIndent(map[string]any{
		"userId": snap.UserID, "agentId": snap.AgentID, "sessionKey": snap.SessionKey,
		"backedUpAt": time.Now().UTC().Format(time.RFC3339),
		"messages":   snap.Messages,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal backup: %w", err)
	}
	if err := os.WriteFile(backupPath, payload, 0o644); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}

	kept := make([]store.SessionMessage, 0, len(snap.Messages))
	for i, m := range snap.Messages {
		if !drop[i] {
			kept = append(kept, m)
		}
	}
	if err := db.SaveSession(ctx, snap.UserID, snap.AgentID, snap.SessionKey, &store.SessionRecord{
		Channel:   snap.Channel,
		AccountID: snap.AccountID,
		ChatID:    snap.ChatID,
		ProjectID: snap.ProjectID,
		Messages:  kept,
	}); err != nil {
		return fmt.Errorf("save repaired session: %w", err)
	}

	report.Fixed = len(drop)
	report.BackupPath = backupPath
	return nil
}

func printDoctorReport(scanned int, reports []doctorSessionReport, fixed int, didFix bool) {
	if len(reports) == 0 {
		fmt.Printf("doctor sessions: %d session(s) scanned, no tool-call pairing findings\n", scanned)
		return
	}
	for _, r := range reports {
		fmt.Printf("%s  (agent %s, %d messages)\n", r.SessionKey, r.AgentID, r.Messages)
		counts := map[doctor.Kind]int{}
		for _, f := range r.Findings {
			counts[f.Kind]++
		}
		for _, kind := range []doctor.Kind{doctor.DuplicateReply, doctor.OrphanReply, doctor.UnansweredCall} {
			if counts[kind] > 0 {
				fmt.Printf("  %-22s %d\n", kind, counts[kind])
			}
		}
		for _, f := range r.Findings {
			fmt.Printf("    [%d] %s %s\n", f.Index, f.Kind, f.ToolCallID)
		}
		if r.Fixed > 0 {
			fmt.Printf("  fixed %d duplicate repl(y|ies); backup: %s\n", r.Fixed, r.BackupPath)
		}
	}
	if didFix {
		fmt.Printf("doctor sessions: %d session(s) scanned, %d affected, %d duplicate repl(y|ies) removed\n",
			scanned, len(reports), fixed)
		return
	}
	fmt.Printf("doctor sessions: %d session(s) scanned, %d affected (re-run with --fix to remove duplicate replies)\n",
		scanned, len(reports))
}
