//go:build !windows

package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// meandrArgs, set in a child's environment, makes this test binary run as
// meandr with those arguments, one per line.
const meandrArgs = "MEANDR_TEST_ARGS"

func TestMain(m *testing.M) {
	if args, ok := os.LookupEnv(meandrArgs); ok {
		os.Exit(run(strings.Split(args, "\n")))
	}
	os.Exit(m.Run())
}

// Nothing marks a piped read as started, so the child is given this long
// to reach it before it is signalled.
const settle = 300 * time.Millisecond

func TestCtrlCEndsConfigureAtThePrompt(t *testing.T) {
	c := startMeandr(t, "configure", "--id", "01a0-tunnel")
	time.Sleep(settle)

	c.signal(t, os.Interrupt)
	c.wantKilledBy(t, syscall.SIGINT)
}

func TestFirstSignalEndsTheTunnelCleanly(t *testing.T) {
	addr, dialled := fakeEdge(t, func(conn net.Conn) { _ = conn.Close() })
	c := startMeandr(t, "tunnel", "--id", "01a0-tunnel", "--endpoint", addr, "--", "true")
	c.await(t, dialled)

	c.signal(t, os.Interrupt)
	c.wait(t)
	if code := c.cmd.ProcessState.ExitCode(); code != exitOK {
		t.Fatalf("exit = %v, want %d; stderr:\n%s", c.err, exitOK, c.stderr.String())
	}
}

// An edge that never answers the handshake holds the run for 30 s after
// cancellation, as a slow drain would.
func TestSecondSignalEndsADrainingTunnel(t *testing.T) {
	addr, dialled := fakeEdge(t, func(conn net.Conn) {
		go func() { _, _ = io.Copy(io.Discard, conn); _ = conn.Close() }()
	})
	c := startMeandr(t, "tunnel", "--id", "01a0-tunnel", "--endpoint", addr, "--", "true")
	c.await(t, dialled)

	c.signal(t, os.Interrupt)
	select {
	case <-c.done:
		t.Fatalf("the first signal ended the process (%v); want it to drain", c.err)
	case <-time.After(settle):
	}

	c.signal(t, os.Interrupt)
	c.wantKilledBy(t, syscall.SIGINT)
}

type meandrProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser // held open and never written to
	stderr bytes.Buffer
	done   chan struct{}
	err    error
}

func startMeandr(t *testing.T, args ...string) *meandrProcess {
	t.Helper()
	if signal.Ignored(os.Interrupt) {
		t.Skip("SIGINT is ignored here, and the child would inherit that")
	}

	c := &meandrProcess{cmd: exec.Command(os.Args[0]), done: make(chan struct{})}
	c.cmd.Env = append(os.Environ(),
		meandrArgs+"="+strings.Join(args, "\n"),
		"MEANDR_AUTH_TOKEN=tun_test",
		"MEANDR_CONFIG_DIR="+t.TempDir())
	c.cmd.Stderr = &c.stderr

	var err error
	if c.stdin, err = c.cmd.StdinPipe(); err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := c.cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	go func() { c.err = c.cmd.Wait(); close(c.done) }()

	t.Cleanup(func() {
		_ = c.cmd.Process.Kill()
		<-c.done
	})
	return c
}

func (c *meandrProcess) signal(t *testing.T, sig os.Signal) {
	t.Helper()
	if err := c.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal %v: %v", sig, err)
	}
}

func (c *meandrProcess) await(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-c.done:
		t.Fatalf("exited early: %v; stderr:\n%s", c.err, c.stderr.String())
	case <-time.After(5 * time.Second):
		t.Fatal("not ready within 5 s")
	}
}

func (c *meandrProcess) wait(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("still running 5 s after the signal")
	}
}

func (c *meandrProcess) wantKilledBy(t *testing.T, sig syscall.Signal) {
	t.Helper()
	c.wait(t)
	var exit *exec.ExitError
	if errors.As(c.err, &exit) {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == sig {
			return
		}
	}
	t.Fatalf("exit = %v, want killed by %v; stderr:\n%s", c.err, sig, c.stderr.String())
}

// fakeEdge accepts connections on loopback and hands each to serve. The
// channel closes on the first, once the tunnel is dialling.
func fakeEdge(t *testing.T, serve func(net.Conn)) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	dialled := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			once.Do(func() { close(dialled) })
			serve(conn)
		}
	}()
	return ln.Addr().String(), dialled
}
