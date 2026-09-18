// Command mcp-oauth-stub is a throwaway authorization server + resource server
// used to answer five questions about real MCP clients before any of the real
// OAuth work is written (see README.md). It is deliberately not production
// code: no database, no rate limiting, no real login — it accepts any client,
// any user, and issues tokens to whoever asks.
//
// Everything it learns is printed to stdout as one JSON object per line, so a
// session can be replayed and diffed instead of remembered.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const skillsExtensionID = "io.modelcontextprotocol/skills"

type config struct {
	addr      string
	publicURL string // what the client sees: scheme + host (+ optional port)
	agentID   string
	scope     string
	offerCIMD bool
	offerDCR  bool
	failAuthz bool   // make /oauth/authorize fail, to observe the client's error path
	fetchCIMD bool   // fetch URL-formatted client_ids and log what they contain
	noAuth    bool   // skip the 401 challenge: isolates Q4/Q5 from the authorization dance
	protocol  string // answer initialize with this revision; empty = echo the client's request
	deny      bool   // answer /oauth/authorize with an OAuth error redirect (the real denial shape)
}

var (
	cfg   config
	mu    sync.Mutex
	codes = map[string]authRequest{}
)

// authRequest is one pending authorization, keyed by the code we mint.
type authRequest struct {
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	ChallengeMeth string
	Resource      string
	Scope         string
}

// event is the single log shape: one fact per line, greppable and diffable.
func event(kind string, fields map[string]any) {
	rec := map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "event": kind}
	for k, v := range fields {
		rec[k] = v
	}
	b, _ := json.Marshal(rec)
	fmt.Fprintln(os.Stdout, string(b))
}

func main() {
	flag.StringVar(&cfg.addr, "addr", "127.0.0.1:8787", "listen address")
	flag.StringVar(&cfg.publicURL, "public-url", "", "URL clients use (defaults to http://<addr>); set this to an https tunnel when a client refuses plain http")
	flag.StringVar(&cfg.agentID, "agent", "agt_spike", "agent id in the resource URL")
	flag.StringVar(&cfg.scope, "scope", "skills:read", "scope challenged and issued")
	flag.BoolVar(&cfg.offerCIMD, "cimd", true, "advertise client_id_metadata_document_supported")
	flag.BoolVar(&cfg.offerDCR, "dcr", true, "advertise registration_endpoint")
	flag.BoolVar(&cfg.failAuthz, "fail-authorize", false, "make /oauth/authorize return an error, to see how the client reports it")
	flag.BoolVar(&cfg.fetchCIMD, "fetch-cimd", true, "fetch URL-formatted client_id documents and log their fields")
	flag.BoolVar(&cfg.noAuth, "no-auth", false, "accept requests without a token (isolates the handshake/surface questions from OAuth)")
	flag.StringVar(&cfg.protocol, "protocol-version", "", "revision to answer `initialize` with (default: echo what the client asked for)")
	flag.BoolVar(&cfg.deny, "deny", false, "redirect back with error=access_denied instead of issuing a code (Q3)")
	flag.Parse()

	if cfg.publicURL == "" {
		cfg.publicURL = "http://" + cfg.addr
	}
	cfg.publicURL = strings.TrimRight(cfg.publicURL, "/")

	mux := http.NewServeMux()
	// --- resource server ---
	mux.HandleFunc("/.well-known/oauth-protected-resource", prmHandler)
	mux.HandleFunc("/.well-known/oauth-protected-resource/", prmHandler) // RFC 9728 path-suffixed form
	mux.HandleFunc("/mcp/agents/", mcpHandler)
	// --- authorization server ---
	mux.HandleFunc("/.well-known/oauth-authorization-server", asmHandler)
	mux.HandleFunc("/oauth/authorize", authorizeHandler)
	mux.HandleFunc("/oauth/token", tokenHandler)
	mux.HandleFunc("/oauth/register", registerHandler)
	mux.HandleFunc("/oauth/revoke", revokeHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		event("unmatched", map[string]any{"method": r.Method, "path": r.URL.Path})
		http.NotFound(w, r)
	})

	event("stub-started", map[string]any{
		"publicURL": cfg.publicURL,
		"resource":  cfg.resource(),
		"cimd":      cfg.offerCIMD,
		"dcr":       cfg.offerDCR,
	})
	log.Fatal(http.ListenAndServe(cfg.addr, logRequests(mux)))
}

