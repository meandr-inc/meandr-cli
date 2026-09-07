package stdio

import (
	"context"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The package's contract: bytes in, the same bytes out. "cat" stands in
// for an MCP server, so the test involves no protocol.
func TestAttachIsATransparentPipe(t *testing.T) {
	skipOnWindows(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := New(Config{Command: "cat"})
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ours, theirs := net.Pipe()
	go func() { _ = c.Attach(ctx, theirs) }()

	// Not newline-delimited JSON, and split across writes: the package
	// must not care where a message begins.
	payloads := []string{
		`{"jsonrpc":"2.0","id":1}` + "\n",
		"not json at all\n",
		`{"partial":`, `"split across writes"}` + "\n",
	}
	var want strings.Builder
	for _, p := range payloads {
		want.WriteString(p)
	}

	go func() {
		for _, p := range payloads {
			_, _ = ours.Write([]byte(p))
			time.Sleep(10 * time.Millisecond)
		}
	}()

	got := make([]byte, 0, want.Len())
	buf := make([]byte, 256)
	_ = ours.SetReadDeadline(time.Now().Add(5 * time.Second))
	for len(got) < want.Len() {
		n, err := ours.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			break
		}
	}
	if string(got) != want.String() {
		t.Errorf("pipe altered the bytes:\n got %q\nwant %q", got, want.String())
	}
	_ = ours.Close()
}

// The stream ending ends the session, and the child must go with it.
// Otherwise every disconnect leaks a process.
func TestChildDiesWithTheStream(t *testing.T) {
	skipOnWindows(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := New(Config{Command: "cat"})
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := c.cmd.Process.Pid

	ours, theirs := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- c.Attach(ctx, theirs) }()

	_ = ours.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Attach after peer close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Attach did not return after the stream closed")
	}
	if processAlive(pid) {
		t.Errorf("child %d still running after the stream closed", pid)
	}
}

// The previous test's `cat` exits when stdin closes, so it passes either
// way. This one does not: a server that ignores stdin close and says
// nothing must still be stopped when its stream goes.
func TestChildStoppedWhenTheStreamGoesThoughItIgnoresStdin(t *testing.T) {
	skipOnWindows(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Ignores stdin, never writes: a server with a timer, in miniature.
	c := New(Config{Command: "sh", Args: []string{"-c", "while true; do sleep 1; done"}})
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := c.cmd.Process.Pid

	ours, theirs := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- c.Attach(ctx, theirs) }()

	_ = ours.Close()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Attach waited on a silent stdout instead of stopping the child")
	}
	if processAlive(pid) {
		t.Errorf("child %d still running after its stream closed", pid)
	}
}

// A server that dies during startup explains itself on stderr and
// nowhere else.
func TestStderrTailKeptForDiagnosis(t *testing.T) {
	skipOnWindows(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := New(Config{Command: "sh", Args: []string{"-c", "echo 'cannot find module' >&2; exit 3"}})
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ours, theirs := net.Pipe()
	defer func() { _ = ours.Close() }()
	err := c.Attach(ctx, theirs)
	if err == nil {
		t.Error("a non-zero exit should be reported")
	}

	// Give the stderr reader a moment to drain after the process exits.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(c.StderrTail(), "\n"), "cannot find module") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("stderr tail lost the reason: %q", c.StderrTail())
}

func TestStderrTailBounded(t *testing.T) {
	skipOnWindows(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := New(Config{Command: "sh", Args: []string{"-c", "for i in $(seq 1 200); do echo line$i >&2; done"}})
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ours, theirs := net.Pipe()
	defer func() { _ = ours.Close() }()
	_ = c.Attach(ctx, theirs)

	if tail := c.StderrTail(); len(tail) > stderrLines {
		t.Fatalf("tail kept %d lines, cap is %d", len(tail), stderrLines)
	}
}

// A server reading to EOF exits when stdin closes, without a signal.
func TestStopClosesStdinFirst(t *testing.T) {
	skipOnWindows(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := New(Config{Command: "cat"})
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= stopGrace {
		t.Errorf("Stop took %v; cat exits on EOF long before the %v grace", elapsed, stopGrace)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	skipOnWindows(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := New(Config{Command: "cat"})
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := c.Stop()
	if second := c.Stop(); second != first {
		t.Errorf("second Stop returned %v, first returned %v", second, first)
	}
}

func TestAttachBeforeStartFails(t *testing.T) {
	_, theirs := net.Pipe()
	c := New(Config{Command: "cat"})
	if err := c.Attach(context.Background(), theirs); err == nil {
		t.Fatal("Attach before Start should fail")
	}
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
}

// Cancelling the context must reach the whole tree. A wrapper like npx
// execs the real server as a grandchild, and killing only the direct
// process leaves it running on the customer's host.
func TestContextCancelStopsGrandchildren(t *testing.T) {
	skipOnWindows(t)
	ctx, cancel := context.WithCancel(context.Background())

	// sh reports the grandchild's pid, then waits for it.
	c := New(Config{Command: "sh", Args: []string{"-c", "sleep 30 & echo $! ; wait"}})
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ours, theirs := net.Pipe()
	defer func() { _ = ours.Close() }()
	go func() { _ = c.Attach(ctx, theirs) }()

	line := make([]byte, 32)
	n, err := ours.Read(line)
	if err != nil {
		t.Fatalf("read grandchild pid: %v", err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(string(line[:n])))
	if err != nil {
		t.Fatalf("parse grandchild pid %q: %v", line[:n], err)
	}

	cancel()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(grandchild) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	killProcess(grandchild) // do not leak it from the test
	t.Fatal("grandchild survived the cancelled context")
}

// The direct process can exit while a grandchild still holds stdout. The
// relay must not wait on a process that is already gone.
func TestRelayEndsThoughAGrandchildHoldsStdout(t *testing.T) {
	skipOnWindows(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := New(Config{Command: "sh", Args: []string{"-c", "sleep 25 & exit 0"}})
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ours, theirs := net.Pipe()
	defer func() { _ = ours.Close() }()
	go func() { _, _ = io.Copy(io.Discard, ours) }()

	done := make(chan struct{})
	go func() {
		_ = c.Attach(ctx, theirs)
		close(done)
	}()

	// stopGrace for WaitDelay, plus room for the stop sequence.
	select {
	case <-done:
	case <-time.After(4 * stopGrace):
		t.Fatal("relay never ended, though the process exited immediately")
	}
}

var _ io.ReadWriter = (net.Conn)(nil)
