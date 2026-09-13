package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Reaping the instances no lease row names.
//
// The problem this closes: e2b never kills a paused sandbox on its own, and
// paused sandboxes are unbilled, outside the concurrency limit and kept
// indefinitely. The pool pauses rather than destroys on purpose (that is what
// makes the next call a resume instead of a rebuild), but every instance whose
// scope nobody will ever use again — a crashed pod's sandbox, a scope whose row
// was replaced, a rotation that made a row unreadable — then stays in the
// account forever. Not billed is not the same as not accumulating.
//
// The pool is the only component that can decide, and the decision needs three
// facts from two places, so it lives here rather than in the lifecycle layer:
//
//	provider says paused                 — the authoritative lifecycle
//	metadata says this deployment made it — ownership that survives pausing
//	no row still claims it                — the naming authority's answer
//
// Anything unknown means "do not reap": a wrong reap destroys state, a missed
// reap only costs one more pass.

// Lifecycle values as the provider spells them (ListedSandbox.state).
const e2bStatePaused = "paused"

// e2bPoolTagKey is the metadata key carrying the deployment's pool tag.
const e2bPoolTagKey = "fastagent_pool"

// List paging: the provider caps a page at 100 and hands back the cursor for
// the next one in a header. e2bMaxListPages bounds a runaway loop (a provider
// that keeps answering with a fresh cursor); whatever lies beyond it stays
// unreaped, which is a leak rather than damage.
const (
	e2bListPageLimit = 100
	e2bMaxListPages  = 20
)

// e2bSandboxInfo is the projection of a listed sandbox the reaper needs: which
// instance, whether the provider says it is paused, and the metadata that
// proves which deployment created it.
type e2bSandboxInfo struct {
	SandboxID string
	State     string
	Metadata  map[string]string
}

// listE2BSandboxes enumerates the project's sandboxes in one lifecycle state,
// following the pagination cursor to the end.
//
// state is a filter, never a guarantee: ReapOrphans re-checks the state of
// every sandbox it is about to destroy, so a provider that ignores the
// parameter (the v1 endpoint lists "running" sandboxes and the v2 filter is new)
// cannot turn this into a destroy of something live.
func listE2BSandboxes(ctx context.Context, client *http.Client, apiKey, state string) ([]e2bSandboxInfo, error) {
	var out []e2bSandboxInfo
	next := ""
	for page := 0; page < e2bMaxListPages; page++ {
		q := url.Values{}
		if state != "" {
			q.Set("state", state)
		}
		q.Set("limit", strconv.Itoa(e2bListPageLimit))
		if next != "" {
			q.Set("nextToken", next)
		}
		req, err := http.NewRequestWithContext(ctx, "GET",
			e2bBaseURL+"/v2/sandboxes?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-API-Key", apiKey)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("e2b list sandboxes: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("e2b list sandboxes: read body: %w", readErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("e2b list sandboxes: HTTP %d: %s", resp.StatusCode, string(body))
		}
		var listed []struct {
			SandboxID string            `json:"sandboxID"`
			State     string            `json:"state"`
			Metadata  map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal(body, &listed); err != nil {
			return nil, fmt.Errorf("e2b list sandboxes: parse response: %w", err)
		}
		for _, s := range listed {
			out = append(out, e2bSandboxInfo{
				SandboxID: s.SandboxID,
				State:     s.State,
				Metadata:  s.Metadata,
			})
		}
		if next = resp.Header.Get("X-Next-Token"); next == "" {
			return out, nil
		}
	}
	slog.Warn("e2b sandbox listing hit its page cap",
		"pages", e2bMaxListPages, "state", state, "listed", len(out))
	return out, nil
}

// destroyE2BSandbox deletes one instance by id without ever pointing an
// executor at it. The delete — and the rule that a 404 means "already gone, and
// that is success" — lives in closeSandboxByID, so a throwaway adopted executor
// is the cheapest way to reuse it rather than duplicate the call and its
// classification here.
func destroyE2BSandbox(_ context.Context, apiKey, sandboxID string) error {
	return newAdoptedE2BExecutor(apiKey, sandboxID, "", "", 0).closeSandboxByID(sandboxID)
}

// createMetadata is the metadata attached to every sandbox this pool creates.
// Nil (not an empty map) when no tag is configured, so the create body stays
// byte-identical to what it was before reaping existed.
func (p *E2BExecutorPool) createMetadata() map[string]string {
	if p.poolTag == "" {
		return nil
	}
	return map[string]string{e2bPoolTagKey: p.poolTag}
}

// reapGrace is how long a lapsed lease still protects the instance it names.
//
// Two lease TTLs: the row is valid for TTL after the last renew, and one more
// TTL covers a lease that lapsed while the pod holding it was between writes.
// Beyond that the instance is unreachable by construction — every read path
// treats an expired row as absent, so nothing can resume or adopt it — which is
// what makes it an orphan rather than a cache entry.
func (p *E2BExecutorPool) reapGrace() time.Duration {
	return 2 * p.leaseTTL
}

// ReapOrphans destroys paused instances that this deployment created and no
// lease row still claims. Returns how many are gone as a result (an instance
// that turns out to be already gone counts: it is out of the account either
// way) and a joined error naming the instances whose delete failed — one
// failure never stops the pass.
//
// Implements the lifecycle layer's OrphanReaper port. Runs on its own slow
// clock (see LifecyclePool.loop); the caller holds no scope lock, because the
// pass touches scopes it does not own and every scope it does own is protected
// by its row.
func (p *E2BExecutorPool) ReapOrphans(ctx context.Context) (int, error) {
	if p.leaseStore == nil || p.poolTag == "" {
		// No shared naming authority, or no way to prove ownership: there is no
		// safe set to reap.
		return 0, nil
	}
	if p.listSandboxes == nil || p.destroySandbox == nil {
		return 0, nil
	}
	instances, err := p.listSandboxes(ctx, p.apiKey, e2bStatePaused)
	if err != nil {
		return 0, err
	}
	refs, err := p.leaseStore.ListSandboxLeaseRefs(ctx)
	if err != nil {
		// Fail closed. Without the naming authority's answer every paused
		// instance looks unreferenced, and guessing wrong here destroys a
		// sandbox another pod is about to resume.
		return 0, fmt.Errorf("list lease refs: %w", err)
	}
	cutoff := time.Now().Add(-p.reapGrace()).Unix()
	claimed := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.ExpiresAt > cutoff {
			claimed[ref.SandboxID] = struct{}{}
		}
	}

	reaped := 0
	var failures []error
	for _, in := range instances {
		if in.State != e2bStatePaused {
			// The provider filter is an optimization, not the rule: a sandbox
			// that is still running may have work inside it.
			continue
		}
		if in.Metadata[e2bPoolTagKey] != p.poolTag {
			// Created by another deployment (or before tagging existed) inside
			// the same account. Not ours to destroy.
			continue
		}
		if _, ok := claimed[in.SandboxID]; ok {
			continue
		}
		if err := p.destroySandbox(ctx, p.apiKey, in.SandboxID); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", in.SandboxID, err))
			continue
		}
		reaped++
		slog.Info("e2b orphan sandbox reaped",
			"sandboxID", in.SandboxID, "poolTag", p.poolTag)
	}
	// One line per pass, and the only way an operator can tell "reaping works
	// and found nothing" from "the provider returned nothing at all".
	slog.Info("e2b orphan reap pass",
		"listed", len(instances), "claimed", len(claimed), "reaped", reaped)
	return reaped, errors.Join(failures...)
}

var _ OrphanReaper = (*E2BExecutorPool)(nil)
