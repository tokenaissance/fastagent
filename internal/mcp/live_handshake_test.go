package mcp

// The live half of G15 (docs 10 §3.4 boundary 2). It needs the network and a
// node toolchain, so it is opt-in:
//
//	FASTAGENT_MCP_LIVE=1 go test ./internal/mcp/ -run TestLiveMCP -count=1 -v
//
// It drives the REAL reference server (`@modelcontextprotocol/server-everything`,
// the SDK's own conformance server) through our client, and asserts the thing the
// handshake is for: the server registers part of its tool list from its
// `oninitialized` hook, so a client that skips `notifications/initialized` reads
// a smaller tool list than the server offers. `simulate-research-query` is that
// tool — measured 2026-09-22, 12 tools without the notification and 13 with it.
//
// Falsification (run for real): drop the notify call in StdioClient.handshake ⇒
// this test reddens with "the server offers simulate-research-query only once the
// handshake says initialized".

import (
	"os"
	"testing"
	"time"
)

func TestLiveMCPHandshakeReadsTheWholeToolList(t *testing.T) {
	if os.Getenv("FASTAGENT_MCP_LIVE") != "1" {
		t.Skip("set FASTAGENT_MCP_LIVE=1 to run against the real reference MCP server")
	}
	c := NewStdioClient("npx", []string{"-y", "@modelcontextprotocol/server-everything"}, nil)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect to the reference server: %v", err)
	}
	defer c.Close()

	deadline := time.Now().Add(60 * time.Second)
	var names []string
	for {
		tools, err := c.ListTools()
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		names = names[:0]
		for _, tool := range tools {
			names = append(names, tool.Name)
		}
		if len(names) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}

	if c.protocolVersion == "" {
		t.Fatalf("the negotiated protocol version was not recorded from the handshake")
	}
	found := false
	for _, n := range names {
		if n == "simulate-research-query" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tools = %v (%d): the server offers simulate-research-query only once the handshake says initialized", names, len(names))
	}
	t.Logf("negotiated %s; %d tools, including the one gated on initialized", c.protocolVersion, len(names))
}
