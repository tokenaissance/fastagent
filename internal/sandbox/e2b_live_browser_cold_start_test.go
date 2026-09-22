package sandbox_test

// The browser's cold start, against a REAL e2b sandbox built from
// deploy/docker/sandbox/Dockerfile.
//
// Provisioning no longer pre-warms the camoufox daemon — on dev that was 24s of
// a 26s cold start, paid by every fresh scope, and it never covered the adopt
// or rebuild paths. The insurance moved into the image's shim
// (deploy/docker/sandbox/camoufox-cli-shim.sh), so what has to hold here is the
// claim the warm-up used to make: the FIRST `camoufox-cli open` in a scope
// nobody has used still succeeds.
//
// Two things only this test can check:
//
//	1. the template actually carries the shim with the retry — a gateway rolled
//	   out without the rebuilt image is the unguarded combination;
//	2. a fresh sandbox opens a page with no warm-up having run.
//
// Requires live e2b credentials:
//
//	E2B_API_KEY=e2b_... E2B_TEMPLATE=fastclaw-sandbox \
//	  go test ./internal/sandbox/ -run TestE2BLiveBrowserColdStart -v -count=1
import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

func TestE2BLiveBrowserColdStart(t *testing.T) {
	apiKey := os.Getenv("E2B_API_KEY")
	template := os.Getenv("E2B_TEMPLATE")
	if apiKey == "" || template == "" {
		// Same rule as the other live e2e tests: in CI this must not pass
		// silently, because a green run would be read as "verified".
		if os.Getenv("CI") != "" {
			t.Fatal("TestE2BLiveBrowserColdStart requires E2B_API_KEY and E2B_TEMPLATE in CI")
		}
		t.Skip("set E2B_API_KEY and E2B_TEMPLATE to run the live cold-start test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	pool := sandbox.NewE2BExecutorPool(apiKey, template, t.TempDir(), 10*time.Minute)
	defer pool.CloseAll()
	pool.SetWorkspace(workspace.NewLocalFS(t.TempDir()))

	// A scope nobody has used, so this is the create path and nothing else.
	agent := fmt.Sprintf("live_coldstart_%d", time.Now().UnixNano())
	ex, err := pool.Get(ctx, agent, "", "sess-coldstart")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	shim, err := ex.ReadFile(ctx, "/usr/local/bin/camoufox-cli")
	if err != nil {
		t.Skipf("this template does not ship camoufox-cli at /usr/local/bin/camoufox-cli (%v)", err)
	}
	if !strings.Contains(shim, "CAMOUFOX_SHIM_WAIT_SECS") {
		t.Fatalf("template %q carries a shim without the cold-start retry: rebuild the sandbox image (deploy/docker/sandbox/build.sh) and re-register the template before rolling out a gateway that no longer pre-warms", template)
	}

	// The acceptance: no warm-up ran during provisioning, and the first real
	// browser call in this sandbox still lands.
	out, err := ex.Exec(ctx, "cd /workspace && camoufox-cli open about:blank", 180*time.Second)
	if err != nil {
		t.Fatalf("first browser call in a fresh sandbox failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "Daemon did not start within 5 seconds") {
		t.Logf("the client hit its own 5s spawn wait and the shim's retry absorbed it:\n%s", out)
	}
	t.Logf("cold-start output:\n%s", out)
}
