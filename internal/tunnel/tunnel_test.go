package tunnel_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meandr-inc/meandr-cli/internal/tunnel"
	"github.com/meandr-inc/meandr-cli/internal/tunnel/edgetest"
)

// echoChild stands in for a spawned MCP server: it copies bytes back and
// records that it was stopped. Not a process, since these tests are
// about the tunnel.
type echoChild struct {
	mu      sync.Mutex
	stopped bool
	started chan struct{}
}

func newEchoChild() *echoChild { return &echoChild{started: make(chan struct{})} }

func (c *echoChild) Attach(ctx context.Context, stream io.ReadWriter) error {
	close(c.started)
	_, err := io.Copy(stream, stream)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (c *echoChild) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	return nil
}

func (c *echoChild) Stopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

func (c *echoChild) StderrTail() []string { return nil }

func dialerFor(t *testing.T, e *edgetest.Edge) *tunnel.Dialer {
	t.Helper()
	return &tunnel.Dialer{
		Endpoint: e.Addr(),
		TunnelID: "01a03496-tunnel",
		Token:    "tok-secret",
		RootCAs:  e.Roots(),
	}
}

// These headers are the handshake contract with the service.
func TestUpgradeSendsTheExpectedHeaders(t *testing.T) {
	e := edgetest.Start(t)
	conn, info, err := dialerFor(t, e).Dial()
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	req := e.LastRequest()
	if req == nil {
		t.Fatal("edge saw no request")
	}
	for header, want := range map[string]string{
		"Upgrade":          "meandr-tunnel/1",
		"Connection":       "Upgrade",
		"Authorization":    "Bearer tok-secret",
		"Meandr-Tunnel-Id": "01a03496-tunnel",
	} {
		if got := req.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	// The tunnel id is the only identifier the cli sends.
	if got := req.Header.Get("Meandr-Tunnel-Server"); got != "" {
		t.Errorf("cli sent a server id: %q", got)
	}
	if req.Header.Get("Meandr-Tunnel-Instance") == "" {
		t.Error("no instance id sent")
	}
	if info.Node != "" || info.Region != "" {
		t.Logf("edge advice: node=%q region=%q", info.Node, info.Region)
	}
}

func TestUpgradeReadsAdvice(t *testing.T) {
	e := edgetest.Start(t)
	e.Headers = map[string]string{
		"Meandr-Node":               "ASD123F",
		"Meandr-Region":             "eu-central-1",
		"Meandr-Tunnel-Connections": "3",
	}
	conn, info, err := dialerFor(t, e).Dial()
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if info.Node != "ASD123F" || info.Region != "eu-central-1" || info.Conns != 3 {
		t.Errorf("advice = %+v", info)
	}
}

// A revoked token must stop the cli dialling, not start a retry loop.
func TestFatalStatusesStopDialling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"revoked token", 401},
		{"unknown tunnel", 404},
		{"binary too old", 426},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := edgetest.Start(t)
			e.Status = tc.status

			_, _, err := dialerFor(t, e).Dial()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !tunnel.IsFatal(err) {
				t.Errorf("status %d should be fatal, got %v", tc.status, err)
			}
		})
	}
}

func TestRetryableStatuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		wantFor time.Duration
	}{
		{"connection cap", 429, 30 * time.Second},
		{"node draining", 503, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := edgetest.Start(t)
			e.Status = tc.status

			_, _, err := dialerFor(t, e).Dial()
			if err == nil {
				t.Fatal("expected an error")
			}
			if tunnel.IsFatal(err) {
				t.Fatalf("status %d should be retryable", tc.status)
			}
			var r *tunnel.Retryable
			if !errors.As(err, &r) {
				t.Fatalf("want Retryable, got %T", err)
			}
			if r.After != tc.wantFor {
				t.Errorf("backoff hint = %v, want %v", r.After, tc.wantFor)
			}
		})
	}
}

// An opened stream is the spawn signal, and bytes pass through untouched.
func TestStreamSpawnsAChildAndPipesBytes(t *testing.T) {
	e := edgetest.Start(t)
	conn, info, err := dialerFor(t, e).Dial()
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	child := newEchoChild()
	tc, err := tunnel.NewConn(conn, info, func(context.Context) (tunnel.Child, error) {
		return child, nil
	})
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tc.Serve(ctx) }()

	stream := e.OpenStream(t)
	select {
	case <-child.started:
	case <-time.After(5 * time.Second):
		t.Fatal("opening a stream did not spawn a child")
	}

	const payload = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	if _, err := stream.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != payload {
		t.Errorf("bytes altered in transit:\n got %q\nwant %q", got, payload)
	}
	_ = stream.Close()
}

// Closing the stream must stop the child, so no process is orphaned.
func TestClosingTheStreamStopsTheChild(t *testing.T) {
	e := edgetest.Start(t)
	conn, info, err := dialerFor(t, e).Dial()
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	child := newEchoChild()
	tc, err := tunnel.NewConn(conn, info, func(context.Context) (tunnel.Child, error) {
		return child, nil
	})
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tc.Serve(ctx) }()

	stream := e.OpenStream(t)
	<-child.started
	_ = stream.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tc.Children() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child still running %v after the stream closed", 5*time.Second)
}

// cancelledChild mimics what stdio.Child does on shutdown: it returns
// the context's error once cancelled.
type cancelledChild struct{ started chan struct{} }

func (c *cancelledChild) Attach(ctx context.Context, _ io.ReadWriter) error {
	close(c.started)
	<-ctx.Done()
	return ctx.Err()
}
func (c *cancelledChild) Stop() error          { return nil }
func (c *cancelledChild) StderrTail() []string { return nil }

// Ctrl-C is a clean exit and the process reports 0 for it, so the log has
// to agree: a cancelled session is not a failure.
func TestShutdownIsNotLoggedAsAnError(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	e := edgetest.Start(t)
	conn, info, err := dialerFor(t, e).Dial()
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	child := &cancelledChild{started: make(chan struct{})}
	tc, err := tunnel.NewConn(conn, info, func(context.Context) (tunnel.Child, error) {
		return child, nil
	})
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { _ = tc.Serve(ctx); close(served) }()

	s := e.OpenStream(t)
	defer func() { _ = s.Close() }()
	select {
	case <-child.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the child never started")
	}

	cancel()
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve never returned")
	}

	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("shutdown logged an error:\n%s", buf.String())
	}
}

// On the system roots, an unknown certificate must be refused.
func TestUnknownCertificateIsRefused(t *testing.T) {
	e := edgetest.Start(t)
	d := dialerFor(t, e)
	d.RootCAs = nil

	_, _, err := d.Dial()
	if err == nil {
		t.Fatal("dialled an edge with an untrusted certificate")
	}
	if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Errorf("expected a certificate error, got %v", err)
	}
}

var _ net.Conn = (net.Conn)(nil)
