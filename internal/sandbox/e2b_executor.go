package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// E2B API: https://e2b.dev/docs
// Sandbox creation: POST https://api.e2b.dev/sandboxes
// Command execution: Connect protocol via envd on the sandbox

const e2bBaseURL = "https://api.e2b.dev"
const e2bEnvdPort = "49983"

// sandboxIdent is the pair every envd request authenticates against: the
// instance id (it selects the endpoint) and that instance's access token.
//
// The two travel as one value because a rebuild swaps them together. Reading
// them separately lets a request be assembled from a new id and the old token
// (or the reverse), which envd answers as an opaque 401/502 — indistinguishable
// from the sandbox being gone, so it would trigger yet another rebuild.
//
// rebuilt rides along in the same value: it means "this identity has not
// reached the shared lease yet". Keeping it in the snapshot means the flag is
// always read with the identity it describes, and the pool's clear step can be
// conditional on that identity (see clearRebuild).
type sandboxIdent struct {
	id      string
	token   string
	rebuilt bool
}

// E2BExecutor implements Executor using E2B hosted sandboxes.
type E2BExecutor struct {
	apiKey string
	// stateMu guards ident. Critical sections are a few field reads — never
	// I/O — so a request building its URL cannot be held up by a rebuild, and
	// a rebuild cannot be observed half-applied.
	stateMu  sync.Mutex
	ident    sandboxIdent
	client   *http.Client
	template string        // remembered for recreate() so the new sandbox uses the same template; immutable once handed out by the pool
	timeout  time.Duration // remembered for recreate()
	// readyTimeout / readyInterval bound the post-create readiness wait. Zero
	// means the defaults; tests shrink them so a persistent routing gap does
	// not cost a minute of wall clock.
	readyTimeout  time.Duration
	readyInterval time.Duration
	// rebuildMu serialises recreate(). Parallel tool calls share one executor
	// (the agent loop fans tool calls out concurrently and only exec itself is
	// not serialised), so several goroutines can observe the same dead sandbox
	// and all decide to replace it. Without this lock each one mints its own
	// instance and every instance but the last is stranded: no lease row names
	// it and nothing ever closes it.
	rebuildMu sync.Mutex
	// closeSandboxFn overrides the HTTP DELETE used to destroy a sandbox.
	// Test-only seam so pool unit tests can assert an evicted/adopted-away
	// sandbox was closed without calling the real e2b API. Nil keeps the
	// production behavior.
	closeSandboxFn func(sandboxID string) error
	// createFn mints the replacement sandbox inside recreate(). Defaults to
	// newE2BExecutor; a field (rather than a direct call) so the whole rebuild
	// path — create → hydrate → verify → mark rebuilt — is exercisable offline,
	// and so a pool that injected its own create seam gets it honored on
	// rebuild too instead of silently falling back to the package function.
	createFn func(ctx context.Context, apiKey, template string, timeout time.Duration) (*E2BExecutor, error)
	// hydrate sources — set by the pool after creation so recreate()
	// can rebuild /skills + /workspace without reaching back into the
	// pool. Workspace store is optional; skill dirs may be empty.
	skillDirs []string
	workspace workspace.Store
	agentID   string
	projectID string
	sessionID string
}

// identSnapshot returns a consistent view of the identity a request must use.
func (e *E2BExecutor) identSnapshot() sandboxIdent {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	return e.ident
}

// setIdent publishes a new identity. rebuilt must stay false while the
// replacement is still being hydrated: the pool may route sibling pods to
// whatever the row names, and a half-built sandbox is worse than a dead one.
func (e *E2BExecutor) setIdent(id, token string, rebuilt bool) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	e.ident = sandboxIdent{id: id, token: token, rebuilt: rebuilt}
}

// pendingPublish reports the identity that still has to reach the shared
// lease, if any. See E2BExecutorPool.reconcileLocalLeaseLocked.
func (e *E2BExecutor) pendingPublish() (sandboxIdent, bool) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	return e.ident, e.ident.rebuilt
}

// clearRebuild drops the pending bit — but only when the executor still holds
// the identity that was just published. If a newer rebuild landed while the
// write was in flight, its own bit survives, so the next reconcile moves the
// row again instead of letting it drift from the executor.
func (e *E2BExecutor) clearRebuild(seen sandboxIdent) bool {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.ident.id != seen.id || e.ident.token != seen.token || !e.ident.rebuilt {
		return false
	}
	e.ident.rebuilt = false
	return true
}

