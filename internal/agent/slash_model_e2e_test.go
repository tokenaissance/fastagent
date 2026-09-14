package agent

// `/model` must be a config change, not a per-process tweak.
//
// What the tests below pin, and why each one matters:
//
//  1. The row written is the SAME row the dashboard's model picker writes
//     (scope=agent, name=agents.defaults). If chat wrote a different row, the
//     two surfaces would disagree and the next config reload would silently
//     revert the switch.
//  2. Sibling keys in that row survive. It also carries promptMode /
//     splitReplies / autoPersist, so a blind overwrite would quietly reset
//     them.
//  3. The runtime is notified through the same in-session hook `mcp add/remove`
//     uses, which is what makes every replica drop its cached UserSpace
//     instead of firing the old model until idle eviction.
//  4. A store failure is reported and the in-memory model is NOT changed —
//     claiming success while the write failed is how "the switch did not work"
//     reports get born.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func newSlashModelAgent(t *testing.T, notify func(userID, agentID string)) (*Agent, store.Store) {
	t.Helper()
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return &Agent{
		name:            "agt_model",
		agentID:         "agt_model",
		ownerUserID:     "usr_owner",
		model:           "openai/gpt-5.5",
		dataStore:       db,
		mcpConfigNotify: notify,
	}, db
}

func agentDefaultsRow(t *testing.T, db store.Store, agentID string) map[string]interface{} {
	t.Helper()
	row, err := scope.SettingAt(context.Background(), db, agentDefaultsNamespace, "", agentID)
	if err != nil {
		t.Fatalf("read agents.defaults: %v", err)
	}
	return row
}

// modelCommand builds the message a web chatter sends. /model is owner/admin
// gated (slashRequiresAdmin), so the identity is part of the fixture: the switch
// path is only reachable as the agent's owner.
func modelCommand(text string) bus.InboundMessage {
	return bus.InboundMessage{Channel: "web", UserID: "usr_owner", Text: text}
}

func TestSlashModelPersistsTheAgentDefault(t *testing.T) {
	var notified [][2]string
	ag, db := newSlashModelAgent(t, func(userID, agentID string) {
		notified = append(notified, [2]string{userID, agentID})
	})

	got := ag.handleSlashCommand(modelCommand("/model deepseek/deepseek-v4-flash"))
	if !got.handled {
		t.Fatal("/model was not handled")
	}
	if !strings.Contains(got.reply, "deepseek/deepseek-v4-flash") {
		t.Fatalf("reply does not name the new model: %q", got.reply)
	}
	if ag.model != "deepseek/deepseek-v4-flash" {
		t.Fatalf("in-memory model = %q, want the new one", ag.model)
	}
	if row := agentDefaultsRow(t, db, "agt_model"); row["model"] != "deepseek/deepseek-v4-flash" {
		t.Fatalf("agents.defaults.model = %v, want the new model written to the dashboard's row", row["model"])
	}
	if len(notified) != 1 || notified[0] != [2]string{"usr_owner", "agt_model"} {
		t.Fatalf("notify calls = %v, want one for (usr_owner, agt_model)", notified)
	}
}

func TestSlashModelKeepsSiblingDefaultsKeys(t *testing.T) {
	ag, db := newSlashModelAgent(t, nil)
	if err := scope.SaveSettingByScope(context.Background(), db, scope.Agent, "agt_model",
		agentDefaultsNamespace, map[string]interface{}{
			"model":      "openai/gpt-5.5",
			"promptMode": "chatbot",
		}); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}

	ag.handleSlashCommand(modelCommand("/model anthropic/claude-sonnet-4-7"))

	row := agentDefaultsRow(t, db, "agt_model")
	if row["model"] != "anthropic/claude-sonnet-4-7" {
		t.Fatalf("model = %v, want the switch", row["model"])
	}
	if row["promptMode"] != "chatbot" {
		t.Fatalf("promptMode = %v, want it preserved (read-modify-write)", row["promptMode"])
	}
}

// A store that refuses writes: no switch, no notify, and a reply that says so.
func TestSlashModelReportsAFailedWriteAndStaysPut(t *testing.T) {
	notified := 0
	ag, db := newSlashModelAgent(t, func(string, string) { notified++ })
	ag.dataStore = &failingConfigStore{Store: db}

	got := ag.handleSlashCommand(modelCommand("/model deepseek/deepseek-v4-flash"))

	if ag.model != "openai/gpt-5.5" {
		t.Fatalf("in-memory model = %q, want it unchanged after a failed write", ag.model)
	}
	if notified != 0 {
		t.Fatalf("notify calls = %d, want 0 after a failed write", notified)
	}
	if !strings.Contains(got.reply, "saving the agent default failed") {
		t.Fatalf("reply does not report the failure: %q", got.reply)
	}
}

// No store wired (local runs): the switch still applies in memory, and the reply
// says it was not persisted instead of implying it was.
func TestSlashModelSaysWhenItCannotPersist(t *testing.T) {
	ag, _ := newSlashModelAgent(t, nil)
	ag.dataStore = nil

	got := ag.handleSlashCommand(modelCommand("/model deepseek/deepseek-v4-flash"))

	if ag.model != "deepseek/deepseek-v4-flash" {
		t.Fatalf("in-memory model = %q, want the switch applied", ag.model)
	}
	if !strings.Contains(got.reply, "Not persisted") {
		t.Fatalf("reply claims a persisted switch it could not make: %q", got.reply)
	}
}

// failingConfigStore rejects every write while serving reads.
type failingConfigStore struct {
	store.Store
}

func (f *failingConfigStore) SaveConfig(context.Context, *store.ConfigRecord) error {
	return errors.New("config store is read-only")
}
