package agent

// The `skillsLearner` row on the production path.
//
// Before this change the row had the shape privacy.piiScrubbing.enabled had
// before change-register row 49: writable through the settings API / CLI,
// readable back, and read by nobody — its only reader was
// NewAgentWithFullCfg, a constructor no production path calls, so MaybeExtract
// was unreachable. Two halves of the wiring are not obvious and are what these
// tests pin:
//
//  1. the learner writes through the `skills/` namespace's SINGLE writer. It
//     used to call os.WriteFile on <workspace>/skills/... itself, which is a
//     second writer for a namespace that has exactly one (Registry.
//     writeSkillToHost: host disk plus the object-store mirror). A learned
//     SKILL.md that lives on one pod's disk is invisible to siblings and gone
//     on restart — the same hole the file tools were fixed for in row 50.
//  2. the extraction call is a provider call site, so it sits inside the
//     piiScrubbing rule (row 49): the learner holds the agent's provider, and
//     Agent.setProvider re-stamps it, so it cannot be handed a raw one.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

const learnerProbeEmail = "casey.rivera@example.com"

// learnerSkillStore is the workspace.Store half of the witness: it records what
// the namespace's writer publishes (SyncSkillUp → PutIfVersion) and answers
// every read with "nothing there", which is the empty cloud store.
type learnerSkillStore struct {
	workspace.Store
	mu      sync.Mutex
	objects map[string][]byte
}

func newLearnerSkillStore() *learnerSkillStore {
	return &learnerSkillStore{objects: make(map[string][]byte)}
}

func (s *learnerSkillStore) List(context.Context, string, string, string) ([]workspace.ObjectInfo, error) {
	return nil, nil
}

func (s *learnerSkillStore) Stat(context.Context, string, string, string, string) (*workspace.ObjectInfo, error) {
	return nil, os.ErrNotExist
}

func (s *learnerSkillStore) Get(context.Context, string, string, string, string) (io.ReadCloser, error) {
	return nil, os.ErrNotExist
}

func (s *learnerSkillStore) PutIfVersion(_ context.Context, owner, _, _, path string, r io.Reader, _ int64, _ string, _ workspace.Version) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[owner+"|"+path] = data
	return nil
}

func (s *learnerSkillStore) content(owner, path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.objects[owner+"|"+path])
}

func (s *learnerSkillStore) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.objects))
	for k := range s.objects {
		out = append(out, k)
	}
	return out
}

// learnerProbeProvider answers every call with the extraction JSON and records
// what reached it — through whatever provider it actually is (raw or wrapped).
type learnerProbeProvider struct {
	mu    sync.Mutex
	seen  []provider.Message
	reply string
}

func (p *learnerProbeProvider) record(msgs []provider.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, msgs...)
}

func (p *learnerProbeProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	p.record(msgs)
	return &provider.Response{Content: p.reply}, nil
}