func (c config) resource() string {
	return c.publicURL + "/mcp/agents/" + c.agentID
}

// logRequests records every request line, because which well-known form a
// client probes (bare vs path-suffixed) is itself one of the observations.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		event("http", map[string]any{
			"method":     r.Method,
			"path":       r.URL.Path,
			"query":      r.URL.RawQuery,
			"userAgent":  r.Header.Get("User-Agent"),
			"authorized": r.Header.Get("Authorization") != "",
		})
		next.ServeHTTP(w, r)
	})
}

// --- resource server ---

func prmHandler(w http.ResponseWriter, r *http.Request) {
	meta := map[string]any{
		"resource":                 cfg.resource(),
		"authorization_servers":    []string{cfg.publicURL},
		"scopes_supported":         []string{cfg.scope},
		"bearer_methods_supported": []string{"header"},
	}
	writeJSON(w, meta)
}

func asmHandler(w http.ResponseWriter, r *http.Request) {
	meta := map[string]any{
		"issuer":                                         cfg.publicURL,
		"authorization_endpoint":                         cfg.publicURL + "/oauth/authorize",
		"token_endpoint":                                 cfg.publicURL + "/oauth/token",
		"revocation_endpoint":                            cfg.publicURL + "/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"scopes_supported":                               []string{cfg.scope},
		"authorization_response_iss_parameter_supported": true,
	}
	if cfg.offerCIMD {
		meta["client_id_metadata_document_supported"] = true
	}
	if cfg.offerDCR {
		meta["registration_endpoint"] = cfg.publicURL + "/oauth/register"
	}
	writeJSON(w, meta)
}

