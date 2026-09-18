package setup

import (
	"os"
	"testing"
)

// requireLiveNet gates the tests that reach the PUBLIC INTERNET — here, the
// skill-install path (codeload.github.com / api.github.com).
//
// Same convention as internal/skills' helper of the same name and as the live
// sandbox suite: `go test ./...` must be a statement about this repository, not
// about somebody else's server being up. Before this gate,
// TestRunInstall_GitHub_ReturnsRepoInResult downloaded a real tarball on every
// run and failed after ~112s when GitHub answered 404 (found 2026-09-18 while
// verifying the offline suite; see docs/文件系统形式化证明/11-change-register.md §11.5).
func requireLiveNet(t *testing.T) {
	t.Helper()
	if os.Getenv("FASTAGENT_NET_LIVE") != "1" {
		t.Skip("live network: set FASTAGENT_NET_LIVE=1 to run " +
			"(this test installs a skill from codeload.github.com)")
	}
}