func (p *learnerProbeProvider) ChatStream(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	p.record(msgs)
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: p.reply, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func (p *learnerProbeProvider) saw(s string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.seen {
		if strings.Contains(m.Content, s) {
			return true
		}
	}
	return false
}

func extractionJSON(slug string) string {
	return `{"extract":true,"skill":{"name":"` + slug + `","slug":"` + slug + `","description":"A demo skill.","content":"---\nname: ` + slug + `\ndescription: A demo skill.\n---\n\nStep 1: do the thing.\n"}}`
}

func learnerTestAgent(t *testing.T, base string) config.ResolvedAgent {
	t.Helper()
	home := filepath.Join(base, "agents", "agt_learner", "agent")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return config.ResolvedAgent{
		ID: "agt_learner", UserID: "u_owner", Home: home,
		Workspace: filepath.Join(home, "workspace"), Model: "fake-model",
		MaxTokens: 128, Temperature: 0.7, MaxToolIterations: 2,
	}
}

func TestTheSkillsLearnerRowReachesTheLearnerAndWritesThroughTheSingleWriter(t *testing.T) {
	base := t.TempDir()
	t.Setenv("FASTAGENT_HOME", base)
	rc := learnerTestAgent(t, base)

	// Row off: no learner at all — so "the row is what turns it on" is the
	// first half of the witness, not an assumption.
	off, err := NewManager([]config.ResolvedAgent{rc}, &learnerProbeProvider{reply: extractionJSON("demo")}, bus.New(),
		WithUserID("u_owner"))
	if err != nil {
		t.Fatalf("manager with the row off: %v", err)
	}
	if ag := off.AgentByID(rc.ID); ag == nil || ag.skillsLearner != nil {
		t.Fatal("a learner was built with the skillsLearner row off")
	}

	objstore := newLearnerSkillStore()
	prov := &learnerProbeProvider{reply: extractionJSON("demo")}
	mgr, err := NewManager([]config.ResolvedAgent{rc}, prov, bus.New(),
		WithUserID("u_owner"),
		WithWorkspaceStore(objstore),
		WithSkillsLearner(config.SkillsLearnerCfg{Enabled: true, Model: "distiller"}),
	)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	ag := mgr.AgentByID(rc.ID)
	if ag == nil {
		t.Fatal("agent not built")
	}
	if ag.skillsLearner == nil {
		t.Fatal("the skillsLearner row did not reach the agent: no learner was built")
	}
	if ag.skillsLearner.model != "distiller" {
		t.Errorf("learner model = %q, want the row's model", ag.skillsLearner.model)
	}
	if ag.skillsLearner.writer == nil {
		t.Fatal("learner was built without the skills namespace's writer")
	}

	msgs := []provider.Message{
		{Role: "user", Content: "line the reports up and publish them"},
		{Role: "assistant", Content: "done"},
	}
	if err := ag.skillsLearner.MaybeExtract(context.Background(), msgs, 5); err != nil {
		t.Fatalf("MaybeExtract: %v", err)
	}

	userRoot := filepath.Join(base, "users", "u_owner")
	disk := filepath.Join(userRoot, "skills", "demo", "SKILL.md")
	if _, err := os.Stat(disk); err != nil {
		t.Errorf("learned skill is not at the canonical skills path %s: %v", disk, err)
	}
	if got := objstore.content(skills.UserSkillOwner("u_owner"), "skills/demo/SKILL.md"); !strings.Contains(got, "Step 1") {
		t.Errorf("learned skill never reached the object store (objects=%v): a skill that exists on one pod's disk only is exactly the hole this pins",
			objstore.keys())
	}
	if _, err := os.Stat(filepath.Join(rc.Home, "skills", "demo", "SKILL.md")); err == nil {
		t.Error("the learner also wrote <agent home>/skills directly — that is the second writer for the namespace")
	}
}

func TestTheSkillsLearnerExtractionCallSitsInsideThePiiScrubbingRule(t *testing.T) {
	base := t.TempDir()
	t.Setenv("FASTAGENT_HOME", base)
	rc := learnerTestAgent(t, base)

	prov := &learnerProbeProvider{reply: extractionJSON("demo")}
	mgr, err := NewManager([]config.ResolvedAgent{rc}, prov, bus.New(),
		WithUserID("u_owner"),
		WithPrivacy(config.PrivacyCfg{PIIScrubbing: config.PIIScrubCfg{Enabled: true}}),
		WithSkillsLearner(config.SkillsLearnerCfg{Enabled: true}),
	)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	ag := mgr.AgentByID(rc.ID)
	if ag == nil || ag.skillsLearner == nil {
		t.Fatal("no learner to exercise — the wiring half of this test failed first")
	}

	msgs := []provider.Message{{Role: "user", Content: "my mail is " + learnerProbeEmail}}
	if err := ag.skillsLearner.MaybeExtract(context.Background(), msgs, 5); err != nil {
		t.Fatalf("MaybeExtract: %v", err)
	}

	if prov.saw(learnerProbeEmail) {
		t.Errorf("with piiScrubbing on, the extraction call still carried the raw address")
	}
	if !prov.saw("[EMAIL]") {
		t.Error("no [EMAIL] reached the extraction call — either it never happened or the assertion above is vacuous")
	}
}
