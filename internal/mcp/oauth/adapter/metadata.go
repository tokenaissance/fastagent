package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/domain"
)

// HTTPMetadataFetcher fetches the two discovery documents an MCP OAuth flow needs:
// RFC 9728 (OAuth 2.0 Protected Resource Metadata) for the MCP server itself, and
// RFC 8414 (OAuth 2.0 Authorization Server Metadata) for the authorization server
// that document names.
type HTTPMetadataFetcher struct {
	Client *http.Client
}

// The two well-known suffixes. Both go BETWEEN the host and the resource's path,
// never in place of it (RFC 8414 §3.1, RFC 9728 §3): an MCP server mounted at
// https://host/mcp/quant publishes at https://host/.well-known/<suffix>/mcp/quant,
// and one at the origin root publishes at https://host/.well-known/<suffix>.
//
// Rewriting the path to the origin-root form instead — what this file used to do —
// is only correct for the second kind of server, and only because that server also
// answers at the root by accident of where it is mounted.
const (
	wellKnownAuthServer        = "oauth-authorization-server"
	wellKnownProtectedResource = "oauth-protected-resource"
)

// wellKnownURL inserts suffix between the host and the path. The trailing slash of
// the resource path is trimmed so ".../quant/" and ".../quant" name one document.
func wellKnownURL(base *url.URL, suffix string) string {
	out := *base
	resourcePath := strings.TrimSuffix(out.Path, "/")
	path := ""
	if resourcePath != "" && resourcePath != "/" {
		if !strings.HasPrefix(resourcePath, "/") {
			resourcePath = "/" + resourcePath
		}
		path = resourcePath
	}
	out.Path = "/.well-known/" + suffix + path
	out.RawPath = ""
	out.RawQuery, out.Fragment = "", ""
	return out.String()
}

// wellKnownURLs returns the candidates for one suffix: the path-aware form first,
// then the origin-root form. Both are legal publications — the path-aware URL is the
// document that belongs to THIS resource, the root one is what a server deployed at
// "/" serves — and both have to be tried, because a server may publish either.
func wellKnownURLs(u *url.URL, suffix string) []string {
	pathAware := wellKnownURL(u, suffix)
	rootU := *u
	rootU.Path, rootU.RawPath = "", ""
	root := wellKnownURL(&rootU, suffix)
	if pathAware == root {
		return []string{pathAware}
	}
	return []string{pathAware, root}
}

func isFetchableScheme(u *url.URL) bool {
	return u.Scheme == "http" || u.Scheme == "https"
}

// Fetch resolves the authorization server metadata for an MCP server URL.
//
// Candidates are tried in order and every one of them is attempted: a step that
// fails, or that answers a document without both endpoints, moves on to the next
// instead of aborting. That is what makes this change additive — the origin-root
// RFC 8414 form below is what every provider integrated before it relied on, and it
// stays reachable even when a server also publishes a protected-resource document
// that points at something unusable.
//
// Order: RFC 9728 first, because that is the document MCP servers are specified to
// publish and the only one that can name an authorization server on another host;
// then RFC 8414 against the MCP server's own URL (this file's original behaviour,
// now path-aware first); then whatever the resource's own WWW-Authenticate challenge
// advertises.
func (f *HTTPMetadataFetcher) Fetch(ctx context.Context, serverURL string) (*domain.DiscoveryMetadata, error) {
	var firstErr error

	if issuer, err := f.fetchProtectedResourceIssuer(ctx, serverURL); err == nil && issuer != "" {
		if md, err := f.fetchAuthServerMetadata(ctx, issuer); err == nil {
			return md, nil
		}
	}

	if md, err := f.fetchAuthServerMetadata(ctx, serverURL); err == nil {
		return md, nil
	} else if firstErr == nil {
		firstErr = err
	}

	// Last resort: the resource's own 401 may name both documents
	// (RFC 9728 §5.1: WWW-Authenticate: Bearer resource_metadata="…", auth-issuer="…").
	hints := f.hintsFromResource(ctx, serverURL)
	if hints.resourceMetadata != "" {
		if issuer, err := f.fetchProtectedResourceDoc(ctx, hints.resourceMetadata); err == nil && issuer != "" {
			if md, err := f.fetchAuthServerMetadata(ctx, issuer); err == nil {
				return md, nil
			}
		}
	}
	if hints.issuer != "" {
		if md, err := f.fetchAuthServerMetadata(ctx, hints.issuer); err == nil {
			return md, nil
		}
	}

	if firstErr == nil {
		firstErr = fmt.Errorf("oauth: no authorization server metadata for %s", serverURL)
	}
	return nil, firstErr
}

