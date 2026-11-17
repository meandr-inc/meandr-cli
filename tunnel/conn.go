package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	// keepAlive proves the socket is alive. It must be well under the
	// idle timeout of any load balancer in the path.
	keepAlive = 30 * time.Second

	// drainGrace is how long running MCP servers get to finish after the
	// service closes the connection.
	drainGrace = 30 * time.Second
)

// Spawner starts the MCP server for one stream.
type Spawner func(ctx context.Context) (Child, error)

// Child is a running MCP server. The interface is a byte pipe: this
// package carries the protocol without reading it.
type Child interface {
	Attach(ctx context.Context, stream io.ReadWriter) error
	Stop() error
	StderrTail() []string
}

// Conn is one authenticated connection, multiplexed with yamux.
//
// This side is the yamux server: the service opens a stream when an agent
// needs the MCP server, and an accepted stream is the signal to start one.
type Conn struct {
	Info  Info
	Spawn Spawner

	session *yamux.Session
	kids    atomic.Int32
	wg      sync.WaitGroup
}

// NewConn wraps an authenticated socket, which the Conn owns from here.
func NewConn(conn net.Conn, info Info, spawn Spawner) (*Conn, error) {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = keepAlive
	cfg.LogOutput = io.Discard // yamux logs in its own format

	session, err := yamux.Server(conn, cfg)
	if err != nil {
		return nil, fmt.Errorf("tunnel: yamux server: %w", err)
	}
	return &Conn{Info: info, Spawn: spawn, session: session}, nil
}

// Serve accepts streams until the session ends or ctx is cancelled, then
// waits for the MCP servers it started.
func (c *Conn) Serve(ctx context.Context) error {
	slog.Info("connected",
		slog.String("node", c.Info.Node),
		slog.String("region", c.Info.Region))

	// Accept has no context, so closing the session is what unblocks it.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.session.Close()
		case <-done:
		}
	}()

	var acceptErr error
	for {
		stream, err := c.session.Accept()
		if err != nil {
			acceptErr = err
			break
		}
		c.handle(ctx, stream)
	}

	if !waitTimeout(&c.wg, drainGrace) {
		slog.Warn("disconnected with MCP servers still running",
			slog.Int("running", int(c.kids.Load())),
			slog.Duration("waited", drainGrace))
	}
	_ = c.session.Close()

	slog.Info("disconnected",
		slog.String("node", c.Info.Node),
		slog.String("reason", closeReason(ctx, acceptErr)))

	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// handle runs one stream: start an MCP server, pipe bytes, stop it.
//
// How many may run at once is the tunnel's concurrency setting, enforced
// by the service before it opens a stream. A second limit here could
// only disagree with it.
func (c *Conn) handle(ctx context.Context, stream net.Conn) {
	c.kids.Add(1)
	c.wg.Add(1)

	go func() {
		defer c.wg.Done()
		defer c.kids.Add(-1)
		defer func() { _ = stream.Close() }()

		child, err := c.Spawn(ctx)
		if err != nil {
			slog.Error("could not start the MCP server", slog.String("error", err.Error()))
			return
		}
		// Attach stops the child on its normal path; this covers the
		// early returns, where the process would otherwise never be
		// waited for and would linger as a zombie.
		defer func() { _ = child.Stop() }()

		switch err := child.Attach(ctx, stream); {
		case err == nil:
			slog.Debug("session ended")
		case errors.Is(err, context.Canceled):
			// Shutdown, not a failure. The exit code says the same.
			slog.Debug("session ended by shutdown")
		default:
			slog.Error("session ended with an error",
				slog.String("error", err.Error()),
				slog.Any("stderr", child.StderrTail()))
		}
	}()
}

// Children reports how many MCP servers are running.
func (c *Conn) Children() int { return int(c.kids.Load()) }

func closeReason(ctx context.Context, err error) string {
	switch {
	case ctx.Err() != nil:
		return "shutdown"
	case errors.Is(err, yamux.ErrSessionShutdown), errors.Is(err, io.EOF):
		return "closed by the service"
	case err != nil:
		return err.Error()
	default:
		return "closed"
	}
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
