package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNilAgentFileLoaderReadsNoLayerThreeConfig locks the agent.json
// retirement at the resolution layer: a nil loader means no layer-3 source, and
// the merge must not look for a file of its own. Even when a legacy agent.json
// sits next to the agent, the resolved config carries nothing from it. The
// DB-first loader lives in the gateway, and its own test proves that it ignores
// the file too.
func TestNilAgentFileLoaderReadsNoLayerThreeConfig(t *testing.T) {
	home := t.TempDir()
	legacy := `{"model":"openai/gpt-4o-mini","mcpServers":{"quandora":{"type":"http","url":"https://mcp.quandora.ai/quant"}}}`
	if err := os.WriteFile(filepath.Join(home, "agent.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy agent.json: %v", err)
	}

	resolved := (&Config{}).MergedAgentConfig(AgentEntry{ID: "agent-1"}, nil)
	if resolved.Model == "openai/gpt-4o-mini" {
		t.Fatalf("merge read retired agent.json: model=%q", resolved.Model)
	}
	if len(resolved.MCPServers) != 0 {
		t.Fatalf("merge read retired agent.json: mcpServers=%+v", resolved.MCPServers)
	}
}
