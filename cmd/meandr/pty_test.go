//go:build linux || darwin

package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/meandr-inc/meandr-cli/internal/pty"
	"github.com/meandr-inc/meandr-cli/internal/tunnel"
	"github.com/meandr-inc/meandr-cli/internal/tunnel/edgetest"
)

// Built here rather than borrowed from the package: the frame is the
// contract with the service.
func dataFrame(b []byte) []byte {
	f := append([]byte{1, 0, 0, 0, 0}, b...)
	binary.BigEndian.PutUint32(f[1:5], uint32(len(b)))
	return f
}

func awaitOutput(t *testing.T, stream net.Conn, want string, n int) {
	t.Helper()
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))

	var out strings.Builder
	hdr := make([]byte, 5)
	for strings.Count(out.String(), want) < n {
		if _, err := io.ReadFull(stream, hdr); err != nil {
			t.Fatalf("read: %v; the output so far: %q", err, out.String())
		}
		if hdr[0] != 1 {
			t.Fatalf("the cli sent a frame of type %d", hdr[0])
		}
		data := make([]byte, binary.BigEndian.Uint32(hdr[1:]))
		if _, err := io.ReadFull(stream, data); err != nil {
			t.Fatalf("read: %v", err)
		}
		out.Write(data)
	}
}

// The second stream reaches the program the first one left running.
func TestTunnelStreamsAttachToTheOneProgram(t *testing.T) {
	proc, err := pty.Start(pty.Config{Command: "cat"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = proc.Stop() })

	e := edgetest.Start(t)
	d := &tunnel.Dialer{Endpoint: e.Addr(), TunnelID: "01a0-agent", Token: "tok", Kind: tunnel.KindPTY, RootCAs: e.Roots()}
	conn, info, err := d.Dial()
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	tc, err := tunnel.NewConn(conn, info, func(context.Context) (tunnel.Child, error) { return session{proc}, nil })
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tc.Serve(ctx) }()

	for _, word := range []string{"first", "second"} {
		stream := e.OpenStream(t)
		if _, err := stream.Write(dataFrame([]byte(word + "\n"))); err != nil {
			t.Fatalf("write: %v", err)
		}
		awaitOutput(t, stream, word, 2)
		_ = stream.Close()
	}
}

func TestPTYEndsWithItsCommand(t *testing.T) {
	addr, _ := fakeEdge(t, func(conn net.Conn) { _ = conn.Close() })

	for _, tc := range []struct {
		script string
		code   int
	}{
		{"exit 0", exitOK},
		{"exit 3", exitRuntime},
	} {
		c := startMeandr(t, "pty", "--id", "01a0-agent", "--endpoint", addr, "--", "sh", "-c", tc.script)
		c.wait(t)
		if code := c.cmd.ProcessState.ExitCode(); code != tc.code {
			t.Errorf("%s: exit = %d, want %d; stderr:\n%s", tc.script, code, tc.code, c.stderr.String())
		}
	}
}

func TestOnlyPTYTakesStorage(t *testing.T) {
	addr, _ := fakeEdge(t, func(conn net.Conn) { _ = conn.Close() })

	c := startMeandr(t, "pty", "--id", "01a0-agent", "--endpoint", addr, "--storage", t.TempDir(), "--", "sh", "-c", "exit 0")
	c.wait(t)
	if code := c.cmd.ProcessState.ExitCode(); code != exitOK {
		t.Fatalf("pty --storage: exit = %d, want %d; stderr:\n%s", code, exitOK, c.stderr.String())
	}

	c = startMeandr(t, "tunnel", "--id", "01a0-agent", "--endpoint", addr, "--storage", t.TempDir(), "--", "true")
	c.wait(t)
	if code := c.cmd.ProcessState.ExitCode(); code != exitUsage {
		t.Fatalf("tunnel --storage: exit = %d, want %d", code, exitUsage)
	}
}

func TestFirstSignalStopsThePTYsCommand(t *testing.T) {
	addr, dialled := fakeEdge(t, func(conn net.Conn) { _ = conn.Close() })
	pidFile := filepath.Join(t.TempDir(), "pid")
	c := startMeandr(t, "pty", "--id", "01a0-agent", "--endpoint", addr, "--",
		"sh", "-c", `echo $$ > "$1"; exec sleep 60`, "sh", pidFile)
	c.await(t, dialled)
	pid := awaitPid(t, pidFile)

	c.signal(t, syscall.SIGTERM)
	c.wait(t)
	if code := c.cmd.ProcessState.ExitCode(); code != exitOK {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, exitOK, c.stderr.String())
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the command (pid %d) outlived the cli: %v", pid, err)
	}
}

func awaitPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the command never wrote its pid")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
