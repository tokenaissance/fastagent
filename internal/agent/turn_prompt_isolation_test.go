package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

type testChatKey struct{}

// skillPromptStore serves a per-chatter skill set from an in-memory object
// store. List is owner-scoped, which is exactly how SkillsLoader asks for the
// global, agent and per-user layers.
type skillPromptStore struct {
	workspace.Store
	mu      sync.Mutex
	objects map[string][]workspace.ObjectInfo
	content map[string]string
}

func (s *skillPromptStore) List(_ context.Context, owner, _, _ string) ([]workspace.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]workspace.ObjectInfo(nil), s.objects[owner]...), nil
}

func (s *skillPromptStore) Get(_ context.Context, owner, _, _, path string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.content[owner+"|"+path]
	if !ok {
		return nil, workspace.ErrNotFound
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func (s *skillPromptStore) Stat(context.Context, string, string, string, string) (*workspace.ObjectInfo, error) {
	return nil, workspace.ErrNotFound
}

func (s *skillPromptStore) PutIfVersion(context.Context, string, string, string, string, io.Reader, int64, string, workspace.Version) error {
	return nil
}

func addSkillToStore(s *skillPromptStore, owner, name, description string) {
	path := "skills/" + name + "/SKILL.md"
	body := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s body", name, description, name)
	s.objects[owner] = append(s.objects[owner], workspace.ObjectInfo{Path: path, Size: int64(len(body))})
	s.content[owner+"|"+path] = body
}

type promptCaptureProvider struct {
	mu       sync.Mutex
	prompts  map[string]string
	announce chan string
}

func (p *promptCaptureProvider) record(msgs []provider.Message, reply string) *provider.Response {
	chat := ""
	for _, m := range msgs {
		if strings.Contains(m.Content, "chat-a") {
			chat = "chat-a"
			break
		}
		if strings.Contains(m.Content, "chat-b") {
			chat = "chat-b"
			break
		}
	}
	system := ""
	if len(msgs) > 0 && msgs[0].Role == "system" {
		system = msgs[0].Content
	}
	p.mu.Lock()
	p.prompts[chat] = system
	p.mu.Unlock()
	if p.announce != nil {
		p.announce <- chat
	}
	return &provider.Response{Content: reply}
}

func (p *promptCaptureProvider) Chat(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	return p.record(msgs, "ok"), nil
}

func (p *promptCaptureProvider) ChatStream(_ context.Context, msgs []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	resp := p.record(msgs, "ok")
	ch := make(chan provider.StreamChunk, 2)
	ch <- provider.StreamChunk{Content: resp.Content, Done: true}
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func (p *promptCaptureProvider) prompt(chat string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prompts[chat]
}

// TestConcurrentChatsKeepTheirOwnPromptState parks chat A after its skill and
// environment sampling, lets chat B complete a full turn, then resumes A. A's
// system prompt must still carry only A's skill summary and environment signal.
func TestConcurrentChatsKeepTheirOwnPromptState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FASTAGENT_HOME", root)

	store := &skillPromptStore{
		objects: map[string][]workspace.ObjectInfo{},
		content: map[string]string{},
	}
	addSkillToStore(store, "_user_u_a", "alpha-skill", "A only.")
	addSkillToStore(store, "_user_u_b", "beta-skill", "B only.")

	a, _ := newGateAgent(t)
	a.agentID = "agent-prompt"
	a.homeDir = root
	a.homePath = filepath.Join(root, "agents", "agent-prompt", "agent")
	if err := os.MkdirAll(a.homePath, 0o755); err != nil {
		t.Fatal(err)
	}
	a.workspaceStore = store

	prov := &promptCaptureProvider{prompts: map[string]string{}, announce: make(chan string, 4)}
	a.provider = prov

	msgA := bus.InboundMessage{Channel: "web", UserID: "u_a", ChatID: "chat-a", Text: "chat-a"}
	msgB := bus.InboundMessage{Channel: "web", UserID: "u_b", ChatID: "chat-b", Text: "chat-b"}
	seedReceipt(t, a, msgA, `{"skills":{"alpha-skill":"personal|old A"}}`)
	seedReceipt(t, a, msgB, `{"skills":{"beta-skill":"personal|old B"}}`)

	atPrompt := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	a.hooks.Register(BeforeSystemPrompt, func(ctx context.Context, _ *HookContext) {
		if chat, _ := ctx.Value(testChatKey{}).(string); chat == "chat-a" {
			once.Do(func() { close(atPrompt) })
			<-release
		}
	})

	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		ctx := context.WithValue(context.Background(), testChatKey{}, "chat-a")
		a.HandleMessage(ctx, msgA)
	}()

	select {
	case <-atPrompt:
	case <-time.After(5 * time.Second):
		t.Fatal("chat A never reached BeforeSystemPrompt")
	}

	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		ctx := context.WithValue(context.Background(), testChatKey{}, "chat-b")
		a.HandleMessage(ctx, msgB)
	}()
	select {
	case <-doneB:
	case <-time.After(5 * time.Second):
		t.Fatal("chat B never finished")
	}

	close(release)
	select {
	case <-doneA:
	case <-time.After(5 * time.Second):
		t.Fatal("chat A never finished")
	}

	promptA := prov.prompt("chat-a")
	promptB := prov.prompt("chat-b")
	if !strings.Contains(promptA, "alpha-skill") || strings.Contains(promptA, "beta-skill") {
		t.Fatalf("chat A observed another chatter's skill catalog:\n%s", promptA)
	}
	if !strings.Contains(promptB, "beta-skill") || strings.Contains(promptB, "alpha-skill") {
		t.Fatalf("chat B observed another chatter's skill catalog:\n%s", promptB)
	}
	if !strings.Contains(promptA, "skills changed: alpha-skill") || strings.Contains(promptA, "beta-skill") {
		t.Fatalf("chat A observed another chatter's environment signal:\n%s", promptA)
	}
	if !strings.Contains(promptB, "skills changed: beta-skill") || strings.Contains(promptB, "alpha-skill") {
		t.Fatalf("chat B observed another chatter's environment signal:\n%s", promptB)
	}
}

