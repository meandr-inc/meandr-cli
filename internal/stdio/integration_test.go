package stdio_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/meandr-inc/meandr-cli/internal/stdio"
)

// Drives a real MCP server through a real handshake over Attach. Opt-in,
// since it needs npx and the network:
//
//	MEANDR_INTEGRATION=1 go test ./internal/stdio/ -run Integration -v
//
// The test parses MCP to check the pipe is transparent. The package
// itself never does.
func TestIntegrationRealMCPServer(t *testing.T) {
	if os.Getenv("MEANDR_INTEGRATION") == "" {
		t.Skip("set MEANDR_INTEGRATION=1 to run (needs npx and network)")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not on PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	child := stdio.New(stdio.Config{
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-everything"},
	})
	if err := child.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ours, theirs := net.Pipe()
	go func() { _ = child.Attach(ctx, theirs) }()
	defer func() { _ = ours.Close() }()

	write := func(frame string) {
		t.Helper()
		if _, err := ours.Write([]byte(frame + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"meandr-cli","version":"test"}}}`)

	// Read until both responses have come back. Substring matching, so
	// the test does not grow a decoder either.
	var seen strings.Builder
	buf := make([]byte, 32<<10)
	sentList := false

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		_ = ours.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := ours.Read(buf)
		if n > 0 {
			seen.Write(buf[:n])
		}
		if !sentList && strings.Contains(seen.String(), `"serverInfo"`) {
			sentList = true
			write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
			write(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		}
		if sentList && strings.Contains(seen.String(), `"tools":[`) {
			t.Logf("handshake and tools/list completed through a pipe that parses nothing (%d bytes)", seen.Len())
			return
		}
		if err != nil {
			break
		}
	}
	t.Fatalf("did not complete the handshake; stderr: %v", child.StderrTail())
}
