package gateway

// The delivery-point half of the `skillsLearner` row.
//
// internal/agent/skills_learner_wiring_test.go proves the rule: with the option
// set, the learner exists and a learned SKILL.md lands through the `skills/`
// namespace's single writer (host disk plus object-store mirror).
//
// This one proves the link above it: that the row an operator writes —
// namespace "skillsLearner", the only surface that exists for it (no dashboard
// renders these fields) — actually becomes that option, so a real turn's
// post-turn pass publishes a learned skill. Without it the row is what it was
// before this change: writable, readable back, and read by nobody, because the
// learner was only ever constructed in agent.NewAgentWithFullCfg, a constructor
// no production path calls.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// skillWriteStore records what the skills namespace's writer publishes
// (SyncSkillUp → PutIfVersion) and answers every read with "nothing there" —
// the empty object store a fresh deployment has.
type skillWriteStore struct {
	workspace.Store
	mu      sync.Mutex
	objects map[string][]byte
}

func newSkillWriteStore() *skillWriteStore {
	return &skillWriteStore{objects: make(map[string][]byte)}
}

func (s *skillWriteStore) List(context.Context, string, string, string) ([]workspace.ObjectInfo, error) {
	return nil, nil
}

func (s *skillWriteStore) Stat(context.Context, string, string, string, string) (*workspace.ObjectInfo, error) {
	return nil, workspace.ErrNotFound
}

func (s *skillWriteStore) Get(context.Context, string, string, string, string) (io.ReadCloser, error) {
	return nil, workspace.ErrNotFound
}

func (s *skillWriteStore) PutIfVersion(_ context.Context, owner, _, _, path string, r io.Reader, _ int64, _ string, _ workspace.Version) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[owner+"|"+path] = data
	return nil
}

func (s *skillWriteStore) content(owner, path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.objects[owner+"|"+path])
}

func (s *skillWriteStore) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.objects))
	for k := range s.objects {
		out = append(out, k)
	}
	return out
}

// skillTurnProvider drives one ordinary turn (one tool call, then an answer) and
// recognises the learner's extraction call by its prompt — which is how the
// same provider serves both without the test having to count calls.
type skillTurnProvider struct {
	mu       sync.Mutex
	toolRuns int
}

func (p *skillTurnProvider) respond(msgs []provider.Message, tools []provider.Tool) *provider.Response {
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(m.Content, "Output ONLY the JSON") {
			return &provider.Response{Content: learnerExtractionJSON}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(tools) > 0 && p.toolRuns == 0 {
		p.toolRuns++
		return &provider.Response{ToolCalls: []provider.ToolCall{{
			ID: "call_1", Type: "function",
			Function: provider.FunctionCall{Name: "learner_probe", Arguments: `{}`},
		}}}
	}
	return &provider.Response{Content: "done"}
}

func (p *skillTurnProvider) Chat(_ context.Context, msgs []provider.Message, tools []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	return p.respond(msgs, tools), nil
}

func (p *skillTurnProvider) ChatStream(_ context.Context, msgs []provider.Message, tools []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	resp := p.respond(msgs, tools)
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: resp.Content, ToolCalls: resp.ToolCalls, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

const learnerExtractionJSON = `{"extract":true,"skill":{"name":"demo","slug":"demo","description":"A demo skill.","content":"---\nname: demo\ndescription: A demo skill.\n---\n\nStep 1: do the thing.\n"}}`

func TestTheSkillsLearnerRowReachesTheSingleWriter(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	t.Setenv("FASTAGENT_HOME", base)

	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// What an operator writes to turn the learner on. minToolCalls=1 keeps the
	// turn below to a single tool call; the default cadence (3) is not what this
	// test is about.
	if err := scope.SaveSetting(ctx, db, "u_owner", "", "skillsLearner", map[string]interface{}{
		"enabled": true, "model": "distiller", "minToolCalls": 1,
	}); err != nil {
		t.Fatalf("save settings row: %v", err)
	}

	cfg, err := assembleConfig(ctx, db, "u_owner", "")
	if err != nil {
		t.Fatalf("assemble config: %v", err)
	}
	if !cfg.SkillsLearner.Enabled {
		t.Fatalf("assembleConfig did not read skillsLearner.enabled back into cfg.SkillsLearner")
	}

	home := filepath.Join(base, "agents", "agt_learner", "agent")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	rc := config.ResolvedAgent{
		ID: "agt_learner", UserID: "u_owner", Home: home,
		Workspace: filepath.Join(home, "workspace"), Model: "fake-model",
		MaxTokens: 128, Temperature: 0.7, MaxToolIterations: 2,
	}

	ws := newSkillWriteStore()
	prov := &skillTurnProvider{}
	opts := append(managerOptions(cfg, "u_owner", db, ws, nil, nil, nil), agent.WithWorkspaceStore(ws))
	mgr, err := agent.NewManager([]config.ResolvedAgent{rc}, prov, bus.New(), opts...)
	if err != nil {
		t.Fatalf("agent manager: %v", err)
	}
	ag := mgr.AgentByID(rc.ID)
	if ag == nil {
		t.Fatal("agent not built")
	}
	ag.ToolRegistry().Register("learner_probe", "counting probe", nil, func(context.Context, json.RawMessage) (string, error) {
		return "probe ok", nil
	})

	ag.HandleMessage(ctx, bus.InboundMessage{
		Channel: "web", ChatID: "c1", UserID: "u_owner",
		Text: "please publish the weekly report",
	})

	// The learner runs from the turn's post-turn pass (a goroutine), so wait for
	// the object the single writer publishes rather than assuming it is done.
	wantOwner, wantPath := skills.UserSkillOwner("u_owner"), "skills/demo/SKILL.md"
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && ws.content(wantOwner, wantPath) == "" {
		time.Sleep(20 * time.Millisecond)
	}
	if got := ws.content(wantOwner, wantPath); !strings.Contains(got, "Step 1") {
		t.Fatalf("the skillsLearner row never published a learned skill to the object store (objects=%v): the row must reach the learner, and the learner must write through the namespace's single writer",
			ws.keys())
	}
}
