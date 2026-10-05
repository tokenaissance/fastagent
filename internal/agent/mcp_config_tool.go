package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// mcpServersFromStore returns the agent's declared MCP servers exactly as the
// store holds them right now, or (nil, false) when the store cannot be read
// (no relational store on this deployment, or a read error). The per-key
// `agent_mcp_servers` table is AUTHORITATIVE for rc.MCPServers — the gateway
// assembles the resolved config that way ("Table stays authoritative even if a
// legacy JSON still carries a stale mcpServers object", gateway.go) — so a
// successful read is the whole truth for this question, including the ABSENCE
// of a server.
func mcpServersFromStore(ctx context.Context, ag *Agent, agentID string) (map[string]config.MCPServerConfig, bool) {
	if ag == nil || ag.dataStore == nil {
		return nil, false
	}
	servers, err := ag.dataStore.ListMCPServers(ctx, agentID)
	if err != nil {
		slog.Warn("mcp: could not read the server table; falling back to this session's snapshot",
			"agent", agentID, "error", err)
		return nil, false
	}
	return servers, true
}

// resolveMCPServer resolves one server for the OAuth control plane
// (login / status / check / refresh / logout), in this order:
//
//  1. the STORE table — the row `mcp add` just wrote. This is the fix for the
//     2026-10-05 report: add writes the row and only QUEUES the reload that
//     would fold it into rc.MCPServers on the next build, so the very next
//     command in the same session — `mcp login notion` — answered "not a
//     configured OAuth MCP server" and the owner had to wait a turn (or open a
//     fresh chat) before a link could exist. The OAuth actions do not need a
//     rebuilt snapshot; they need the row, and the row exists.
//  2. a name this session REMOVED — refused immediately. Without this the rc
//     fallback below would keep resolving a server the owner just unregistered
//     (the row is gone; the snapshot still names it).
//  3. rc.MCPServers — the build-time snapshot. It is not only "the store as of
//     the last build": an agent's file config can declare servers that never
//     lived in the table, and those must keep working. The table wins where it
//     speaks; the snapshot answers where the table is silent.
func resolveMCPServer(ctx context.Context, ag *Agent, rc config.ResolvedAgent, serverName string) (config.MCPServerConfig, bool) {
	if servers, ok := mcpServersFromStore(ctx, ag, rc.ID); ok {
		if cfg, found := servers[serverName]; found {
			return cfg, true
		}
		if ag != nil {
			if _, removed := ag.mcpRemovedThisSession.Load(serverName); removed {
				return config.MCPServerConfig{}, false
			}
		}
	}
	cfg, found := rc.MCPServers[serverName]
	return cfg, found
}

// resolveMCPServers is resolveMCPServer for "list them all" (`mcp status` with
// no serverName): the snapshot (file-config declarations included), overlaid
// with the store's rows, minus anything this session removed.
func resolveMCPServers(ctx context.Context, ag *Agent, rc config.ResolvedAgent) map[string]config.MCPServerConfig {
	out := make(map[string]config.MCPServerConfig, len(rc.MCPServers))
	for name, cfg := range rc.MCPServers {
		out[name] = cfg
	}
	if servers, ok := mcpServersFromStore(ctx, ag, rc.ID); ok {
		for name, cfg := range servers {
			out[name] = cfg
		}
		if ag != nil {
			ag.mcpRemovedThisSession.Range(func(key, _ any) bool {
				if name, isString := key.(string); isString {
					delete(out, name)
				}
				return true
			})
		}
	}
	return out
}

// mcpAddInput carries the declarative server fields for `mcp add`.
// Mirrors config.MCPServerConfig so the tool stays a thin adapter over
// the agent's per-server store — no parallel domain model.
type mcpAddInput struct {
	ServerName    string
	URL           string
	OAuthResource string
	Scopes        []string
}

// mcpUndoMarker prefixes the machine-readable inverse payload appended to
// successful add/remove tool results. The agent loop persists tool results
// into session_events (type='tool_result'), so this marker turns the event
// trace into a durable undo journal: `mcp undo` parses it and replays the
// inverse in LIFO order without the caller having to remember arguments.
const mcpUndoMarker = "\n<mcp-undo>"

// mcpUndoJournalWarning is appended to the model-visible result of an mcp
// add/remove when its undo record could not be persisted (fail-loud): the
// declaration change is committed, but `mcp undo` may not be able to
// revert it.
const mcpUndoJournalWarning = "\n\n[undo journal warning] the mcp operation was applied but its undo record could not be persisted" +
	" (or this run context has no chat journal); mcp undo may not be able to revert it. Use mcp status to verify."