func newE2BExecutor(ctx context.Context, apiKey, template string, timeout time.Duration) (*E2BExecutor, error) {
	if template == "" {
		template = "base"
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}

	// No global Client.Timeout: it covers the entire round-trip
	// including streaming the body, which silently cut long execs
	// (image generation, etc.) at 60s and made tools return empty
	// output with no error. We rely on per-request context.WithTimeout
	// at the call site instead — execOnce derives ctx from the user-
	// supplied tool timeout, and create-sandbox below uses an
	// explicit short ctx.
	client := &http.Client{}

	// Field name is `templateID` (camelCase) — verified by server's
	// validation error: `Error at "/templateID": property "templateID"
	// is missing` when the field was renamed to snake_case. The
	// snake_case form shows up in some SDK source code but the
	// production REST API rejects it.
	body, _ := json.Marshal(map[string]interface{}{
		"templateID": template,
		"timeout":    int(timeout.Seconds()),
	})
	// Bound the create-sandbox call to 60s — the call itself usually
	// completes in 1–2s; if it's hanging past that there's a control-
	// plane problem and we'd rather surface a clear timeout than wait
	// indefinitely on a request that inherits no deadline from ctx.
	createCtx, cancelCreate := context.WithTimeout(ctx, 60*time.Second)
	defer cancelCreate()
	req, err := http.NewRequestWithContext(createCtx, "POST", e2bBaseURL+"/sandboxes", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("e2b create sandbox: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("e2b create sandbox: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		SandboxID       string `json:"sandboxID"`
		EnvdAccessToken string `json:"envdAccessToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("e2b parse response: %w", err)
	}

	slog.Info("e2b sandbox created", "sandboxID", result.SandboxID, "template", template)

	ex := &E2BExecutor{
		apiKey:   apiKey,
		ident:    sandboxIdent{id: result.SandboxID, token: result.EnvdAccessToken},
		client:   client,
		template: template,
		timeout:  timeout,
		createFn: newE2BExecutor,
	}
	// POST /sandboxes returns an id before e2b's edge can route it. Waiting for
	// the first answer here keeps that gap inside creation, where it is a retry,
	// instead of leaking it into hydrate, where it is indistinguishable from a
	// dead sandbox (see waitUntilRoutable).
	if err := ex.waitUntilRoutable(ctx); err != nil {
		// Do not leak an instance we cannot talk to.
		_ = ex.closeSandboxByID(result.SandboxID)
		return nil, err
	}
	return ex, nil
}

// Defaults for waitUntilRoutable. The 60s ceiling matches the create call's own
// bound; the interval is short enough that a normal sandbox pays it once.
const (
	defaultReadyTimeout  = 60 * time.Second
	defaultReadyInterval = 1500 * time.Millisecond
)

// waitUntilRoutable blocks until a freshly created sandbox answers envd.
//
// Why this exists: POST /sandboxes returns an id before the edge can route it.
// The first call after create — hydrate's /files upload — is what pays for the
// gap, and it gets the edge's
//
//	{"sandboxId":...,"message":"The sandbox was not found","code":502}
//
// which is byte-identical to the answer for a sandbox that died long ago. That
// made a transient routing gap look like a dead instance, and the caller's
// recovery — rebuild — could never converge, because every rebuild created
// another sandbox and hit the same window.
//
// Only "not routable yet" is retried: a provider status that is not 502/404
// (a 401 from a stale token, a 500 inside the sandbox) is returned at once,
// because rebuilding cannot fix it and the caller should see it immediately.
// Transport errors are retried too — a brand-new hostname may not resolve yet.
func (e *E2BExecutor) waitUntilRoutable(ctx context.Context) error {
	timeout := e.readyTimeout
	if timeout <= 0 {
		timeout = defaultReadyTimeout
	}
	interval := e.readyInterval
	if interval <= 0 {
		interval = defaultReadyInterval
	}
	deadline := time.Now().Add(timeout)

	for attempt := 1; ; attempt++ {
		_, err := e.execOnce(ctx, "true", 15*time.Second)
		if err == nil {
			if attempt > 1 {
				slog.Info("e2b sandbox became routable",
					"sandboxID", e.identSnapshot().id, "attempts", attempt)
			}
			return nil
		}
		if _, isProviderVerdict := statusCodeOf(err); isProviderVerdict && !sandboxGone(err) {
			return fmt.Errorf("sandbox %s is not usable after create: %w", e.identSnapshot().id, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sandbox %s never became routable within %s (%d attempts): %w",
				e.identSnapshot().id, timeout, attempt, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (e *E2BExecutor) envdURLFor(sandboxID string) string {
	return fmt.Sprintf("https://%s-%s.e2b.app", e2bEnvdPort, sandboxID)
}

// recreateIfCurrent replaces the sandbox the caller observed failing. The same
// template / timeout the executor was originally built with are reused —
// hardcoding "base" here would silently demote a custom-template sandbox once
// it idled out. The full hydrate (skills + workspace) is replayed so
// /skills/<name>/ and /workspace/ stay populated across recreations.
//
// observed is the identity the failed request actually used, which makes this
// idempotent under concurrency: parallel calls that all watched the same
// sandbox die queue on rebuildMu, and every waiter but the first finds the
// executor already pointing somewhere else and returns without creating
// anything.
func (e *E2BExecutor) recreateIfCurrent(ctx context.Context, observed sandboxIdent) error {
	e.rebuildMu.Lock()
	defer e.rebuildMu.Unlock()

	if cur := e.identSnapshot(); cur.id != observed.id {
		slog.Info("e2b rebuild already performed by a parallel call",
			"observedSandboxID", observed.id, "currentSandboxID", cur.id)
		return nil
	}
	create := e.createFn
	if create == nil {
		create = newE2BExecutor
	}
	slog.Info("e2b sandbox expired, recreating", "oldSandboxID", observed.id)
	newEx, err := create(ctx, e.apiKey, e.template, e.timeout)
	if err != nil {
		return err
	}
	// Swap the identity before hydrating (the hydrate calls must land on the
	// replacement) but leave the pending bit clear: the pool may only publish
	// this identity once the sandbox below is proven usable.
	replacement := newEx.identSnapshot()
	e.setIdent(replacement.id, replacement.token, false)

	// Hydrate is mandatory for the same reason it is in Get() — it's
	// the only step that makes /workspace writable to the exec user.
	// If we just warn-log a failure here, the recreated sandbox is
	// silently broken: agent calls succeed but every /workspace write
	// fails with Permission denied, and the bytes get stranded in
	// /tmp where they evaporate at the next eviction.
	if err := e.Hydrate(ctx); err != nil {
		return e.abandonRebuild(observed, replacement.id,
			fmt.Errorf("hydrate after recreate (sandboxID=%s): %w", replacement.id, err))
	}
	if err := verifyWorkspaceWritable(ctx, e); err != nil {
		return e.abandonRebuild(observed, replacement.id,
			fmt.Errorf("recreated sandbox unusable (sandboxID=%s): %w", replacement.id, err))
	}
	// The shared lease still names the sandbox that just died. Mark the
	// executor so the pool moves the row onto this replacement at the next Get
	// (reconcileLocalLeaseLocked) — the pool owns the lease, so the executor
	// only records the fact and stays free of SQL and scope bookkeeping.
	e.setIdent(replacement.id, replacement.token, true)
	return nil
}

// abandonRebuild undoes a replacement that was created but never became
// usable. Without it the executor keeps a sandbox that cannot serve while the
// lease row still names the dead one, so the next reconcile reads the mismatch
// as "another replica took over", adopts the corpse back and closes the
// replacement — a create + close cycle on every call.
//
// Restoring the previous identity keeps memory and row agreeing, and
// destroying the replacement keeps a failed rebuild from leaking an instance
// nothing references. The cause is returned unchanged so callers still see
// why the rebuild failed; the next call retries from a consistent state.
func (e *E2BExecutor) abandonRebuild(prev sandboxIdent, failedSandboxID string, cause error) error {
	e.setIdent(prev.id, prev.token, false)
	if cerr := e.closeSandboxByID(failedSandboxID); cerr != nil {
		slog.Warn("e2b could not destroy the unusable replacement sandbox",
			"sandboxID", failedSandboxID, "error", cerr)
	}
	return cause
}

// SetHydrationSources records the inputs Hydrate() should pull from on
// the next call. Called by the pool right after sandbox creation; the
// executor then carries them so recreate() can replay everything without
// asking the pool. Pass nil/empty for any source you don't have.
func (e *E2BExecutor) SetHydrationSources(skillDirs []string, ws workspace.Store, agentID, projectID, sessionID string) {
	e.skillDirs = append(e.skillDirs[:0], skillDirs...)
	e.workspace = ws
	e.agentID = agentID
	e.projectID = projectID
	e.sessionID = sessionID
}

// Hydrate populates the sandbox with everything the agent's tools expect
// to find on disk:
//   - /skills/<name>/...   from each configured skill dir (per-agent +
//     global, first-wins precedence to match the docker bind-mount layer)
//   - /workspace/...       from the agent's workspace.Store (so files
//     written via write_file in past sessions survive sandbox restarts,
//     same contract as the existing per-file hydrateWorkspace)
//
// Implementation: pack everything into one tar.gz, upload it via envd's
// /files multipart endpoint (same path writeFileOnce uses — known good),
// then run a tiny `bash -c` to extract+chown. Earlier versions inlined
// the base64'd tar inside the `bash -c` arg; that worked for trivially
// small bundles but empirically truncated the Connect-protocol response
// once the encoded payload climbed past ~80KB-of-base64 (8 bundled
// skills was already 103KB and tripped it). Truncation surfaced as an
// envd End-frame with `exited=false` and no exit-status — the old code
// treated that as success because exitCode was still 0, so Hydrate
// looked like it ran while the chown step had actually been cut off
// mid-flight. The fix moves the bulk transfer off the exec channel
// entirely so the script stays small and constant-sized regardless of
// bundle size.
func (e *E2BExecutor) Hydrate(ctx context.Context) error {
	bundle := newTarBundle()

	skillCount := 0
	skillFileCount := 0
	seen := make(map[string]bool) // per-skill: first dir wins
	for _, dir := range e.skillDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if seen[name] {
				continue
			}
			seen[name] = true
			n, err := bundle.addLocalDir(filepath.Join(dir, name), "skills/"+name)
			if err != nil {
				slog.Warn("e2b hydrate: skill tar", "skill", name, "error", err)
				continue
			}
			skillCount++
			skillFileCount += n
		}
	}

	workspaceCount := 0
	if e.workspace != nil {
		// For project chats, hydrate the whole project (List with
		// session=""), so the chat sees sibling chats' files at
		// /workspace/<other-sid>/... — same visibility docker gets
		// from mounting projects/<pid>/ as the bind root. Loose chats
		// stay scoped to their own session subtree.
		listProject := e.projectID
		listSession := e.sessionID
		if e.projectID != "" {
			listSession = ""
		}
		objs, err := e.workspace.List(ctx, e.agentID, listProject, listSession)
		if err != nil {
			slog.Warn("e2b hydrate: workspace list", "agent", e.agentID, "project", e.projectID, "session", e.sessionID, "error", err)
		} else {
			for _, obj := range objs {
				rc, err := e.workspace.Get(ctx, e.agentID, listProject, listSession, obj.Path)
				if err != nil {
					slog.Warn("e2b hydrate: workspace get", "path", obj.Path, "error", err)
					continue
				}
				data, rerr := io.ReadAll(rc)
				rc.Close()
				if rerr != nil {
					slog.Warn("e2b hydrate: workspace read", "path", obj.Path, "error", rerr)
					continue
				}
				rel := strings.TrimPrefix(obj.Path, "/")
				if err := bundle.addBytes("workspace/"+rel, data, 0o644, obj.ModTime); err != nil {
					slog.Warn("e2b hydrate: workspace tar", "path", obj.Path, "error", err)
					continue
				}
				workspaceCount++
			}
		}
	}

	if err := bundle.close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}

	// Why every word here matters:
	// - We ALWAYS run the mkdir+chown step, even when there are no
	//   files to push. /workspace and /skills must exist and be
	//   writable by `user` regardless — image-tool / write_file /
	//   anything that writes there fails with ENOENT or EACCES if the
	//   dirs are missing. This was the failure mode on a fresh session
	//   with empty workspace: no files → previous code returned early
	//   → /workspace never created → "mkdir: Permission denied" when
	//   the LLM tried to make it itself as the non-root `user`.
	// - `sudo`: E2B's "base" template runs as `user`, who has no
	//   write access to /. The default user has passwordless sudo per
	//   e2b's published Dockerfile; custom templates that strip sudo
	//   either need to keep it or pre-create /skills + /workspace
	//   chowned to user.
	// - The bundle (when any files exist) is now uploaded via the
	//   /files endpoint BEFORE this exec runs — see uploadBytes above
	//   and the size-related comment on Hydrate itself. The script
	//   below only ever references /tmp/fc-hydrate.tar.gz as a path,
	//   so the bash -c arg stays small regardless of how large the
	//   bundle gets.
	// - chown after extract: tar-as-root lands files root-owned, so
	//   re-chown after extract; agent's subsequent writes run as user.
	cmdParts := []string{
		"set -e",
		"sudo mkdir -p /skills /workspace",
		"sudo chown user:user /skills /workspace",
	}
	if bundle.fileCount > 0 {
		cmdParts = append(cmdParts,
			"sudo tar -xzf /tmp/fc-hydrate.tar.gz -C /",
			"sudo chown -R user:user /skills /workspace",
			"rm -f /tmp/fc-hydrate.tar.gz",
		)
	}
	cmd := strings.Join(cmdParts, "; ")
	// Ship it, with a bounded retry. A sandbox created moments ago can cut a
	// stream once while it finishes booting — seen in production as "did not
	// exit cleanly … response stream truncated" from the mkdir/chown step, and
	// as a 502 from the upload before it. Both network steps are idempotent
	// (same tar to the same path, same mkdir/chown), and anything a retry
	// cannot fix — a 401, a permission error, an instance that is simply gone —
	// is not retried. The bundle is built once, above: assembling it walks the
	// workspace store.
	var err error
	for attempt := 1; attempt <= hydrateAttempts; attempt++ {
		err = e.shipBundleOnce(ctx, cmd, bundle)
		if err == nil {
			break
		}
		if attempt == hydrateAttempts || !retryableHydrateFailure(err) {
			break
		}
		slog.Warn("retrying hydrate against a freshly created sandbox",
			"sandboxID", e.identSnapshot().id, "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(hydrateRetryInterval):
		}
	}
	if err != nil {
		slog.Warn("e2b hydrate failed", "sandboxID", e.identSnapshot().id, "error", err)
		return err
	}
	slog.Info("e2b sandbox hydrated",
		"sandboxID", e.identSnapshot().id,
		"skills", skillCount,
		"skillFiles", skillFileCount,
		"workspaceFiles", workspaceCount,
		"tarBytes", bundle.gz.Len())
	return nil
}

// Hydrate's bounded retry. See shipBundleOnce for why a retry is safe and
// retryableHydrateFailure for what is allowed to use it.
const (
	hydrateAttempts      = 3
	hydrateRetryInterval = 1500 * time.Millisecond
)

// retryableHydrateFailure reports whether an envd failure is the kind a
// freshly created sandbox produces once and then not again: a stream that ended
// without its exit-status trailer (the container was still coming up), or an
// explicit 502/404 from the edge. Everything else — a 401 from a stale token, a
// permission error inside the sandbox — is a verdict, and retrying it just
// delays the report.
func retryableHydrateFailure(err error) bool {
	if err == nil {
		return false
	}
	if sandboxGone(err) {
		return true
	}
	var truncated *execStreamTruncatedError
	return errors.As(err, &truncated)
}

// shipBundleOnce uploads the tar (when there is one) and runs the
// mkdir/chown/extract script. Idempotent by construction, which is what makes
// the retry in Hydrate safe.
func (e *E2BExecutor) shipBundleOnce(ctx context.Context, cmd string, bundle *tarBundle) error {
	if bundle.fileCount > 0 {
		// Upload as `user`-owned to /tmp; tar still runs under sudo so
		// it can land /skills/* and /workspace/* at the filesystem root.
		if err := e.uploadBytes(ctx, "/tmp/fc-hydrate.tar.gz", bundle.gz.Bytes()); err != nil {
			return fmt.Errorf("hydrate upload tar: %w", err)
		}
	}
	out, err := e.execOnce(ctx, cmd, 60*time.Second)
	if err != nil {
		return fmt.Errorf("hydrate sandbox dirs: %w (output: %s)", err, out)
	}
	return nil
}

// tarBundle is a small helper around archive/tar + gzip so the Hydrate
// path doesn't have to repeat the writer-close dance. All paths in the
// bundle are sandbox-relative (no leading slash); callers pick the
// extraction root.
type tarBundle struct {
	gz        bytes.Buffer
	gw        *gzip.Writer
	tw        *tar.Writer
	fileCount int
}

func newTarBundle() *tarBundle {
	b := &tarBundle{}
	b.gw = gzip.NewWriter(&b.gz)
	b.tw = tar.NewWriter(b.gw)
	return b
}

// addBytes adds an in-memory file to the bundle. tar -xz auto-creates
// parent dirs from the entry path, so we don't need explicit dir
// entries — verified by a roundtrip test on the host.
func (b *tarBundle) addBytes(name string, data []byte, mode int64, modTime time.Time) error {
	if modTime.IsZero() {
		modTime = time.Now()
	}
	if err := b.tw.WriteHeader(&tar.Header{
		Name:     strings.TrimPrefix(name, "/"),
		Mode:     mode,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
		ModTime:  modTime,
	}); err != nil {
		return err
	}
	if _, err := b.tw.Write(data); err != nil {
		return err
	}
	b.fileCount++
	return nil
}

// addLocalDir walks a host directory and adds every regular file under
// it to the bundle, rooted at sandboxPrefix. Symlinks / sockets / etc.
// are skipped — a skill bundle should be plain files.
func (b *tarBundle) addLocalDir(localRoot, sandboxPrefix string) (int, error) {
	count := 0
	err := filepath.Walk(localRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(localRoot, p)
		if err != nil {
			return err
		}
		// Never ship a skill's top-level SKILL.md into the sandbox — it's
		// the agent's IP, the model already has it via load_skill, and the
		// sandbox only needs the skill's scripts/resources to run. Keeping
		// it out closes the `cat /skills/<name>/SKILL.md` exfil path.
		if rel == "SKILL.md" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		name := sandboxPrefix + "/" + filepath.ToSlash(rel)
		if err := b.addBytes(name, data, int64(info.Mode().Perm()), info.ModTime()); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

func (b *tarBundle) close() error {
	if err := b.tw.Close(); err != nil {
		return err
	}
	return b.gw.Close()
}

// isSandboxGone checks if a status code means the sandbox itself is gone:
// e2b answers 502 for an instance it has already reaped and 404 for one it
// never had.
func isSandboxGone(statusCode int) bool {
	return statusCode == http.StatusBadGateway || statusCode == http.StatusNotFound
}

// sandboxGone reports whether err says the instance no longer exists — the
// only failure a rebuild can cure. Anything else (a 401 from a stale token, a
// 500 inside the sandbox) must surface to the caller instead of costing a
// sandbox.
func sandboxGone(err error) bool {
	status, ok := statusCodeOf(err)
	return ok && isSandboxGone(status)
}

// connectEnvelope wraps JSON payload in Connect protocol envelope framing.
// Format: [1 byte flags][4 bytes big-endian length][payload]
func connectEnvelope(payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	buf[0] = 0 // flags: no compression, not end of stream
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	return buf
}

// parseConnectStream reads Connect protocol streaming response.
// Each frame: [1 byte flags][4 bytes length][payload]
//
// Trailers (flags & 0x02) are returned rather than dropped. They used to be
// skipped, which discarded the one place the protocol carries a server-side
// error: a stream that fails mid-flight ends with an end-stream frame holding
// {"error":{...}}, and throwing it away left callers with "the stream was
// truncated" and no idea why.
func parseConnectStream(data []byte) (messages, trailers []json.RawMessage) {
	for len(data) >= 5 {
		flags := data[0]
		length := binary.BigEndian.Uint32(data[1:5])
		data = data[5:]
		if uint32(len(data)) < length {
			break
		}
		payload := data[:length]
		data = data[length:]

		if flags&0x02 != 0 {
			trailers = append(trailers, json.RawMessage(payload))
			continue
		}
		messages = append(messages, json.RawMessage(payload))
	}
	return messages, trailers
}

// connectErrorFrom returns the server's own error text from an end-stream
// frame, if it sent one.
func connectErrorFrom(trailers []json.RawMessage) string {
	for _, raw := range trailers {
		var envelope struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error == nil {
			continue
		}
		if envelope.Error.Code != "" && envelope.Error.Message != "" {
			return envelope.Error.Code + ": " + envelope.Error.Message
		}
		if envelope.Error.Message != "" {
			return envelope.Error.Message
		}
		return string(raw)
	}
	return ""
}

// execStreamTruncatedError is an exec response that ended without its
// exit-status trailer. It is a distinct type so callers can decide whether the
// failure is worth retrying: a fresh sandbox can cut a stream once while it
// finishes booting, and a genuinely broken one cuts every stream.
type execStreamTruncatedError struct {
	detail string
}

func (e *execStreamTruncatedError) Error() string { return e.detail }

// snippet bounds a body for a log line or an error message.
func snippet(body []byte, limit int) string {
	s := strings.TrimSpace(string(body))
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

func (e *E2BExecutor) Exec(ctx context.Context, command string, timeout time.Duration) (string, error) {
	// Match Docker's behavior: default cwd is /workspace, not the
	// invoking user's $HOME. Without this, relative-path writes from
	// agent commands (e.g. `camoufox-cli screenshot out.png`) land in
	// /home/user/ and never make it to the host-visible workspace.
	// Hydrate creates /workspace before any agent Exec runs, and
	// recreate() re-hydrates after a sandbox replacement, so `cd` is
	// guaranteed to succeed here.
	wrapped := "cd /workspace && " + command
	observed := e.identSnapshot()
	result, err := e.execOn(ctx, observed, wrapped, timeout)
	if sandboxGone(err) {
		if rerr := e.recreateIfCurrent(ctx, observed); rerr != nil {
			return "", fmt.Errorf("sandbox recreate failed: %w (original: %v)", rerr, err)
		}
		return e.execOnce(ctx, wrapped, timeout)
	}
	return result, err
}

func (e *E2BExecutor) execOnce(ctx context.Context, command string, timeout time.Duration) (string, error) {
	return e.execOn(ctx, e.identSnapshot(), command, timeout)
}

// execOn runs one command against an explicit identity. Callers that may want
// to rebuild afterwards use this form so the identity they report as "the one
// that just died" is exactly the one the request went to.
func (e *E2BExecutor) execOn(ctx context.Context, id sandboxIdent, command string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"process": map[string]interface{}{
			"cmd":  "/bin/bash",
			"args": []string{"-c", command},
		},
	})

	enveloped := connectEnvelope(payload)

	// Per-request deadline: timeout (the user-supplied tool budget) plus
	// a 30s slack so the server has room to flush trailing frames before
	// our side gives up. Critical because the underlying http.Client now
	// has no global timeout — without this the request would hang
	// forever if envd died mid-stream.
	execCtx, cancelExec := context.WithTimeout(ctx, timeout+30*time.Second)
	defer cancelExec()

	reqURL := e.envdURLFor(id.id) + "/process.Process/Start"
	req, err := http.NewRequestWithContext(execCtx, "POST", reqURL, bytes.NewReader(enveloped))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Connect-Timeout-Ms", fmt.Sprintf("%d", int(timeout.Milliseconds())))
	if id.token != "" {
		req.Header.Set("X-Access-Token", id.token)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("e2b exec: %w", err)
	}
	defer resp.Body.Close()

	// Don't drop ReadAll's error — when the connection is severed
	// mid-stream (e.g. our deadline hit before the process finished
	// streaming back its output), this is the only signal we have that
	// the bytes we got are incomplete. Previously we silently kept the
	// partial body, the parser saw zero complete frames, and the tool
	// returned empty stdout — exactly the symptom we just hit with
	// long-running exec calls.
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return string(body), fmt.Errorf("e2b exec body read: %w (got %d bytes)", readErr, len(body))
	}

	if resp.StatusCode != http.StatusOK {
		return "", &sandboxHTTPError{op: "e2b exec", status: resp.StatusCode, body: string(body)}
	}

	// Parse Connect streaming response frames
	frames, trailers := parseConnectStream(body)
	connectErr := connectErrorFrom(trailers)

	var stdout, stderr strings.Builder
	exitCode := 0
	exited := false

	for _, frame := range frames {
		// E2B response format: {"event":{"data":{"stdout":"base64..."}}}
		var msg struct {
			Event struct {
				Start *struct {
					Pid int `json:"pid"`
				} `json:"start,omitempty"`
				Data *struct {
					Stdout string `json:"stdout,omitempty"` // base64 encoded
					Stderr string `json:"stderr,omitempty"` // base64 encoded
				} `json:"data,omitempty"`
				End *struct {
					Exited bool   `json:"exited"`
					Status string `json:"status"` // "exit status 0"
				} `json:"end,omitempty"`
			} `json:"event"`
		}
		if json.Unmarshal(frame, &msg) != nil {
			continue
		}
		if msg.Event.Data != nil {
			if msg.Event.Data.Stdout != "" {
				if decoded, err := base64.StdEncoding.DecodeString(msg.Event.Data.Stdout); err == nil {
					stdout.Write(decoded)
				}
			}
			if msg.Event.Data.Stderr != "" {
				if decoded, err := base64.StdEncoding.DecodeString(msg.Event.Data.Stderr); err == nil {
					stderr.Write(decoded)
				}
			}
		}
		if msg.Event.End != nil {
			exited = msg.Event.End.Exited
			// Parse "exit status N" to get exit code
			if strings.HasPrefix(msg.Event.End.Status, "exit status ") {
				fmt.Sscanf(msg.Event.End.Status, "exit status %d", &exitCode)
			}
		}
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}
	output = strings.TrimSpace(output)

	slog.Info("e2b exec completed", "sandboxID", id.id, "exitCode", exitCode, "exited", exited, "outputLen", len(output), "frames", len(frames), "trailers", len(trailers), "bodyBytes", len(body), "connectError", connectErr)

	// Reject a stream that didn't deliver a proper "End/exited=true" trailer.
	// Why this matters: when the request payload pushes envd past some
	// internal buffer (empirically >~80KB-of-base64 in a single bash -c arg
	// triggers it), the response comes back with frames but no final exit
	// status. exitCode stays at its zero default and we'd otherwise return
	// nil error — which is exactly the silent-failure mode that left Hydrate
	// looking successful while the chown step hadn't actually run, then
	// verifyWorkspaceWritable reported "/workspace probe: Permission denied"
	// with no clue why the chown didn't take.
	if !exited {
		if output == "" {
			output = "(no output — response stream truncated before exit-status trailer)"
		}
		// Say what the provider said. Without this the caller could not tell a
		// sandbox that vanished mid-exec from an envd error we failed to parse;
		// the trailer usually names it ("not found", "internal", …), and the raw
		// body is the only clue when the stream simply died.
		detail := fmt.Sprintf("e2b exec did not exit cleanly (frames=%d, trailers=%d, bodyBytes=%d): %s",
			len(frames), len(trailers), len(body), output)
		if connectErr != "" {
			detail += "; server error: " + connectErr
		}
		// Frames are already parsed JSON, so they read far better than the
		// framed bytes; the raw body is the fallback when nothing parsed (which
		// is itself the diagnosis: the stream died mid-frame).
		if len(frames) > 0 {
			if joined, err := json.Marshal(frames); err == nil {
				detail += "; frames=" + snippet(joined, 300)
			}
		} else if raw := snippet(body, 300); raw != "" {
			detail += "; raw=" + raw
		}
		return output, &execStreamTruncatedError{detail: detail}
	}

	if exitCode != 0 {
		if output == "" {
			output = fmt.Sprintf("Process exited with code %d", exitCode)
		}
		return output, fmt.Errorf("exit code %d", exitCode)
	}
	return output, nil
}

func (e *E2BExecutor) ReadFile(ctx context.Context, path string) (string, error) {
	observed := e.identSnapshot()
	result, err := e.readFileOn(ctx, observed, path)
	if sandboxGone(err) {
		if rerr := e.recreateIfCurrent(ctx, observed); rerr != nil {
			return "", rerr
		}
		return e.readFileOnce(ctx, path)
	}
	return result, err
}

func (e *E2BExecutor) readFileOnce(ctx context.Context, path string) (string, error) {
	return e.readFileOn(ctx, e.identSnapshot(), path)
}

func (e *E2BExecutor) readFileOn(ctx context.Context, id sandboxIdent, path string) (string, error) {
	reqURL := fmt.Sprintf("%s/files?path=%s&username=user", e.envdURLFor(id.id), url.QueryEscape(path))
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	if id.token != "" {
		req.Header.Set("X-Access-Token", id.token)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("e2b read: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", &sandboxHTTPError{op: "e2b read", status: resp.StatusCode, body: string(body)}
	}
	return string(body), nil
}

func (e *E2BExecutor) WriteFile(ctx context.Context, path, content string) (string, error) {
	observed := e.identSnapshot()
	result, err := e.writeFileOn(ctx, observed, path, content)
	if sandboxGone(err) {
		if rerr := e.recreateIfCurrent(ctx, observed); rerr != nil {
			return "", rerr
		}
		return e.writeFileOnce(ctx, path, content)
	}
	return result, err
}

func (e *E2BExecutor) writeFileOnce(ctx context.Context, filePath, content string) (string, error) {
	return e.writeFileOn(ctx, e.identSnapshot(), filePath, content)
}

func (e *E2BExecutor) writeFileOn(ctx context.Context, id sandboxIdent, filePath, content string) (string, error) {
	if err := e.uploadBytesOn(ctx, id, filePath, []byte(content)); err != nil {
		return "", err
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(content), filePath), nil
}

// uploadBytes POSTs `data` to envd's /files multipart endpoint at
// `sandboxPath`, owned by the `user` account exec runs as. Pulled out of
// writeFileOnce so Hydrate can ship its tar bundle through the same
// large-payload-safe path instead of inlining a base64 blob inside a
// `bash -c` arg (the latter empirically truncates the Connect response
// stream at ~80KB and leaves the sandbox half-hydrated; see the comment
// on the Hydrate caller).
func (e *E2BExecutor) uploadBytes(ctx context.Context, sandboxPath string, data []byte) error {
	return e.uploadBytesOn(ctx, e.identSnapshot(), sandboxPath, data)
}

func (e *E2BExecutor) uploadBytesOn(ctx context.Context, id sandboxIdent, sandboxPath string, data []byte) error {
	// E2B envd's POST /files expects multipart/form-data with a `file`
	// field, NOT a raw octet-stream body. The earlier raw-body version
	// returned 200 OK but silently dropped the upload, leaving the file
	// non-existent inside the sandbox — caught when uploaded skills
	// failed with "No such file or directory" at exec time.
	reqURL := fmt.Sprintf("%s/files?path=%s&username=user",
		e.envdURLFor(id.id), url.QueryEscape(sandboxPath))

	// envd: the destination path comes from the `path` query param;
	// the multipart `filename` is just metadata, so basename is fine.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", path.Base(sandboxPath))
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if id.token != "" {
		req.Header.Set("X-Access-Token", id.token)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("e2b upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return &sandboxHTTPError{op: "e2b upload", status: resp.StatusCode, body: string(body)}
	}
	return nil
}

func (e *E2BExecutor) ListDir(ctx context.Context, path string) (string, error) {
	// Use exec to list directory since the files API doesn't have a list endpoint
	return e.Exec(ctx, fmt.Sprintf("ls -la %s", path), 10*time.Second)
}

// IsRemoteWorkspace marks this executor as cloud-hosted so the
// LifecyclePool runs syncSnapshot after every exec instead of only on
// idle eviction. See sandbox.RemoteWorkspace.
func (e *E2BExecutor) IsRemoteWorkspace() {}

// ExposePort implements PortExposer. E2B serves every sandbox port at
// https://<port>-<sandboxID>.e2b.app with no publish step (same scheme
// envdURL uses for the control port), so the dev server bound to 0.0.0.0
// is reachable the moment it listens.
func (e *E2BExecutor) ExposePort(_ context.Context, port int) (string, error) {
	sandboxID := e.identSnapshot().id
	if sandboxID == "" {
		return "", fmt.Errorf("e2b: sandbox not created")
	}
	return fmt.Sprintf("https://%d-%s.e2b.app", port, sandboxID), nil
}

// ProvisionDir implements TemplateProvisioner: tar localDir (skipping the
// heavy, host-specific trees the scaffold reinstalls anyway), upload it
// over the same /files channel Hydrate uses, and extract it into destDir
// inside the sandbox. This seeds a coding template into an E2B sandbox
// that has no host bind mount to share it through.
func (e *E2BExecutor) ProvisionDir(ctx context.Context, localDir, destDir string) error {
	root := filepath.Clean(localDir)
	skip := map[string]bool{"node_modules": true, ".git": true, ".output": true, "dist": true, ".next": true}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	walkErr := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if skip[top] {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Symlinks (and other irregular files) can't be faithfully tarred
		// without resolving targets; skip them — templates are plain trees.
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			return nil
		}
		hdr, herr := tar.FileInfoHeader(fi, "")
		if herr != nil {
			return herr
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, oerr := os.Open(p)
			if oerr != nil {
				return oerr
			}
			_, cerr := io.Copy(tw, f)
			f.Close()
			if cerr != nil {
				return cerr
			}
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	const tmp = "/tmp/fc-template.tar.gz"
	if err := e.uploadBytes(ctx, tmp, buf.Bytes()); err != nil {
		return fmt.Errorf("upload template: %w", err)
	}
	cmd := fmt.Sprintf("mkdir -p %q && tar -C %q -xzf %s && rm -f %s", destDir, destDir, tmp, tmp)
	if out, err := e.Exec(ctx, cmd, 3*time.Minute); err != nil {
		return fmt.Errorf("extract template: %w: %s", err, out)
	}
	return nil
}

// SnapshotWorkspace tars /workspace and ships the bytes back as base64 over
// stdout. This is the inverse of Hydrate's tar+base64 push — used by the
// LifecyclePool to flush sandbox-side files back to the durable
// workspace.Store after every successful exec, so files that the skill
// wrote inside the sandbox (image-tool's /workspace/gen_xxx.webp etc.)
// end up reachable from the host's UI / signed URL paths.
//
// Returns map of /workspace-relative path → contents. Skips silently
// when /workspace is empty or doesn't exist.
func (e *E2BExecutor) SnapshotWorkspace(ctx context.Context) (map[string][]byte, error) {
	// `2>/dev/null` swallows the "tar: ./: directory not found" noise
	// when /workspace doesn't exist yet; we still want to proceed with
	// an empty result. base64 -w0 keeps output on a single line so
	// envd's frame parser doesn't fight whitespace folding. Falls back
	// to the empty tar if /workspace is missing entirely.
	cmd := "if [ -d /workspace ]; then " +
		"tar -czf - -C /workspace . 2>/dev/null | base64 -w0; " +
		"fi"
	out, err := e.execOnce(ctx, cmd, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("snapshot workspace exec: %w (output: %s)", err, out)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	gz, err := base64.StdEncoding.DecodeString(out)
	if err != nil {
		return nil, fmt.Errorf("snapshot workspace decode: %w", err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("snapshot workspace gunzip: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	out2 := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out2, fmt.Errorf("snapshot workspace tar read: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Tar names start with "./" because we tarred `.` from inside
		// /workspace; strip it so callers see the same agent-relative
		// path layout the workspace.Store uses.
		name := strings.TrimPrefix(hdr.Name, "./")
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}
		// Skip macOS resource forks (`._foo`) in case anyone ever runs
		// this against a BSD-tar template — Linux/E2B's GNU tar
		// doesn't emit these, but they'd otherwise pollute the store.
		base := name
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if strings.HasPrefix(base, "._") {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return out2, fmt.Errorf("snapshot workspace read entry %s: %w", name, err)
		}
		out2[name] = data
	}
	return out2, nil
}

// verifyWorkspaceWritable runs a one-shot probe against /workspace as
// the same `user` account agent exec runs under. It catches the
// silent-failure mode where Hydrate appeared to succeed but the
// underlying chown didn't actually take — empirically observed when
// the e2b template ships /workspace owned by root and the chown step
// is skipped or sudoless. The probe writes a single byte and removes
// it; the touch+rm round-trip is the same operation pattern the agent
// uses on its first /workspace write, so anything that would fail
// there fails here too.
func verifyWorkspaceWritable(ctx context.Context, ex *E2BExecutor) error {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	const probeCmd = `touch /workspace/.fc-health && rm -f /workspace/.fc-health && echo ok`
	out, err := ex.execOnce(probeCtx, probeCmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("/workspace probe: %w (out=%s)", err, strings.TrimSpace(out))
	}
	if !strings.Contains(out, "ok") {
		return fmt.Errorf("/workspace not writable as exec user (out=%s)", strings.TrimSpace(out))
	}
	return nil
}

func (e *E2BExecutor) Close() error {
	return e.closeSandboxByID(e.identSnapshot().id)
}

// closeSandboxByID destroys one instance. Pulled out of Close so a failed
// rebuild can destroy the replacement it is abandoning by id, without having
// to pretend the executor's current identity switched to it.
//
// The answer is checked rather than assumed. Reporting success on a rejected
// DELETE is how a "released" sandbox keeps running: its lease row is already
// gone, so nothing points at it any more and nothing will ever close it. A 404
// is success — the instance is not running, which is the whole point.
//
// The call is bounded because one caller (Get's post-create failure path) runs
// it while holding the pool mutex: an unanswered DELETE must not stall every
// other agent's sandbox binding.
func (e *E2BExecutor) closeSandboxByID(sandboxID string) error {
	if e.closeSandboxFn != nil {
		return e.closeSandboxFn(sandboxID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE",
		fmt.Sprintf("%s/sandboxes/%s", e2bBaseURL, sandboxID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", e.apiKey)
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("e2b close sandbox %s: %w", sandboxID, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		// Already reaped — by the provider's timeout, or a sibling pod that
		// won the destroy race. Either way it is not running.
		slog.Info("e2b sandbox already gone", "sandboxID", sandboxID)
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("e2b close sandbox %s: HTTP %d: %s", sandboxID, resp.StatusCode, string(body))
	}
	slog.Info("e2b sandbox closed", "sandboxID", sandboxID)
	return nil
}

// Backend returns "e2b" — used by the per-exec log line so operators can
// confirm at a glance which provider handled a given tool call.
func (e *E2BExecutor) Backend() string { return "e2b" }

// Backend on the pool mirrors E2BExecutor.Backend so the LifecyclePool can
// surface the provider identity without resolving a lazy executor.
func (p *E2BExecutorPool) Backend() string { return "e2b" }

// E2BExecutorPool manages per-user E2B sandboxes.
type E2BExecutorPool struct {
	mu        sync.Mutex
	executors map[string]*E2BExecutor
	// leaseEpochs mirrors executors: the fencing epoch this pod last
	// received for the scope. Destroy passes it to ReleaseSandboxLease so a
	// stale eviction can never win against a newer renew/adopt.
	leaseEpochs map[string]int64
	// scopeLocks serialize the whole Get/Release path per scope, striped by
	// key (see scopeLock). Striped rather than one mutex per key: a map of
	// locks needs refcounting to avoid leaking an entry per session ever
	// served, and two scopes sharing a stripe only queue behind each other —
	// they never break each other's correctness.
	scopeLocks [scopeLockStripes]sync.Mutex
	apiKey     string
	template   string
	timeout    time.Duration
	home       string          // workspace root used to resolve per-agent skill dirs
	workspace  workspace.Store // optional — when set, /workspace is hydrated alongside /skills

	// Cross-pod lease coordination. When set, Get() first consults the
	// shared store and adopts an existing sandbox for the scope instead of
	// creating a duplicate per pod; Release() only destroys a sandbox it
	// still owns. Nil keeps the historical per-pod behavior (local mode,
	// docker, or registry disabled).
	leaseStore SandboxLeaseStore
	ownerID    string
	leaseTTL   time.Duration

	// Test seams: production defaults call the real e2b API; unit tests
	// override them so the create/adopt/reconcile decision paths can be
	// exercised without network access.
	newSandboxExecutor func(ctx context.Context, apiKey, template string, timeout time.Duration) (*E2BExecutor, error)
	hydrateSandbox     func(ctx context.Context, ex *E2BExecutor) error
	verifySandbox      func(ctx context.Context, ex *E2BExecutor) error
	warmupSandbox      func(ctx context.Context, ex *E2BExecutor)
}

// E2BLeaseOptions configures cross-pod sandbox sharing for the E2B pool.
type E2BLeaseOptions struct {
	Store    SandboxLeaseStore
	Owner    string
	LeaseTTL time.Duration
}

// WithSandboxLeases attaches a shared lease store. Requires non-empty Owner
// (unique per pod); TTL zero falls back to DefaultSandboxLeaseTTL.
func WithSandboxLeases(o E2BLeaseOptions) func(*E2BExecutorPool) {
	return func(p *E2BExecutorPool) {
		if o.Store == nil || o.Owner == "" {
			return
		}
		p.leaseStore = o.Store
		p.ownerID = o.Owner
		p.leaseTTL = o.LeaseTTL
		if p.leaseTTL <= 0 {
			p.leaseTTL = DefaultSandboxLeaseTTL
		}
	}
}

// newAdoptedE2BExecutor wraps an existing e2b sandbox (created by another
// pod) without calling the create API. Hydration is skipped on adoption —
// the sandbox was hydrated by its creator with the same (user, agent,
// session) skills/workspace; skill or workspace changes take effect on the
// next recreate, matching single-pod behavior.
//
// apiKey is required even though adoption never calls create: the shared
// lease row deliberately carries only sandbox_id + envd_token (the
// account-level key never touches the DB), and exec/read/write authenticate
// with the envd token alone — so an adopted executor "works" until the
// sandbox idles out. recreate() then needs the account key to mint a
// replacement, and Close() needs it to destroy one. An adopted executor
// without the key therefore fails at exactly that point, and e2b reports it
// as `401 authorization header is missing` (an empty X-API-Key header).
// Passing it at construction keeps "every E2BExecutor can rebuild/destroy
// itself" an invariant instead of a property callers must remember to patch
// in afterwards.
func newAdoptedE2BExecutor(apiKey, sandboxID, accessToken, template string, timeout time.Duration) *E2BExecutor {
	if template == "" {
		template = "base"
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return &E2BExecutor{
		apiKey:   apiKey,
		ident:    sandboxIdent{id: sandboxID, token: accessToken},
		client:   &http.Client{},
		template: template,
		timeout:  timeout,
		createFn: newE2BExecutor,
	}
}

// NewE2BExecutorPool — `home` is the FASTAGENT_HOME the docker backend
// would have used for `-v` mounts; the pool uses it to resolve which
// skill dirs to push into each fresh sandbox.
//
// Locking: p.mu guards only the executor/epoch maps — every critical section
// on it is a map lookup or assignment. Provisioning work (lease reads and
// writes, create, hydrate, verify, warmup) runs under the per-scope lock
// returned by scopeLock, so a slow or cold start for one scope cannot stall
// sandbox binding for every other agent in the process.
func NewE2BExecutorPool(apiKey, template, home string, timeout time.Duration, opts ...func(*E2BExecutorPool)) *E2BExecutorPool {
	p := &E2BExecutorPool{
		executors:          make(map[string]*E2BExecutor),
		leaseEpochs:        make(map[string]int64),
		apiKey:             apiKey,
		template:           template,
		timeout:            timeout,
		home:               home,
		newSandboxExecutor: newE2BExecutor,
		hydrateSandbox: func(ctx context.Context, ex *E2BExecutor) error {
			return ex.Hydrate(ctx)
		},
		verifySandbox: verifyWorkspaceWritable,
		warmupSandbox: warmupCamoufoxDaemon,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// SetWorkspace plugs in the workspace.Store whose contents should be
// mirrored to /workspace inside every fresh sandbox. Optional — when
// nil, only /skills is hydrated. Called by the gateway after
// LifecyclePool's own workspace is wired so the inner pool and the
// lifecycle layer see the same source of truth.
func (p *E2BExecutorPool) SetWorkspace(ws workspace.Store) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workspace = ws
}

// scopeLockStripes is the number of per-scope locks. Sized so unrelated scopes
// rarely collide while the array stays cheap to hold for the pool's lifetime.
const scopeLockStripes = 64

// scopeLock returns the lock that serializes work for one scope. Same key →
// same lock, always, which is what keeps "exactly one sandbox per scope" true
// now that the lock is per-scope instead of process-wide.
func (p *E2BExecutorPool) scopeLock(key string) *sync.Mutex {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(key))
	return &p.scopeLocks[hasher.Sum32()%scopeLockStripes]
}

// cachedExecutor / registerExecutor / recordEpoch / takeExecutor are the only
// ways the maps are touched. Keeping them tiny is the point: p.mu must never be
// held across I/O.
func (p *E2BExecutorPool) cachedExecutor(key string) (*E2BExecutor, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ex, ok := p.executors[key]
	return ex, ok
}

func (p *E2BExecutorPool) registerExecutor(key string, ex *E2BExecutor) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.executors[key] = ex
}

func (p *E2BExecutorPool) recordEpoch(key string, epoch int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.leaseEpochs[key] = epoch
}

// takeExecutor removes and returns the scope's executor together with the
// epoch this pod last received for it.
func (p *E2BExecutorPool) takeExecutor(key string) (*E2BExecutor, int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ex, ok := p.executors[key]
	if !ok {
		return nil, 0, false
	}
	epoch := p.leaseEpochs[key]
	delete(p.executors, key)
	delete(p.leaseEpochs, key)
	return ex, epoch, true
}

func (p *E2BExecutorPool) Get(ctx context.Context, agentID, projectID, sessionID string) (Executor, error) {
	key := poolKey(agentID, projectID, sessionID)

	// Serialize per scope, not process-wide. Everything below can do network
	// I/O — lease reads/writes, create, hydrate, verify, warmup (bounded at
	// 120s) — and a process-wide lock across that delays sandbox binding for
	// every other agent behind one cold scope. Same-scope callers still queue
	// here, so "one sandbox per scope" keeps the guarantee the global lock
	// used to provide as a side effect.
	scope := p.scopeLock(key)
	scope.Lock()
	defer scope.Unlock()

	if ex, ok := p.cachedExecutor(key); ok {
		if p.leaseStore != nil {
			// A cached executor may be stale: its lease can expire while we
			// were idle and another pod can take over the scope with a
			// different sandbox. Reconcile against the shared lease before
			// blindly renewing (blind renewal would steal ownership of the
			// wrong sandbox and orphan the real one on eviction).
			return p.reconcileLocalLease(ctx, key, ex, agentID, projectID, sessionID)
		}
		return ex, nil
	}
	if p.leaseStore != nil {
		if rec, err := p.leaseStore.GetSandboxLease(ctx, key); err != nil {
			slog.Warn("e2b lease lookup failed (falling back to local create)", "scopeKey", key, "error", err)
		} else if rec != nil {
			if ex, ok := p.adoptFromLease(ctx, key, rec, agentID, projectID, sessionID); ok {
				slog.Info("e2b sandbox adopted from shared lease",
					"sandboxID", rec.SandboxID, "scopeKey", key, "owner", p.ownerID)
				return ex, nil
			}
			slog.Warn("e2b lease changed during adoption (falling back to local create)",
				"scopeKey", key, "owner", p.ownerID)
		}
	}
	ex, err := p.newSandboxExecutor(ctx, p.apiKey, p.template, p.timeout)
	if err != nil {
		return nil, err
	}
	ex.SetHydrationSources(skillDirsForAgent(p.home, agentID), p.workspace, agentID, projectID, sessionID)
	if err := p.hydrateSandbox(ctx, ex); err != nil {
		// Hydrate is what chowns /workspace to the non-root `user`
		// account exec runs as; without it every agent write to
		// /workspace gets Permission denied silently — see the
		// reproducer in e2b_probe_diag_test.go. Used to be a WARN
		// here; the sandbox would still get cached and every
		// subsequent turn would surface as "agent wrote files but
		// they vanished". Tear it down so the caller retries or
		// fails loudly.
		_ = ex.Close()
		return nil, fmt.Errorf("e2b hydrate: %w", err)
	}
	if err := p.verifySandbox(ctx, ex); err != nil {
		_ = ex.Close()
		return nil, fmt.Errorf("e2b sandbox unusable: %w", err)
	}
	p.warmupSandbox(ctx, ex)
	if p.leaseStore != nil {
		created := ex.identSnapshot()
		rec, acquired, lerr := p.leaseStore.AcquireSandboxLease(
			ctx, key, p.ownerID, created.id, created.token, p.template, p.leaseTTL)
		if lerr != nil {
			slog.Warn("e2b lease acquire failed (keeping local sandbox)", "scopeKey", key, "error", lerr)
		} else if acquired && rec != nil {
			p.recordEpoch(key, rec.Epoch)
		} else if !acquired && rec != nil {
			// Another replica won the race for this scope; use its sandbox
			// when the CAS adoption succeeds, otherwise keep our own.
			if adopted, ok := p.adoptFromLease(ctx, key, rec, agentID, projectID, sessionID); ok {
				_ = ex.Close()
				ex = adopted
				slog.Info("e2b sandbox adopted after lease race",
					"sandboxID", rec.SandboxID, "scopeKey", key, "owner", p.ownerID)
			} else {
				slog.Warn("e2b adoption after race failed; keeping local sandbox unregistered",
					"scopeKey", key, "owner", p.ownerID)
			}
		}
	}
	p.registerExecutor(key, ex)
	return ex, nil
}

// adoptFromLease builds an executor for an existing lease record and
// CAS-renews it under this pod's ownership (fencing epoch bumped). Returns
// ok=false when the row changed between lookup and renew — callers must not
// adopt in that case. On a registry error the executor is returned without a
// recorded epoch (fail-open; release will not destroy the sandbox).
// Hydration is not replayed: the creating pod hydrated the same scope.
//
// Callers hold the scope lock; the maps are still guarded by p.mu inside the
// accessors, so this must not be called with p.mu held.
func (p *E2BExecutorPool) adoptFromLease(
	ctx context.Context,
	key string,
	rec *SandboxLeaseRecord,
	agentID, projectID, sessionID string,
) (*E2BExecutor, bool) {
	ex := newAdoptedE2BExecutor(p.apiKey, rec.SandboxID, rec.EnvdToken, rec.Template, p.timeout)
	if rec.Template == "" {
		ex.template = p.template
	}
	ex.SetHydrationSources(skillDirsForAgent(p.home, agentID), p.workspace, agentID, projectID, sessionID)
	epoch, err := p.leaseStore.RenewSandboxLease(ctx, key, p.ownerID, rec.SandboxID, p.leaseTTL)
	if err != nil {
		slog.Warn("e2b lease renew after adopt failed; adopting without epoch",
			"scopeKey", key, "owner", p.ownerID, "error", err)
		p.registerExecutor(key, ex)
		return ex, true
	}
	if epoch == 0 {
		return nil, false
	}
	p.recordEpoch(key, epoch)
	p.registerExecutor(key, ex)
	return ex, true
}

// reconcileLocalLease checks the shared lease against a locally cached
// executor before every use:
//
//   - same sandbox → renew (owner = this pod) and keep the local executor;
//   - no valid lease → re-claim with our local sandbox; on a lost race adopt
//     the winner;
//   - different sandbox owns the scope → close our stale local instance and
//     adopt the current one.
//
// Without the sandboxID check a long-idle pod would renew ownership of a
// lease that now points at another pod's sandbox, then "own" the wrong row
// and orphan the live sandbox when it later evicts.
//
// Callers hold the scope lock; this must not be called with p.mu held.
func (p *E2BExecutorPool) reconcileLocalLease(
	ctx context.Context,
	key string,
	ex *E2BExecutor,
	agentID, projectID, sessionID string,
) (Executor, error) {
	rec, err := p.leaseStore.GetSandboxLease(ctx, key)
	if err != nil {
		// Registry unavailable: fail open on the local executor.
		slog.Warn("e2b lease lookup failed (keeping local executor)", "scopeKey", key, "error", err)
		return ex, nil
	}
	cur := ex.identSnapshot()
	if rec == nil {
		// Our lease expired while idle. Try to reclaim with the sandbox we
		// still hold; if another pod won in the meantime, adopt theirs.
		got, acquired, aerr := p.leaseStore.AcquireSandboxLease(
			ctx, key, p.ownerID, cur.id, cur.token, ex.template, p.leaseTTL)
		if aerr != nil {
			slog.Warn("e2b lease reclaim failed (keeping local executor)", "scopeKey", key, "error", aerr)
			return ex, nil
		}
		if acquired {
			if got != nil {
				p.recordEpoch(key, got.Epoch)
			}
			// The re-acquire stamped this executor's CURRENT sandbox onto the
			// row, so anything a rebuild left unpublished is published now.
			ex.clearRebuild(cur)
			return ex, nil
		}
		if got != nil {
			if adopted, ok := p.adoptFromLease(ctx, key, got, agentID, projectID, sessionID); ok {
				_ = ex.Close()
				slog.Info("e2b sandbox adopted after lease expiry race",
					"sandboxID", got.SandboxID, "scopeKey", key, "owner", p.ownerID)
				return adopted, nil
			}
			slog.Warn("e2b adoption after expiry race failed; keeping local executor",
				"scopeKey", key, "owner", p.ownerID)
			return ex, nil
		}
		return ex, nil
	}
	if rec.SandboxID == cur.id {
		epoch, err := p.leaseStore.RenewSandboxLease(ctx, key, p.ownerID, cur.id, p.leaseTTL)
		if err != nil {
			slog.Warn("e2b lease renew failed", "scopeKey", key, "owner", p.ownerID, "error", err)
		} else if epoch > 0 {
			p.recordEpoch(key, epoch)
		}
		return ex, nil
	}
	if pending, ok := ex.pendingPublish(); ok {
		// This executor replaced its own sandbox, so the row is behind by
		// construction. Move it onto the replacement instead of treating the
		// mismatch as a takeover — adopting back would close the healthy
		// replacement and hand back the instance that just died.
		epoch, rerr := p.leaseStore.ReplaceSandboxLease(
			ctx, key, p.ownerID, pending.id, pending.token, ex.template, p.leaseTTL)
		if rerr != nil {
			// Registry down: keep serving from the local sandbox and retry on
			// the next reconcile (same fail-open rule as every other op).
			slog.Warn("e2b rebuilt sandbox not published to lease; keeping local executor",
				"scopeKey", key, "owner", p.ownerID, "sandboxID", pending.id, "error", rerr)
			return ex, nil
		}
		if epoch > 0 {
			p.recordEpoch(key, epoch)
			if !ex.clearRebuild(pending) {
				slog.Info("e2b rebuild superseded mid-publish; the newer identity publishes next",
					"scopeKey", key, "publishedSandboxID", pending.id)
			}
			slog.Info("e2b rebuilt sandbox published to shared lease",
				"scopeKey", key, "owner", p.ownerID, "sandboxID", pending.id, "epoch", epoch)
			return ex, nil
		}
		// CAS missed: another replica owns the scope now. Fall through and
		// adopt its sandbox — our replacement is closed by the adopt path.
		slog.Info("e2b rebuilt sandbox superseded by another pod; adopting current lease",
			"scopeKey", key, "leaseSandboxID", rec.SandboxID, "localSandboxID", pending.id)
	}
	// Scope was taken over by a different sandbox. Our cached instance is
	// stale; adopt the current one and only then close our local instance.
	if adopted, ok := p.adoptFromLease(ctx, key, rec, agentID, projectID, sessionID); ok {
		_ = ex.Close()
		slog.Info("e2b sandbox adopted (local cache stale)",
			"sandboxID", rec.SandboxID, "scopeKey", key, "owner", p.ownerID)
		return adopted, nil
	}
	slog.Warn("e2b adoption of current lease failed; keeping stale local executor",
		"scopeKey", key, "owner", p.ownerID)
	return ex, nil
}

// warmupCamoufoxDaemon spawns the camoufox-cli background daemon as part
// of sandbox provisioning so the agent's first `camoufox-cli open` call
// attaches to a live daemon instead of racing to spawn one. Without this,
// the CLI's auto-spawn path waits only 5 seconds for the daemon socket
// — empirically too short in a fresh e2b sandbox (Python startup +
// Firefox handshake routinely take longer), and concurrent execs inside
// the same sandbox can both try to spawn, surfacing as the user-visible
// "Daemon did not start within 5 seconds" failure.
//
// Best-effort: any error is logged at WARN and sandbox creation still
// succeeds — agents that never touch the browser shouldn't fail because
// camoufox couldn't come up, and the original (slow) cold-start path
// remains intact as a fallback. Bounded to 120s so a wedged camoufox
// install can't pin sandbox creation indefinitely.
func warmupCamoufoxDaemon(ctx context.Context, ex *E2BExecutor) {
	warmCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	// `open about:blank` is the cheapest invocation that starts the
	// daemon: no network, no GeoIP lookup, no real page load. The proxy
	// shim in the Dockerfile still applies — if HTTPS_PROXY is set the
	// browser launches with --proxy, matching what the agent's later
	// calls will see, so the warmed daemon is configured identically.
	out, err := ex.execOnce(warmCtx, "cd /workspace && camoufox-cli open about:blank", 120*time.Second)
	if err != nil {
		slog.Warn("e2b camoufox warmup failed (first browser call will pay cold-start)",
			"sandboxID", ex.identSnapshot().id, "error", err, "out", strings.TrimSpace(out))
		return
	}
	slog.Info("e2b camoufox daemon warmed", "sandboxID", ex.identSnapshot().id)
}

func (p *E2BExecutorPool) Release(agentID, projectID, sessionID string) error {
	key := poolKey(agentID, projectID, sessionID)

	// Same scope lock as Get: without it a Get provisioning this scope could
	// register a fresh executor just after we drained the maps, and that
	// sandbox would never be released — its lease would lapse while the
	// instance kept running.
	scope := p.scopeLock(key)
	scope.Lock()
	defer scope.Unlock()

	ex, epoch, ok := p.takeExecutor(key)
	if !ok {
		return nil
	}
	return p.releaseExecutor(key, ex, epoch)
}

func (p *E2BExecutorPool) CloseAll() {
	// Shutdown path: the maps are drained under p.mu and the per-scope locks
	// are deliberately not taken. A Get racing shutdown can still register an
	// executor after the drain; that is the pre-existing "in-flight work dies
	// with the process" behavior, not a new hazard.
	p.mu.Lock()
	execs := make([]struct {
		key   string
		ex    *E2BExecutor
		epoch int64
	}, 0, len(p.executors))
	for key, ex := range p.executors {
		execs = append(execs, struct {
			key   string
			ex    *E2BExecutor
			epoch int64
		}{key: key, ex: ex, epoch: p.leaseEpochs[key]})
	}
	p.executors = make(map[string]*E2BExecutor)
	p.leaseEpochs = make(map[string]int64)
	p.mu.Unlock()
	for _, e := range execs {
		_ = p.releaseExecutor(e.key, e.ex, e.epoch)
	}
}

// releaseExecutor drops the shared lease when this pod still owns it, and
// only destroys the sandbox when the lease deletion succeeded (or no lease
// store is configured). If another pod adopted the scope, we drop our local
// reference without closing the sandbox out from under it.
func (p *E2BExecutorPool) releaseExecutor(key string, ex *E2BExecutor, epoch int64) error {
	if p.leaseStore == nil {
		return ex.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	deleted, err := p.leaseStore.ReleaseSandboxLease(ctx, key, p.ownerID, epoch)
	if err != nil {
		// Registry unavailable: keep the sandbox alive (fail open) rather
		// than risk destroying an instance another pod just adopted.
		slog.Warn("e2b lease release failed; leaving sandbox alive", "scopeKey", key, "error", err)
		return nil
	}
	if !deleted {
		return nil // another owner holds the lease — do not close shared sandbox
	}
	return ex.Close()
}

var (
	_ Executor             = (*E2BExecutor)(nil)
	_ ExecutorPool         = (*E2BExecutorPool)(nil)
	_ WorkspaceSnapshotter = (*E2BExecutor)(nil)
	_ RemoteWorkspace      = (*E2BExecutor)(nil)
)