// getJSON GETs one already-built URL and decodes it. A non-200 is an error, not an
// empty document: discovery candidates are distinguished by answering 200 with a
// document at all, and treating a 404 body as "no metadata" would hand the parser
// an HTML error page and report a decode failure instead.
func (f *HTTPMetadataFetcher) getJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oauth: discovery http %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// fetchAuthServerMetadata fetches an RFC 8414 document for an issuer URL: the
// path-aware location first, then the origin root. Either may be the one a given
// server publishes, and the RFC 8414 §3.1 insertion rule only defines the first.
func (f *HTTPMetadataFetcher) fetchAuthServerMetadata(ctx context.Context, issuerURL string) (*domain.DiscoveryMetadata, error) {
	u, err := url.Parse(issuerURL)
	if err != nil {
		return nil, err
	}
	if !isFetchableScheme(u) {
		return nil, fmt.Errorf("oauth: discovery issuer is not an http(s) URL: %s", issuerURL)
	}
	var firstErr error
	for _, target := range wellKnownURLs(u, wellKnownAuthServer) {
		md, err := f.fetchAuthServerDoc(ctx, target)
		if err == nil {
			return md, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// fetchAuthServerDoc parses ONE already-built RFC 8414 URL. Both endpoints must be
// present: a document that is only half there is not an answer, and returning it
// would end the candidate chain at a provider that cannot complete the flow.
func (f *HTTPMetadataFetcher) fetchAuthServerDoc(ctx context.Context, target string) (*domain.DiscoveryMetadata, error) {
	var raw struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		RevocationEndpoint    string   `json:"revocation_endpoint"`
		ScopesSupported       []string `json:"scopes_supported"`
		CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
		IssParamSupported     bool     `json:"authorization_response_iss_parameter_supported"`
	}
	if err := f.getJSON(ctx, target, &raw); err != nil {
		return nil, err
	}
	if raw.AuthorizationEndpoint == "" || raw.TokenEndpoint == "" {
		return nil, fmt.Errorf("oauth: discovery document missing authorization/token endpoint")
	}
	return &domain.DiscoveryMetadata{
		Issuer:                raw.Issuer,
		AuthorizationEndpoint: raw.AuthorizationEndpoint,
		TokenEndpoint:         raw.TokenEndpoint,
		RegistrationEndpoint:  raw.RegistrationEndpoint,
		RevocationEndpoint:    raw.RevocationEndpoint,
		ScopesSupported:       raw.ScopesSupported,
		CodeChallengeMethods:  raw.CodeChallengeMethods,
		IssParamSupported:     raw.IssParamSupported,
	}, nil
}

// fetchProtectedResourceIssuer finds the RFC 9728 document for a resource URL and
// returns the first authorization server it names — the whole point of RFC 9728, and
// the only way to discover an MCP server whose AS lives on a different host.
func (f *HTTPMetadataFetcher) fetchProtectedResourceIssuer(ctx context.Context, resourceURL string) (string, error) {
	u, err := url.Parse(resourceURL)
	if err != nil {
		return "", err
	}
	if !isFetchableScheme(u) {
		return "", fmt.Errorf("oauth: discovery resource is not an http(s) URL: %s", resourceURL)
	}
	var firstErr error
	for _, target := range wellKnownURLs(u, wellKnownProtectedResource) {
		issuer, err := f.fetchProtectedResourceDoc(ctx, target)
		if err == nil && issuer != "" {
			return issuer, nil
		}
		if firstErr == nil {
			if err == nil {
				err = fmt.Errorf("oauth: protected resource metadata names no authorization server")
			}
			firstErr = err
		}
	}
	return "", firstErr
}

// fetchProtectedResourceDoc fetches ONE protected-resource metadata document. An
// empty authorization_servers array is not an error here: the caller decides whether
// "nothing named" ends the chain or is a placeholder to skip past.
func (f *HTTPMetadataFetcher) fetchProtectedResourceDoc(ctx context.Context, target string) (string, error) {
	var raw struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := f.getJSON(ctx, target, &raw); err != nil {
		return "", err
	}
	for _, s := range raw.AuthorizationServers {
		if s != "" {
			return s, nil
		}
	}
	return "", nil
}

// The discovery pointers a protected resource may put in its WWW-Authenticate
// challenge. resource_metadata is RFC 9728 §5.1's own pointer at the document;
// auth-issuer predates it and names the issuer directly.
var (
	resourceMetadataRe = regexp.MustCompile(`resource_metadata\s*=\s*"?([^",\s]+)"?`)
	authIssuerRe       = regexp.MustCompile(`auth-issuer\s*=\s*"?([^",\s]+)"?`)
)

// resourceHints carries whichever of those pointers the challenge offered.
type resourceHints struct {
	resourceMetadata string
	issuer           string
}

// hintsFromResource reads those pointers off the resource's challenge. A failure to
// reach the resource is not an error here — this is the last candidate, and the
// caller already holds the reason the earlier ones failed.
func (f *HTTPMetadataFetcher) hintsFromResource(ctx context.Context, serverURL string) resourceHints {
	var hints resourceHints
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL, nil)
	if err != nil {
		return hints
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return hints
	}
	defer resp.Body.Close()
	for _, h := range resp.Header.Values("WWW-Authenticate") {
		if m := resourceMetadataRe.FindStringSubmatch(h); m != nil && hints.resourceMetadata == "" {
			hints.resourceMetadata = m[1]
		}
		if m := authIssuerRe.FindStringSubmatch(h); m != nil && hints.issuer == "" {
			hints.issuer = m[1]
		}
	}
	return hints
}

// CachingMetadataFetcher wraps a fetcher with a per-URL TTL cache so high
// fan-out (every MCP request) doesn't re-fetch discovery each time.
type CachingMetadataFetcher struct {
	inner MetadataFetchFunc
	ttl   time.Duration
	mu    sync.Mutex
	cache map[string]cacheEntry
}

// MetadataFetchFunc matches port.MetadataFetcher.Fetch.
type MetadataFetchFunc func(ctx context.Context, serverURL string) (*domain.DiscoveryMetadata, error)

type cacheEntry struct {
	md    *domain.DiscoveryMetadata
	until time.Time
}

// NewCachingMetadataFetcher builds a cached fetcher over an HTTP fetcher.
func NewCachingMetadataFetcher(cli *http.Client, ttl time.Duration) *CachingMetadataFetcher {
	return &CachingMetadataFetcher{
		inner: (&HTTPMetadataFetcher{Client: cli}).Fetch,
		ttl:   ttl,
		cache: make(map[string]cacheEntry),
	}
}

// Fetch returns a cached document when fresh, else fetches and caches.
func (c *CachingMetadataFetcher) Fetch(ctx context.Context, serverURL string) (*domain.DiscoveryMetadata, error) {
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.cache[serverURL]; ok && now.Before(e.until) {
		md := e.md
		c.mu.Unlock()
		return md, nil
	}
	c.mu.Unlock()

	md, err := c.inner(ctx, serverURL)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache[serverURL] = cacheEntry{md: md, until: now.Add(c.ttl)}
	c.mu.Unlock()
	return md, nil
}