// mcpUndoPayload is the inverse ("g") of one mutating MCP operation,
// produced at the application site and returned alongside the result.
// Action is the inverse action to run: add -> {action:"remove"},
// remove -> {action:"add", config: <full deleted entry>}.
type mcpUndoPayload struct {
	Action     string                  `json:"action"`
	ServerName string                  `json:"serverName"`
	Config     *config.MCPServerConfig `json:"config,omitempty"`
}

func appendUndoMarker(text string, p mcpUndoPayload) (string, error) {
	blob, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return text + mcpUndoMarker + string(blob), nil
}

func extractUndoPayload(out string) (mcpUndoPayload, bool) {
	idx := strings.LastIndex(out, mcpUndoMarker)
	if idx < 0 {
		return mcpUndoPayload{}, false
	}
	var p mcpUndoPayload
	if err := json.Unmarshal([]byte(out[idx+len(mcpUndoMarker):]), &p); err != nil {
		return mcpUndoPayload{}, false
	}
	return p, p.Action != "" && p.ServerName != ""
}

// hasUndoMarker reports whether a tool result carries the machine-readable
// inverse payload — i.e. it is an mcp add/remove that MUST have a durable
// journal row for `mcp undo` to work.
func hasUndoMarker(out string) bool {
	_, ok := extractUndoPayload(out)
	return ok
}

// applyUndoJournalWarning implements the fail-loud decision for a tool
// result: only mcp add/remove results carrying the undo marker need a
// durable journal, so a persistence error (jerr != nil) or a run context
// without a chat journal (seq < 0) yields the explicit warning.
func applyUndoJournalWarning(toolName, result string, seq int64, jerr error) (string, bool) {
	if (jerr != nil || seq < 0) && toolName == "mcp" && hasUndoMarker(result) {
		return result + mcpUndoJournalWarning, true
	}
	return result, false
}

// mcpToolAdd registers an HTTP MCP server as one row in
// agent_mcp_servers (per-key storage, so concurrent adds of different
// servers never race on a shared document). Reversible with mcp remove:
// both go through the same per-row store methods, then trigger the
// gateway's per-agent reload notify so the next build sees the change.
func mcpToolAdd(ctx context.Context, ag *Agent, rc config.ResolvedAgent, in mcpAddInput) (string, error) {
	out, err := applyMCPAdd(ctx, ag, rc, in)
	if err != nil {
		return "", err
	}
	// Inverse produced at the application site: add's inverse is remove
	// and needs only the server name — the caller (and the persisted
	// tool_result trace) can replay it without remembering url/scopes.
	out, err = appendUndoMarker(out, mcpUndoPayload{Action: "remove", ServerName: in.ServerName})
	if err != nil {
		return "", fmt.Errorf("mcp add: %w", err)
	}
	return out, nil
}

// applyMCPAdd performs the add without the undo marker. Shared by the
// `mcp add` tool and the `mcp undo` replay path.
func applyMCPAdd(ctx context.Context, ag *Agent, rc config.ResolvedAgent, in mcpAddInput) (string, error) {
	if in.ServerName == "" || in.URL == "" {
		return "", fmt.Errorf("mcp add: serverName and url are required")
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("mcp add: url must be an absolute http(s) endpoint")
	}
	if len(in.Scopes) > 0 && in.OAuthResource == "" {
		return "", fmt.Errorf("mcp add: scopes require oauthResource (an OAuth-protected server)")
	}
	if ag == nil || ag.dataStore == nil {
		return "", fmt.Errorf("mcp add: agent config store is unavailable on this deployment")
	}
	if _, err := requireAgentOwner(ctx, ag.dataStore, rc); err != nil {
		return "", fmt.Errorf("mcp add: %w", err)
	}

	cfg := config.MCPServerConfig{
		Type:          "http",
		URL:           in.URL,
		OAuthResource: in.OAuthResource,
		Scopes:        append([]string(nil), in.Scopes...),
	}
	if err := ag.dataStore.AddMCPServer(ctx, rc.ID, in.ServerName, cfg); err != nil {
		if errors.Is(err, store.ErrMCPServerExists) {
			return "", fmt.Errorf("mcp add: server %q already exists — remove it first (or edit via the agent settings page)", in.ServerName)
		}
		return "", fmt.Errorf("mcp add: %w", err)
	}
	// A re-add in this session undoes this session's removal (see
	// resolveMCPServer's step 2).
	ag.mcpRemovedThisSession.Delete(in.ServerName)
	kind := "http"
	if in.OAuthResource != "" {
		kind = "oauth"
	}
	// Say what is true now and what still needs the rebuild. Since 2026-10-05 the
	// OAuth actions read the store (resolveMCPServer), so a server registered
	// here is usable by them in this same session; the TOOLS are what still needs
	// the next build, because the registry's tool set is fixed at construction
	// and this turn's tool list has already been handed to the model.
	out := fmt.Sprintf("%s: registered (%s).", in.ServerName, kind)
	if in.OAuthResource != "" {
		out += " The mcp OAuth actions (login/status/check/refresh/logout) can use it now."
	}
	out += " Its tools appear on the next build."
	if ag.mcpConfigNotify != nil {
		ag.mcpConfigNotify(rc.UserID, rc.ID)
		out += " Reload queued."
	}
	return out, nil
}

