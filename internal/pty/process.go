// Package pty runs one program on a PTY for as long as the process lives,
// and attaches every session's stream to it. Bytes are relayed, never
// parsed, recorded or logged.
package pty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// readChunk is the most one read of the PTY takes.
	readChunk = 32 << 10

	// queueFrames is how far a viewer may fall behind before the PTY waits
	// for it.
	queueFrames = 64

	// stopGrace is how long the program gets between SIGTERM and SIGKILL.
	stopGrace = 5 * time.Second

	// The size until the first viewer sends its own.
	defaultCols, defaultRows = 80, 24

	defaultTerm = "xterm-256color"
)

// stallLimit is how long the PTY waits on a viewer that stopped reading
// before dropping it. A variable for tests; a Process keeps its own copy.
var stallLimit = 10 * time.Second

// Config is the program to run.
type Config struct {
	Command string
	Args    []string
	Env     []string // extra variables, appended to this process's environment
	Dir     string
}

// Process is the program on a PTY that is always read: with nobody
// attached its output is dropped, so it never blocks on its screen.
type Process struct {
	name   string
	cmd    *exec.Cmd
	master *os.File
	stall  time.Duration

	inMu   sync.Mutex // one viewer's input at a time
	sizeMu sync.Mutex

	mu      sync.Mutex
	viewers map[*viewer]struct{}

	stopping atomic.Bool
	stopOnce sync.Once
	done     chan struct{}
	err      error // how the program exited, once done is closed
}

// viewer is one attached stream.
type viewer struct {
	out  chan []byte   // frames for its stream
	gone chan struct{} // closed when it ends, from either side
	once sync.Once
}

func (v *viewer) end() { v.once.Do(func() { close(v.gone) }) }

// Start runs the program on a new PTY.
func Start(cfg Config) (*Process, error) {
	master, tty, err := openPTY()
	if err != nil {
		return nil, err
	}
	if _, err := setSize(master, defaultCols, defaultRows); err != nil {
		_, _ = master.Close(), tty.Close()
		return nil, fmt.Errorf("pty: size the pty: %w", err)
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = append(os.Environ(), cfg.Env...)
	// Every viewer is a browser terminal, xterm-compatible. Without a TERM,
	// tmux and its like refuse to draw at all.
	if !termSet(cmd.Env) {
		cmd.Env = append(cmd.Env, "TERM="+defaultTerm)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	setControllingTerminal(cmd)

	err = cmd.Start()
	_ = tty.Close() // the program holds its own
	if err != nil {
		_ = master.Close()
		return nil, fmt.Errorf("pty: start %s: %w", cfg.Command, err)
	}

	p := &Process{
		name:    cfg.Command,
		cmd:     cmd,
		master:  master,
		stall:   stallLimit,
		viewers: map[*viewer]struct{}{},
		done:    make(chan struct{}),
	}
	go p.pump()
	go p.wait()

	slog.Info("pty started",
		slog.String("command", cfg.Command),
		slog.Int("pid", cmd.Process.Pid))
	return p, nil
}

// Done is closed once the program has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// Err waits for the program to exit and reports how: nil for status 0, or
// for any exit once Stop was asked for.
func (p *Process) Err() error {
	<-p.done
	return p.err
}

// Stop ends the program, SIGTERM to its process group and SIGKILL after
// stopGrace, and returns once it has exited. Safe to call more than once.
func (p *Process) Stop() error {
	p.stopOnce.Do(func() {
		p.stopping.Store(true)
		select {
		case <-p.done:
			return
		default:
		}
		terminateGroup(p.cmd.Process.Pid)

		t := time.NewTimer(stopGrace)
		defer t.Stop()
		select {
		case <-p.done:
		case <-t.C:
			slog.Warn("no exit on terminate; killing", slog.Int("pid", p.cmd.Process.Pid))
			killGroup(p.cmd.Process.Pid)
		}
	})
	return p.Err()
}

// Attach relays one stream until it ends: the program's output to it, its
// frames to the program. The program runs on. Only a bad frame is an error.
func (p *Process) Attach(ctx context.Context, stream io.ReadWriter) error {
	v := &viewer{out: make(chan []byte, queueFrames), gone: make(chan struct{})}
	if !p.join(v) {
		return nil // the program has exited
	}
	defer p.leave(v)
	slog.Debug("pty session attached")

	// Whatever ends the viewer closes its stream, which unblocks both
	// directions.
	stop := context.AfterFunc(ctx, v.end)
	defer stop()
	go func() {
		<-v.gone
		if c, ok := stream.(io.Closer); ok {
			_ = c.Close()
		}
	}()
	go v.output(stream)

	err := p.input(stream)
	v.end()
	slog.Debug("pty session detached")

	switch {
	case errors.Is(err, errFrame):
		return err
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		return nil
	}
}

func (p *Process) join(v *viewer) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return false
	default:
		p.viewers[v] = struct{}{}
		return true
	}
}