// mcpHandler is the resource server endpoint. Without a token it must produce
// the challenge that starts discovery; with one it answers whatever MCP method
// the client sends, which is how we learn which surface the client actually uses.
func mcpHandler(w http.ResponseWriter, r *http.Request) {
	if !cfg.noAuth && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(
			`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", scope="%s"`,
			cfg.publicURL, cfg.scope))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only in this stub", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	event("mcp", map[string]any{"method": req.Method, "bytes": len(body)})
	if req.ID == nil { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := cfg.protocol
		if version == "" {
			version = p.ProtocolVersion // echo: lets a pre-2026-07-28 client proceed
		}
		if version == "" {
			version = "2026-07-28"
		}
		event("handshake", map[string]any{
			"method":           req.Method,
			"requestedVersion": p.ProtocolVersion,
			"answeredVersion":  version,
		})
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": version,
			"serverInfo":      map[string]any{"name": "spike-stub", "version": "0"},
			"instructions":    "spike stub: answer the client's discovery questions and log what it does next",
			"capabilities": map[string]any{
				"tools":     map[string]any{},
				"resources": map[string]any{},
				"extensions": map[string]any{
					skillsExtensionID: map[string]any{"directoryRead": true},
				},
			},
		})
	case "server/discover":
		// 2026-07-28 shape: capabilities + serverInfo + instructions, and no
		// protocolVersion — the revision is implied by the method existing.
		event("handshake", map[string]any{"method": req.Method})
		writeRPC(w, req.ID, map[string]any{
			"resultType":   "complete",
			"serverInfo":   map[string]any{"name": "spike-stub", "version": "0"},
			"instructions": "spike stub: answer the client's discovery questions and log what it does next",
			"capabilities": map[string]any{
				"tools":     map[string]any{},
				"resources": map[string]any{},
				"extensions": map[string]any{
					skillsExtensionID: map[string]any{"directoryRead": true},
				},
			},
		})
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{"resultType": "complete", "tools": []any{
			map[string]any{
				"name":        "list_skills",
				"description": "stub fallback: list the skills this server publishes",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			},
			map[string]any{
				"name":        "read_skill",
				"description": "stub fallback: read one skill's SKILL.md",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string", "description": "skill name, e.g. spike-skill"},
					},
					"required":             []string{"name"},
					"additionalProperties": false,
				},
			},
		}})
	case "skills/list":
		writeRPC(w, req.ID, map[string]any{"resultType": "complete", "skills": []any{
			map[string]any{
				"uri":         "skill://spike-skill/SKILL.md",
				"frontmatter": map[string]any{"name": "spike-skill", "description": "stub skill used to watch which surface the client consumes"},
				"resources": []any{map[string]any{
					"uri":    "skill://spike-skill/SKILL.md",
					"digest": "sha256:" + strings.Repeat("0", 64),
					"size":   54,
				}},
			},
		}, "ttlMs": 60000, "cacheScope": "public"})
	case "skills/get":
		writeRPC(w, req.ID, map[string]any{"resultType": "complete", "skill": map[string]any{
			"uri":         "skill://spike-skill/SKILL.md",
			"frontmatter": map[string]any{"name": "spike-skill", "description": "stub skill"},
			"resources":   []any{map[string]any{"uri": "skill://spike-skill/SKILL.md", "digest": "sha256:" + strings.Repeat("0", 64), "size": 54}},
		}, "ttlMs": 60000, "cacheScope": "public"})
	case "resources/list":
		writeRPC(w, req.ID, map[string]any{"resultType": "complete", "resources": []any{
			map[string]any{"uri": "skill://spike-skill/SKILL.md", "name": "spike-skill", "mimeType": "text/markdown"},
		}})
	case "resources/read":
		writeRPC(w, req.ID, map[string]any{"resultType": "complete", "contents": []any{
			map[string]any{"uri": "skill://spike-skill/SKILL.md", "mimeType": "text/markdown",
				"text": "---\nname: spike-skill\ndescription: stub skill\n---\n\nSay the word \"spike\" out loud.\n"},
		}})
	case "resources/directory/read":
		writeRPC(w, req.ID, map[string]any{"resultType": "complete", "resources": []any{}})
	case "tools/call":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &p)
		event("tools-call", map[string]any{"name": p.Name})
		writeRPC(w, req.ID, map[string]any{
			"resultType": "complete",
			"content": []any{map[string]any{"type": "text",
				"text": "spike-skill: `skill://spike-skill/SKILL.md` — say the word \"spike\" out loud.\n"}},
		})
	default:
		writeRPCError(w, req.ID, -32601, "method not implemented in the stub: "+req.Method)
	}
}

// --- authorization server ---