// mcpToolRemove deletes the server's agent_mcp_servers row and returns the
// deleted entry as the inverse payload, so the caller (or the persisted
// tool_result trace) can replay `mcp add` verbatim.
func mcpToolRemove(ctx context.Context, ag *Agent, rc config.ResolvedAgent, serverName string) (string, error) {
	if ag == nil || ag.dataStore == nil {
		return "", fmt.Errorf("mcp remove: agent config store is unavailable on this deployment")
	}
	servers, err := ag.dataStore.ListMCPServers(ctx, rc.ID)
	if err != nil {
		return "", fmt.Errorf("mcp remove: %w", err)
	}
	cfg, ok := servers[serverName]
	if !ok {
		return "", fmt.Errorf("mcp remove: %q is not a configured MCP server", serverName)
	}
	out, err := applyMCPRemove(ctx, ag, rc, serverName)
	if err != nil {
		return "", err
	}
	// Inverse produced at the application site: remove's inverse is add
	// and needs the FULL entry that is about to leave the state.
	cfgCopy := cfg
	out, err = appendUndoMarker(out, mcpUndoPayload{Action: "add", ServerName: serverName, Config: &cfgCopy})
	if err != nil {
		return "", fmt.Errorf("mcp remove: %w", err)
	}
	return out, nil
}

// applyMCPRemove performs the remove without the undo marker. Shared by
// the `mcp remove` tool and the `mcp undo` replay path.
func applyMCPRemove(ctx context.Context, ag *Agent, rc config.ResolvedAgent, serverName string) (string, error) {
	if serverName == "" {
		return "", fmt.Errorf("mcp remove: serverName is required")
	}
	if ag == nil || ag.dataStore == nil {
		return "", fmt.Errorf("mcp remove: agent config store is unavailable on this deployment")
	}
	if _, err := requireAgentOwner(ctx, ag.dataStore, rc); err != nil {
		return "", fmt.Errorf("mcp remove: %w", err)
	}
	if err := ag.dataStore.DeleteMCPServer(ctx, rc.ID, serverName); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", fmt.Errorf("mcp remove: %q is not a configured MCP server", serverName)
		}
		return "", fmt.Errorf("mcp remove: %w", err)
	}
	// Remembered for the rest of this session: the row is gone, but rc.MCPServers
	// still names it until the next build, and the OAuth actions must stop
	// resolving it NOW (resolveMCPServer step 2).
	ag.mcpRemovedThisSession.Store(serverName, true)
	// Mirror of add's wording: the OAuth actions stop resolving it now (the row
	// is gone from the store), the tools disappear on the next build.
	out := serverName + ": removed. The mcp OAuth actions stop seeing it now; its tools disappear on the next build."
	if ag.mcpConfigNotify != nil {
		ag.mcpConfigNotify(rc.UserID, rc.ID)
		out += " Reload queued."
	}
	return out, nil
}

// requireAgentOwner loads the agent row and verifies the resolved config's
// owner still matches the stored row (defense in depth behind the tool's
// actor gate: an agent must never be written through a stale rc that names
// a different owner).
func requireAgentOwner(ctx context.Context, st store.Store, rc config.ResolvedAgent) (*store.AgentRecord, error) {
	if st == nil {
		return nil, errors.New("agent config store is unavailable")
	}
	rec, err := st.GetAgent(ctx, rc.ID)
	if err != nil {
		return nil, fmt.Errorf("load agent: %w", err)
	}
	if rec == nil {
		return nil, fmt.Errorf("agent %q not found", rc.ID)
	}
	if rec.UserID != "" && rc.UserID != "" && rec.UserID != rc.UserID {
		return nil, errors.New("agent ownership mismatch")
	}
	return rec, nil
}
