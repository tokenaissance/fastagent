package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"
)

// recorder remembers the request paths a test server was asked for, in order. "It
// worked" is not the claim under test here — "it asked the path-aware location
// first, and never stopped asking the origin-root one" is, and only the order can
// witness that.
type recorder struct {
	mu    sync.Mutex
	paths []string
}

func (r *recorder) record(p string) {
	r.mu.Lock()
	r.paths = append(r.paths, p)
	r.mu.Unlock()
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

// authServerDoc is the RFC 8414 body every issuer in these tests serves.
func authServerDoc(issuerURL string) map[string]any {
	return map[string]any{
		"issuer":                 issuerURL,
		"authorization_endpoint": issuerURL + "/authorize",
		"token_endpoint":         issuerURL + "/token",
		"registration_endpoint":  issuerURL + "/register",
	}
}

// A server that publishes its RFC 8414 document ONLY under its own path — the shape
// that made the old origin-root rewrite fail.
func TestHTTPMetadataFetcherPrefersPathAwareAuthServerDoc(t *testing.T) {
	rec := &recorder{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.URL.Path)
		if r.URL.Path != "/.well-known/oauth-authorization-server/quant" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(authServerDoc(srv.URL))
	}))
	defer srv.Close()

	md, err := (&HTTPMetadataFetcher{Client: srv.Client()}).Fetch(context.Background(), srv.URL+"/quant")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if md.TokenEndpoint != srv.URL+"/token" {
		t.Fatalf("metadata came from the wrong document: %+v", md)
	}
	// RFC 9728 first (the document MCP servers are specified to publish), then
	// RFC 8414 — and within each, the path-aware location before the root.
	want := []string{
		"/.well-known/oauth-protected-resource/quant",
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-authorization-server/quant",
	}
	if got := rec.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requested paths = %v, want %v", got, want)
	}
}

// The pre-change behaviour, and the regression guard for every provider integrated
// that way (Robinhood among them): a document served at the origin root must keep
// working, even though it is now the third candidate instead of the first.
func TestHTTPMetadataFetcherWellKnown(t *testing.T) {
	rec := &recorder{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.URL.Path)
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(authServerDoc(srv.URL))
	}))
	defer srv.Close()

	md, err := (&HTTPMetadataFetcher{Client: srv.Client()}).Fetch(context.Background(), srv.URL+"/quant")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if md.AuthorizationEndpoint == "" || md.TokenEndpoint == "" {
		t.Fatalf("incomplete metadata: %+v", md)
	}
	want := []string{
		"/.well-known/oauth-protected-resource/quant",
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-authorization-server/quant",
		"/.well-known/oauth-authorization-server",
	}
	if got := rec.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requested paths = %v, want %v", got, want)
	}
}

// RFC 9728: the MCP server is not the authorization server, and its metadata document
// is the only thing that says where the AS lives.
func TestHTTPMetadataFetcherReadsProtectedResourceMetadata(t *testing.T) {
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(authServerDoc(issuer.URL))
	}))
	defer issuer.Close()

	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-protected-resource/quant" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"resource":              "https://mcp.example.com/quant",
			"authorization_servers": []string{issuer.URL},
		})
	}))
	defer resource.Close()

	md, err := (&HTTPMetadataFetcher{Client: resource.Client()}).Fetch(context.Background(), resource.URL+"/quant")
	if err != nil {
		t.Fatalf("fetch via RFC 9728: %v", err)
	}
	if md.RegistrationEndpoint != issuer.URL+"/register" {
		t.Fatalf("metadata did not come from the issuer the resource named: %+v", md)
	}
}

// RFC 9728 §5.1: the resource points at its document by URL. Honouring the pointer
// means fetching it as given, not deriving a well-known location from it.
func TestHTTPMetadataFetcherHonorsResourceMetadataPointer(t *testing.T) {
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(authServerDoc(issuer.URL))
	}))
	defer issuer.Close()

	doc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prm.json" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"authorization_servers": []string{issuer.URL},
		})
	}))
	defer doc.Close()

	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+doc.URL+`/prm.json"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer resource.Close()

	md, err := (&HTTPMetadataFetcher{Client: resource.Client()}).Fetch(context.Background(), resource.URL+"/quant")
	if err != nil {
		t.Fatalf("fetch via resource_metadata pointer: %v", err)
	}
	if md.TokenEndpoint != issuer.URL+"/token" {
		t.Fatalf("metadata did not come from the pointed-at document: %+v", md)
	}
}

// The pre-9728 convention: the challenge names the issuer directly.
func TestHTTPMetadataFetcherWWWAuthenticateFallback(t *testing.T) {
	// Issuer server carries the real discovery document.
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 issuer.URL,
			"authorization_endpoint": issuer.URL + "/authorize",
			"token_endpoint":         issuer.URL + "/token",
		})
	}))
	defer issuer.Close()

	// Resource server has no well-known doc but advertises the issuer.
	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer auth-issuer="`+issuer.URL+`"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer resource.Close()

	md, err := (&HTTPMetadataFetcher{Client: resource.Client()}).Fetch(context.Background(), resource.URL+"/quant")
	if err != nil {
		t.Fatalf("fetch with fallback: %v", err)
	}
	if md.Issuer != issuer.URL || md.AuthorizationEndpoint == "" {
		t.Fatalf("fallback metadata wrong: %+v", md)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return u
}

// The insertion rule itself (RFC 8414 §3.1, RFC 9728 §3): the well-known segment goes
// between the host and the path, and never replaces the path. The old code set
// u.Path outright, which is why a server mounted anywhere but "/" was invisible.
func TestWellKnownURLKeepsTheResourcePath(t *testing.T) {
	cases := []struct {
		raw    string
		suffix string
		want   string
	}{
		{
			"https://host/mcp/quant",
			wellKnownProtectedResource,
			"https://host/.well-known/oauth-protected-resource/mcp/quant",
		},
		{
			"https://host/mcp/quant",
			wellKnownAuthServer,
			"https://host/.well-known/oauth-authorization-server/mcp/quant",
		},
		// A trailing slash names the same resource as the bare path (RFC 9728 §3.1).
		{
			"https://host/quant/",
			wellKnownAuthServer,
			"https://host/.well-known/oauth-authorization-server/quant",
		},
		// Origin root: no path to keep.
		{
			"https://host",
			wellKnownAuthServer,
			"https://host/.well-known/oauth-authorization-server",
		},
		{
			"https://host/",
			wellKnownAuthServer,
			"https://host/.well-known/oauth-authorization-server",
		},
		// The resource's query and fragment are not part of the document's address.
		{
			"https://host/quant?x=1#frag",
			wellKnownAuthServer,
			"https://host/.well-known/oauth-authorization-server/quant",
		},
	}
	for _, c := range cases {
		if got := wellKnownURL(mustParse(t, c.raw), c.suffix); got != c.want {
			t.Errorf("wellKnownURL(%s, %s) = %s, want %s", c.raw, c.suffix, got, c.want)
		}
	}

	// A path-less resource has one candidate, not two: the path-aware and root forms
	// would be the same URL, and requesting it twice is a wasted round trip.
	if got := wellKnownURLs(mustParse(t, "https://host"), wellKnownAuthServer); len(got) != 1 {
		t.Errorf("wellKnownURLs(origin) = %v, want a single candidate", got)
	}
	if got := wellKnownURLs(mustParse(t, "https://host/quant"), wellKnownAuthServer); len(got) != 2 {
		t.Errorf("wellKnownURLs(path) = %v, want two candidates", got)
	}
}
