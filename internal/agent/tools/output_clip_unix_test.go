//go:build unix

package tools

// The host exec path had the same hole as the sandbox one: cmd.CombinedOutput()
// returned whatever the command printed. `cat` of a 70 MB log on the host is just
// as fatal to the daemon as it was inside the container (2026-09-14).

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

func TestHostExecClipsHugeOutput(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()

	// 300 KB of output, no sandbox attached → the host path runs it.
	out, err := r.Execute(context.Background(), "exec",
		`{"command":"head -c 300000 /dev/zero | tr '\\0' x"}`)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "of output omitted") {
		t.Fatalf("300 KB of host output must come back clipped; got %d bytes with no marker", len(out))
	}
	if len(out) > sandbox.OutputHeadCap+sandbox.OutputTailCap+400 {
		t.Fatalf("clipped host result is %d bytes — the cap must bound it", len(out))
	}
}

func TestHostExecLeavesNormalOutputAlone(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()

	out, err := r.Execute(context.Background(), "exec", `{"command":"printf 'a\\nb\\n'"}`)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out, "omitted") || out != "a\nb\n" {
		t.Fatalf("small output was touched: %q", out)
	}
}
