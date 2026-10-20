package main

import (
	"context"
	"io"

	"github.com/meandr-inc/meandr-cli/internal/pty"
	"github.com/meandr-inc/meandr-cli/internal/tunnel"
)

// runPTY starts the command before the first dial and keeps it for the cli's
// whole life: every session the service opens attaches to it, and when it
// exits, so does the cli.
func runPTY(ctx context.Context, args []string) error {
	ctx, stop := signalContext(ctx)
	defer stop()

	s, err := prepare(ctx, "pty", args, "claude")
	if err != nil {
		return err
	}

	proc, err := pty.Start(pty.Config{Command: s.argv[0], Args: s.argv[1:], Env: s.env, Storage: s.storage})
	if err != nil {
		return err
	}

	mgr := &tunnel.Manager{
		Dialer: s.dialer(tunnel.KindPTY),
		Spawn:  func(context.Context) (tunnel.Child, error) { return session{proc}, nil },
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- mgr.Run(runCtx) }()

	select {
	case <-proc.Done():
		return proc.Err()
	case err = <-ran: // a status redialling cannot fix
	case <-ctx.Done():
		err = ctx.Err()
	}
	_ = proc.Stop()
	return err
}

// session is one stream on the PTY, as the tunnel's Child: it is a viewer,
// not a process, so stopping it leaves the program running.
type session struct{ proc *pty.Process }

func (s session) Attach(ctx context.Context, stream io.ReadWriter) error {
	return s.proc.Attach(ctx, stream)
}

func (session) Stop() error          { return nil }
func (session) StderrTail() []string { return nil }
