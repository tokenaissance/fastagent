package mcp

// G15's delivery point (docs 10 §3.4 boundary 2): the spec requires
// `notifications/initialized` after initialize, and the client never sent it on
// either transport. Measured live on 2026-09-22 against the reference SDK server
// (TestLiveMCPHandshakeReadsTheWholeToolList): without it `tools/list` answers 12
// tools, with it 13 — so skipping it is not a harmless omission, it is the client
// reporting a smaller world than the server offers.

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

type captureWriteCloser struct{ buf bytes.Buffer }

func (c *captureWriteCloser) Write(p []byte) (int, error) { return c.buf.Write(p) }
func (c *captureWriteCloser) Close() error                { return nil }

func TestStdioClientSendsInitializedAfterTheHandshake(t *testing.T) {
	stdout := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}` + "\n"
	pipe := &captureWriteCloser{}
	c := &StdioClient{
		stdin:   pipe,
		scanner: bufio.NewScanner(strings.NewReader(stdout)),
		nextID:  1,
	}
	if err := c.handshake(); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(pipe.buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("the handshake wrote %d lines; want initialize then notifications/initialized:\n%s", len(lines), pipe.buf.String())
	}
	if !strings.Contains(lines[0], `"method":"initialize"`) {
		t.Fatalf("first line = %s; want initialize", lines[0])
	}
	second := lines[1]
	if !strings.Contains(second, `"method":"notifications/initialized"`) {
		t.Fatalf("second line = %s; want the initialized notification", second)
	}
	if strings.Contains(second, `"id"`) {
		t.Fatalf("the initialized notification carries an id: %s", second)
	}
	if c.protocolVersion != "2024-11-05" {
		t.Fatalf("recorded protocol version = %q; want the server's answer", c.protocolVersion)
	}
}

// A notification carries no id, so it must not consume one from the request
// sequence either: the next real request keeps the id the server expects.
func TestANotificationDoesNotConsumeARequestID(t *testing.T) {
	stdout := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}` + "\n"
	pipe := &captureWriteCloser{}
	c := &StdioClient{stdin: pipe, scanner: bufio.NewScanner(strings.NewReader(stdout)), nextID: 1}
	if err := c.handshake(); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if c.nextID != 2 {
		t.Fatalf("nextID after the handshake = %d; want 2 (initialize took 1, the notification takes none)", c.nextID)
	}
}

var _ io.WriteCloser = (*captureWriteCloser)(nil)
