package skills

import (
	"os"
	"testing"
)

// requireLiveNet gates the tests that reach the PUBLIC INTERNET — skills.sh,
// api.github.com and codeload.github.com.
//
// Why a gate: `go test ./...` has to be a statement about this repository, not
// about somebody else's server. Before this, `TestInstallFromSkillsSh_RepoField`
// downloaded a real tarball on every run, took ~115s, and failed with
// `InstallFromSkillsSh: probe HTTP 404` when the upstream repo moved — leaving
// the suite red for a reason no commit here caused (found 2026-09-18, see
// docs/文件系统形式化证明/11-change-register.md §11.5).
//
// Same convention as the live sandbox suite (`FASTAGENT_E2B_LIVE=1`): opt in
// explicitly, and say what to set in the skip message.
func requireLiveNet(t *testing.T) {
	t.Helper()
	if os.Getenv("FASTAGENT_NET_LIVE") != "1" {
		t.Skip("live network: set FASTAGENT_NET_LIVE=1 to run " +
			"(this test reaches skills.sh / api.github.com / codeload.github.com)")
	}
}