func registerHandler(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	clientID := "stub-client-" + randHex(8)
	event("dcr-register", map[string]any{"clientID": clientID, "body": body})
	writeJSON(w, map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        time.Now().Unix(),
		"redirect_uris":              body["redirect_uris"],
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

// authorizeHandler stands in for the real login+consent page. It performs no
// authentication; it records what the client asked for, optionally fetches the
// client's CIMD document, then offers one button that returns the code.
func authorizeHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := authRequest{
		ClientID:      q.Get("client_id"),
		RedirectURI:   q.Get("redirect_uri"),
		State:         q.Get("state"),
		CodeChallenge: q.Get("code_challenge"),
		ChallengeMeth: q.Get("code_challenge_method"),
		Resource:      q.Get("resource"),
		Scope:         q.Get("scope"),
	}
	redirectHost := ""
	if u, err := url.Parse(req.RedirectURI); err == nil {
		redirectHost = u.Host
	}
	event("authorize-request", map[string]any{
		"clientID":            req.ClientID,
		"clientIDIsURL":       strings.HasPrefix(req.ClientID, "https://"),
		"redirectURI":         req.RedirectURI,
		"redirectHost":        redirectHost,
		"state":               req.State != "",
		"codeChallenge":       req.CodeChallenge != "",
		"codeChallengeMethod": req.ChallengeMeth,
		"resource":            req.Resource,
		"resourceMatches":     req.Resource == cfg.resource(),
		"scope":               req.Scope,
	})
	if cfg.fetchCIMD && strings.HasPrefix(req.ClientID, "https://") {
		go fetchCIMD(req.ClientID)
	}
	if cfg.failAuthz {
		http.Error(w, "stub: authorization denied on purpose", http.StatusForbidden)
		return
	}
	if cfg.deny {
		u, err := url.Parse(req.RedirectURI)
		if err != nil {
			http.Error(w, "bad redirect_uri: "+err.Error(), http.StatusBadRequest)
			return
		}
		qq := u.Query()
		qq.Set("error", "access_denied")
		qq.Set("error_description", "stub: the user denied this request on purpose")
		qq.Set("iss", cfg.publicURL)
		if req.State != "" {
			qq.Set("state", req.State)
		}
		u.RawQuery = qq.Encode()
		event("authorize-denied", map[string]any{"redirectURI": req.RedirectURI})
		http.Redirect(w, r, u.String(), http.StatusFound)
		return
	}
	code := randHex(16)
	mu.Lock()
	codes[code] = req
	mu.Unlock()
	callback := req.RedirectURI
	if callback != "" {
		u, err := url.Parse(callback)
		if err == nil {
			qq := u.Query()
			qq.Set("code", code)
			qq.Set("iss", cfg.publicURL)
			if req.State != "" {
				qq.Set("state", req.State)
			}
			u.RawQuery = qq.Encode()
			callback = u.String()
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>spike AS</title>
<h1>Spike authorization server</h1>
<p>No login here on purpose. This page exists to record what the client sent and to hand back a code.</p>
<ul>
<li>client_id: <code>%s</code></li>
<li>redirect_uri: <code>%s</code></li>
<li>resource: <code>%s</code></li>
<li>scope: <code>%s</code></li>
<li>PKCE: %s (%s)</li>
</ul>
<p><a href="%s">Approve and return to the client</a></p>`,
		htmlEscape(req.ClientID), htmlEscape(req.RedirectURI), htmlEscape(req.Resource),
		htmlEscape(req.Scope), boolWord(req.CodeChallenge != ""), htmlEscape(req.ChallengeMeth),
		htmlEscape(callback))
}

func tokenHandler(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	grant := r.Form.Get("grant_type")
	fields := map[string]any{
		"grantType":    grant,
		"clientID":     r.Form.Get("client_id"),
		"code":         r.Form.Get("code") != "",
		"codeVerifier": r.Form.Get("code_verifier") != "",
		"redirectURI":  r.Form.Get("redirect_uri"),
		"resource":     r.Form.Get("resource"),
		"scope":        r.Form.Get("scope"),
		"clientSecret": r.Form.Get("client_secret") != "",
	}
	mu.Lock()
	req, ok := codes[r.Form.Get("code")]
	delete(codes, r.Form.Get("code"))
	mu.Unlock()
	if grant == "authorization_code" {
		fields["codeKnown"] = ok
		if ok {
			fields["resourceMatches"] = r.Form.Get("resource") == cfg.resource() && req.Resource == cfg.resource()
		}
	}
	event("token-request", fields)
	writeJSON(w, map[string]any{
		"access_token":  "stub-access-" + randHex(12),
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": "stub-refresh-" + randHex(12),
		"scope":         cfg.scope,
	})
}

func revokeHandler(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	event("revoke", map[string]any{"clientID": r.Form.Get("client_id"), "tokenTypeHint": r.Form.Get("token_type_hint")})
	w.WriteHeader(http.StatusOK)
}

// fetchCIMD shows what a real AS would receive when a client uses an HTTPS URL
// as its client_id: the document's claims, which we validate against the
// authorization request's redirect_uri in the real implementation.
func fetchCIMD(clientID string) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(clientID)
	if err != nil {
		event("cimd-fetch", map[string]any{"clientID": clientID, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	event("cimd-fetch", map[string]any{
		"clientID":     clientID,
		"status":       resp.StatusCode,
		"redirectURIs": doc["redirect_uris"],
		"fields":       keysOf(doc),
	})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result map[string]any) {
	writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func boolWord(b bool) string {
	if b {
		return "present"
	}
	return "missing"
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
