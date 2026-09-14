package agent

// Which provider an agent's model ends up on is decided at build time by
// providerForAgent, and three of its four paths fall back to the SHARED
// provider without a trace. That silence is expensive: a bare model name looks
// fine, sends the request on whatever account the shared key carries, and the
// only symptom is an upstream "No available channel for model X under group Y"
// — the model name was right, the account was not the intended one. These
// lines are what turns that into a five-second diagnosis, so they are pinned
// like the grace window's are.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// sharedStubProvider stands in for the user-space provider every agent inherits.
type sharedStubProvider struct{ name string }

func (p *sharedStubProvider) Chat(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.Response, error) {
	return &provider.Response{}, nil
}

func (p *sharedStubProvider) ChatStream(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.StreamReader, error) {
	ch := make(chan provider.StreamChunk)
	close(ch)
	return provider.NewStreamReader(ch), nil
}

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestProviderForAgentLogsEverySilentFallback(t *testing.T) {
	shared := &sharedStubProvider{name: "shared"}

	t.Run("bare model names the shared-account consequence", func(t *testing.T) {
		logs := captureWarnings(t)
		rc := config.ResolvedAgent{ID: "agt-1", Model: "deepseek-flash"}

		if got := providerForAgent(rc, shared); got != provider.Provider(shared) {
			t.Fatalf("a bare model must fall back to the shared provider, got %T", got)
		}
		out := logs.String()
		if !strings.Contains(out, "no provider prefix") {
			t.Fatalf("missing the no-prefix warning; logs=%q", out)
		}
		// The two facts an operator needs: which model, and what to write instead.
		if !strings.Contains(out, `model=deepseek-flash`) || !strings.Contains(out, "providerKey>/<modelId>") {
			t.Fatalf("warning does not name the model and the fix; logs=%q", out)
		}
	})

	t.Run("prefixed model that resolves stays quiet", func(t *testing.T) {
		logs := captureWarnings(t)
		rc := config.ResolvedAgent{
			ID: "agt-1", Model: "deepseek/deepseek-flash",
			Providers: map[string]config.ProviderConfig{
				"deepseek": {APIKey: "sk-real", APIBase: "https://example.test/v1", APIType: "openai-chat"},
			},
		}

		if got := providerForAgent(rc, shared); got == provider.Provider(shared) {
			t.Fatal("a resolvable <provider>/<model> must build its own provider")
		}
		if out := logs.String(); out != "" {
			t.Fatalf("a working configuration must not warn; logs=%q", out)
		}
	})

	t.Run("unknown provider key lists what does exist", func(t *testing.T) {
		logs := captureWarnings(t)
		rc := config.ResolvedAgent{
			ID: "agt-1", Model: "deepsek/deepseek-flash", // typo on purpose
			Providers: map[string]config.ProviderConfig{
				"deepseek": {APIKey: "sk-real"},
				"openai":   {APIKey: "sk-other"},
			},
		}

		if got := providerForAgent(rc, shared); got != provider.Provider(shared) {
			t.Fatalf("an unknown key must fall back to the shared provider, got %T", got)
		}
		out := logs.String()
		if !strings.Contains(out, "unknown provider") || !strings.Contains(out, "deepseek,openai") {
			t.Fatalf("warning must list the known keys (sorted); logs=%q", out)
		}
	})

	t.Run("provider row without a key says so", func(t *testing.T) {
		logs := captureWarnings(t)
		rc := config.ResolvedAgent{
			ID: "agt-1", Model: "deepseek/deepseek-flash",
			Providers: map[string]config.ProviderConfig{"deepseek": {APIBase: "https://example.test/v1"}},
		}

		if got := providerForAgent(rc, shared); got != provider.Provider(shared) {
			t.Fatalf("a keyless row must fall back to the shared provider, got %T", got)
		}
		out := logs.String()
		if !strings.Contains(out, "without an API key") || !strings.Contains(out, `providerKey=deepseek`) {
			t.Fatalf("warning must name the keyless row; logs=%q", out)
		}
	})
}
