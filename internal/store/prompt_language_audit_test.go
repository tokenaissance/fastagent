package store

// scripts/prompt-language-audit.sql is the instrument that decides whether the
// corpus's English per-turn blocks actually drag a Chinese-default agent into
// answering in English (docs/prompt-inventory.md, finding 3). An instrument that
// nobody runs rots, so this test executes the real file against fixtures with
// KNOWN answers and asserts the numbers it reports.
//
// PostgreSQL only (jsonb_array_elements), skipped without FASTAGENT_TEST_PG_DSN —
// the CI job that runs the store suite sets it, so the query is exercised there.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptLanguageAuditQueryOnFixtures(t *testing.T) {
	dsn := os.Getenv("FASTAGENT_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set FASTAGENT_TEST_PG_DSN to run the prompt-language audit query")
	}
	db, err := sql.Open(driverName("postgres"), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// A TEMP table shadows the real one for this connection only: the audit query
	// stays unqualified (that is how an operator runs it) and the test leaves no
	// residue.
	if _, err := db.ExecContext(ctx, `CREATE TEMP TABLE sessions (
		user_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		session_key TEXT NOT NULL,
		channel TEXT NOT NULL DEFAULT '',
		messages TEXT NOT NULL DEFAULT '[]',
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (user_id, agent_id, session_key)
	)`); err != nil {
		t.Fatalf("temp table: %v", err)
	}

	// Three sessions with answers we already know:
	//   zh→en : Chinese question, English reply      → must be counted
	//   zh→zh : Chinese question, Chinese reply      → not counted
	//   en→en : English question, English reply      → not a Chinese question
	session := func(channel string, msgs ...string) {
		t.Helper()
		payload := "[" + strings.Join(msgs, ",") + "]"
		if _, err := db.ExecContext(ctx,
			`INSERT INTO sessions (user_id, agent_id, session_key, channel, messages)
			 VALUES ('audit-user', 'audit-agent', $1, $2, $3)`, channel, channel, payload); err != nil {
			t.Fatalf("insert %s: %v", channel, err)
		}
	}
	msg := func(role, content string) string {
		body, _ := json.Marshal(map[string]string{"role": role, "content": content})
		return string(body)
	}
	session("audit-zh-en",
		msg("user", "帮我把这个季度的销售数据整理成一张表"),
		msg("assistant", "Here is the table you asked for."))
	session("audit-zh-zh",
		msg("user", "帮我把这个季度的销售数据整理成一张表"),
		msg("assistant", "整理好了，下面是这张表。"))
	session("audit-en-en",
		msg("user", "Please summarise the quarterly numbers"),
		msg("assistant", "Here is the summary."))

	query, err := os.ReadFile(filepath.Join("..", "..", "scripts", "prompt-language-audit.sql"))
	if err != nil {
		t.Fatalf("read the audit query: %v", err)
	}
	rows, err := db.QueryContext(ctx, string(query))
	if err != nil {
		t.Fatalf("the audit query does not run: %v", err)
	}
	defer rows.Close()

	got := map[string]float64{}
	var totals float64
	for rows.Next() {
		var channel string
		var replies, chineseQuestions, answeredInEnglish int
		var pct sql.NullFloat64
		if err := rows.Scan(&channel, &replies, &chineseQuestions, &answeredInEnglish, &pct); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if channel == "(all channels)" {
			totals = pct.Float64
			continue
		}
		got[channel] = pct.Float64
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if got["audit-zh-en"] != 100 {
		t.Errorf("zh→en channel = %v, want 100 — the query must count a Chinese question answered in English", got["audit-zh-en"])
	}
	if got["audit-zh-zh"] != 0 {
		t.Errorf("zh→zh channel = %v, want 0", got["audit-zh-zh"])
	}
	if got["audit-en-en"] != 0 {
		t.Errorf("en→en channel = %v, want 0 (no Chinese question here)", got["audit-en-en"])
	}
	// One English answer out of two Chinese questions.
	if totals < 49 || totals > 51 {
		t.Errorf("all-channels percentage = %v, want ~50", totals)
	}
}