func (p *Process) leave(v *viewer) {
	p.mu.Lock()
	delete(p.viewers, v)
	p.mu.Unlock()
	v.end()
}

// output writes a viewer's frames to its stream until it ends.
func (v *viewer) output(w io.Writer) {
	for {
		select {
		case f := <-v.out:
			if _, err := w.Write(f); err != nil {
				v.end()
				return
			}
		case <-v.gone:
			return
		}
	}
}

// input hands one stream's frames to the program until the stream ends.
func (p *Process) input(r io.Reader) error {
	fr := frameReader{r: r}
	for {
		f, err := fr.next()
		if err != nil {
			return err
		}
		switch f.kind {
		case frameData:
			p.inMu.Lock()
			_, err = p.master.Write(f.data)
			p.inMu.Unlock()
		case frameResize:
			err = p.resize(f.cols, f.rows)
		}
		if err != nil {
			return err
		}
	}
}

// resize sizes the PTY. The kernel signals the program only on a change, so
// an unchanged size is signalled here: a new viewer's first frame is its
// size, and the redraw is how it gets the current screen.
func (p *Process) resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	p.sizeMu.Lock()
	defer p.sizeMu.Unlock()

	changed, err := setSize(p.master, cols, rows)
	if err != nil || changed {
		return err
	}
	select {
	case <-p.done:
	default:
		redraw(p.master, p.cmd.Process.Pid)
	}
	return nil
}

// pump reads the PTY until the program's side is closed by everything that
// held it, handing each read to the viewers attached at that moment.
func (p *Process) pump() {
	defer func() { _ = p.master.Close() }()

	buf := make([]byte, readChunk)
	for {
		n, err := p.master.Read(buf)
		if n > 0 {
			p.broadcast(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// broadcast queues one read for every attached viewer. A viewer whose queue
// is full holds the PTY for up to its stall limit, then is dropped.
func (p *Process) broadcast(b []byte) {
	p.mu.Lock()
	if len(p.viewers) == 0 {
		p.mu.Unlock()
		return
	}
	vs := make([]*viewer, 0, len(p.viewers))
	for v := range p.viewers {
		vs = append(vs, v)
	}
	p.mu.Unlock()

	f := dataFrame(b)
	for _, v := range vs {
		select {
		case v.out <- f:
			continue
		case <-v.gone:
			continue
		default:
		}

		t := time.NewTimer(p.stall)
		select {
		case v.out <- f:
		case <-v.gone:
		case <-t.C:
			slog.Info("pty dropped a session that stopped reading",
				slog.Duration("waited", p.stall))
			v.end()
		}
		t.Stop()
	}
}

// wait reaps the program and ends every viewer.
func (p *Process) wait() {
	err := p.exitError(p.cmd.Wait())

	p.mu.Lock()
	p.err = err
	close(p.done)
	for v := range p.viewers {
		v.end()
	}
	p.mu.Unlock()

	attrs := []any{slog.String("command", p.name), slog.Int("pid", p.cmd.Process.Pid)}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	slog.Info("pty exited", attrs...)
}

// termSet reports whether env, where the last entry for a name wins, gives
// TERM a value.
func termSet(env []string) bool {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], "TERM="); ok {
			return v != ""
		}
	}
	return false
}

// exitError reports an exit nobody asked for; once Stop is called, any exit
// is the one it asked for.
func (p *Process) exitError(err error) error {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case !errors.As(err, &exit):
		return err
	case p.stopping.Load():
		return nil
	}
	if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return fmt.Errorf("%s was killed by %v", p.name, ws.Signal())
	}
	return fmt.Errorf("%s exited with status %d", p.name, exit.ExitCode())
}