func seedReceipt(t *testing.T, a *Agent, msg bus.InboundMessage, receipt string) {
	t.Helper()
	sess := a.sessions.Get(sessionTriple(msg, msg.ProjectID))
	sess.SetRunReceipt(receipt)
	sess.Append(provider.Message{Role: "assistant", Content: "previous"})
}

func TestLoadSkillReadsTurnScopeDirectories(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeSkill := func(dir, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "shared"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "shared", "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSkill(dirA, "A skill body")
	writeSkill(dirB, "B skill body")

	r := tools.NewRegistry(t.TempDir(), t.TempDir())
	tools.RegisterLoadSkill(r, []string{dirB})
	args, _ := json.Marshal(map[string]string{"name": "shared"})

	ctxA := tools.WithTurnScope(context.Background(), &tools.TurnScope{SkillDirs: []string{dirA}})
	got, err := r.Execute(ctxA, "load_skill", string(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "A skill body") || strings.Contains(got, "B skill body") {
		t.Fatalf("load_skill ignored the turn scope directories:\n%s", got)
	}
}

func TestUsageCaptureIsPerRequest(t *testing.T) {
	a := &Agent{}
	ctxA := WithUsageCapture(context.Background(), &UsageCapture{})
	ctxB := WithUsageCapture(context.Background(), &UsageCapture{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.meterTokens(ctxA, "session-a", provider.Usage{InputTokens: 11, OutputTokens: 12}, 0)
	}()
	go func() {
		defer wg.Done()
		a.meterTokens(ctxB, "session-b", provider.Usage{InputTokens: 21, OutputTokens: 22}, 0)
	}()
	wg.Wait()

	uA, okA := usageCaptureFrom(ctxA).Snapshot()
	uB, okB := usageCaptureFrom(ctxB).Snapshot()
	if !okA || !okB {
		t.Fatal("a request capture did not record usage")
	}
	if uA.InputTokens != 11 || uB.InputTokens != 21 {
		t.Fatalf("usage crossed request boundaries: A=%+v B=%+v", uA, uB)
	}
}
