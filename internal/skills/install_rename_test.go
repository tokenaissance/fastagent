package skills

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// apiGitHubTransport rewrites api.github.com requests to a local test server
// so githubRepoIdentity / githubDefaultBranch can be exercised against a
// deterministic stub of the GitHub API (no live network, no rate limits).
type apiGitHubTransport struct {
	base string // e.g. "http://127.0.0.1:PORT"
}

func (t apiGitHubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if strings.HasPrefix(clone.URL.Host, "api.github.com") {
		// The original req is discarded after RoundTrip returns, so mutating
		// the cloned URL fields in place is safe.
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(t.base, "http://")
	}
	return http.DefaultTransport.RoundTrip(clone)
}

// TestGithubRepoIdentity_FollowsRenameRedirect is the core regression test
// for the probe HTTP 404 bug: api.github.com returns a 301 for a renamed repo
// (net/http follows it) while codeload.github.com does NOT, so installs must
// build the tarball URL from the canonical name returned by the identity call.
func TestGithubRepoIdentity_FollowsRenameRedirect(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/tokenaissance/silicon-economics-skill":
			// Stale name → 301 to the canonical repo (GitHub rename).
			http.Redirect(w, r, "https://api.github.com/repos/tokenaissance/silicon-economics", http.StatusMovedPermanently)
		case "/repos/tokenaissance/silicon-economics":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"full_name":"tokenaissance/silicon-economics","default_branch":"main"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	client := &http.Client{Transport: apiGitHubTransport{base: api.URL}}

	owner, repo, branch, ok := githubRepoIdentity(client, "tokenaissance", "silicon-economics-skill")
	if !ok {
		t.Fatal("githubRepoIdentity(renamed repo) → ok=false, want true")
	}
	if owner != "tokenaissance" || repo != "silicon-economics" {
		t.Fatalf("canonical repo = %s/%s, want tokenaissance/silicon-economics", owner, repo)
	}
	if branch != "main" {
		t.Fatalf("default branch = %q, want main", branch)
	}

	// The pre-fix code built the tarball URL from the stale name → 404 on
	// codeload ("probe HTTP 404"). The fix resolves the canonical name first.
	stale := codeloadTarURL("tokenaissance", "silicon-economics-skill", branch)
	canonical := codeloadTarURL(owner, repo, branch)
	if stale == canonical {
		t.Fatalf("canonical resolution changed nothing: %s", canonical)
	}
	if !strings.HasSuffix(canonical, "/tokenaissance/silicon-economics/tar.gz/refs/heads/main") {
		t.Fatalf("tarball URL not built from canonical name: %s", canonical)
	}
	t.Logf("stale URL:   %s", stale)
	t.Logf("canonical URL: %s ✓ (this is what gets downloaded)", canonical)
}

// TestGithubRepoIdentity_UnchangedRepo verifies the common case (no rename)
// returns the input owner/repo unchanged, so the tarball URL is identical.
func TestGithubRepoIdentity_UnchangedRepo(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"full_name":"acme/widgets","default_branch":"trunk"}`)
	}))
	defer api.Close()

	client := &http.Client{Transport: apiGitHubTransport{base: api.URL}}

	owner, repo, branch, ok := githubRepoIdentity(client, "acme", "widgets")
	if !ok || owner != "acme" || repo != "widgets" || branch != "trunk" {
		t.Fatalf("identity = (%s, %s, %s, %v), want (acme, widgets, trunk, true)", owner, repo, branch, ok)
	}
	t.Logf("unchanged repo identity ✓ (owner=%s repo=%s branch=%s)", owner, repo, branch)
}

// TestGithubRepoIdentity_APIFailureFallsBack verifies the install path is
// never blocked by the identity probe: an API failure returns ok=false so
// callers fall back to the input owner/repo and the main/master loop.
func TestGithubRepoIdentity_APIFailureFallsBack(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer api.Close()

	client := &http.Client{Transport: apiGitHubTransport{base: api.URL}}

	if _, _, _, ok := githubRepoIdentity(client, "acme", "widgets"); ok {
		t.Fatal("githubRepoIdentity → ok=true on API failure, want false")
	}
	t.Log("githubRepoIdentity → ok=false ✓ (caller falls back to input + main/master)")
}

// TestGithubDefaultBranch_FollowsRename verifies githubDefaultBranch — used by
// FetchSkillReadme and previously by InstallFromSkillsSh — still discovers the
// default branch through the shared identity resolution when the repo was
// renamed.
func TestGithubDefaultBranch_FollowsRename(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/old-name":
			http.Redirect(w, r, "https://api.github.com/repos/acme/current-name", http.StatusMovedPermanently)
		case "/repos/acme/current-name":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"full_name":"acme/current-name","default_branch":"develop"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	client := &http.Client{Transport: apiGitHubTransport{base: api.URL}}

	if got := githubDefaultBranch(client, "acme", "old-name"); got != "develop" {
		t.Fatalf("githubDefaultBranch(renamed) = %q, want develop", got)
	}
	t.Log("githubDefaultBranch follows rename ✓")
}

// TestCodeloadTarURL pins the shared URL builder used by both install paths.
func TestCodeloadTarURL(t *testing.T) {
	u := codeloadTarURL("tokenaissance", "silicon-economics", "main")
	want := "https://codeload.github.com/tokenaissance/silicon-economics/tar.gz/refs/heads/main"
	if u != want {
		t.Fatalf("codeloadTarURL = %q, want %q", u, want)
	}
	t.Logf("codeloadTarURL ✓ %s", u)
}
