package mcp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/mcp/oauth/domain"
)

// TestManagerLogsTheRemedyWhenAServerNeedsReauthorization pins the log
// contract for the one connect failure a human can fix.
//
// Every other reason a server is skipped is transient or operational: the
// host is down, the transport refused, the protocol handshake failed. A
// credential that cannot be renewed is different — nothing recovers until
// the owner authorizes again, and the operator reading the log is the one
// who has to do it. So the line has to name the remedy, not just the
// failure. Before this, the reason arrived as
//
//	failed to connect to MCP server, skipping server=quantconnect
//	  error="mcp: oauth token unavailable: oauth: no refresh token stored"
//
// which reads like a broken token store rather than "re-authorize".
//
// The test walks both failures past the same manager so the assertion is
// about the *distinction*, not about prose: an ordinary connect failure
// keeps the generic line, and only the unrenewable credential gets the one
// that names the remedy.
func TestManagerLogsTheRemedyWhenAServerNeedsReauthorization(t *testing.T) {
	generic := connectLogsFor(t, errors.New("dial tcp: connection refused"))
	if !strings.Contains(generic, "failed to connect to MCP server, skipping") {
		t.Fatalf("an ordinary connect failure must keep the generic line:\n%s", generic)
	}

	reauth := connectLogsFor(t, domain.ErrReauthRequired)
	if !strings.Contains(reauth, "quantconnect") {
		t.Fatalf("log must name the server that was skipped:\n%s", reauth)
	}
	if !strings.Contains(reauth, "needs re-authorization") {
		t.Fatalf("log must name the remedy (re-authorize), not just the failure:\n%s", reauth)
	}
	if strings.Contains(reauth, "failed to connect to MCP server, skipping") {
		t.Fatalf("the unrenewable credential must not be reported as an ordinary connect failure:\n%s", reauth)
	}
}

// connectLogsFor points a one-server manager at a server it cannot reach
// (the auth provider fails before any request leaves) and returns the WARN
// output of that connect attempt.
func connectLogsFor(t *testing.T, authErr error) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	m := NewManager(
		map[string]config.MCPServerConfig{"quantconnect": {Type: "http", URL: srv.URL}},
		WithAuth("quantconnect", func(context.Context) (string, error) {
			return "", authErr
		}),
	)
	defer m.Close()
	return buf.String()
}
