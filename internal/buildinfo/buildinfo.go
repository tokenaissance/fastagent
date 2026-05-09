// Package buildinfo holds the binary's build identity (version, commit,
// date) populated at link time via -ldflags -X. Lives in its own package
// (rather than cmd/fastclaw/main) so internal/* code — notably the agent
// system-prompt builder — can read it without dragging the cmd package
// into a dependency loop.
package buildinfo

import (
	"os"
	"strings"
)

// Version is the FastClaw release tag (e.g. "v0.4.2") set by the
// Makefile via `git describe --tags`. Defaults to "dev" for ad-hoc
// `go build`s where no ldflag is passed; consumers should treat that
// as "no published version" rather than a real release.
var Version = "dev"

// Commit is the short git SHA of the build's source tree.
var Commit = "unknown"

// Date is the UTC timestamp the binary was built.
var Date = "unknown"

// IsHostedDeploy reports whether this fastclaw process is running in a
// hosted/multi-tenant deployment (cloud) versus a self-hosted single-
// operator install. Driven by the FASTCLAW_DEPLOY env var:
//
//	FASTCLAW_DEPLOY=hosted        → IsHostedDeploy() == true
//	FASTCLAW_DEPLOY=self-hosted   → false
//	(unset or anything else)      → false (default = self-hosted)
//
// Operators set this in their cloud deployment manifests (k8s
// values.yaml, docker-compose env, …). Default-self-hosted matches
// the most common case (developer running fastclaw on their own
// laptop) and avoids surprising upgrade prompts when an operator
// just forgets the env var on their cloud deploy — better to
// default to "tell the user how to upgrade" and only suppress when
// explicitly opted in.
//
// Read each call (not cached) so a config-edit + sighup flow can
// flip it without a process restart, though in practice it's set
// once at boot.
func IsHostedDeploy() bool {
	return osDeployVar() == "hosted"
}

// osDeployVar reads FASTCLAW_DEPLOY and normalizes it. Lowercased so
// casing variations don't silently bypass the hosted flag.
func osDeployVar() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("FASTCLAW_DEPLOY")))
}
