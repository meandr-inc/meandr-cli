// Package stdio runs an MCP server as a child process and connects its
// stdin and stdout to a stream.
//
// Bytes are copied, never parsed, so a change to MCP does not reach this
// code.
//
// One process per stream. An MCP server on stdio has one client by
// construction: the pipe is the session, and a second client on the same
// pipe replaces the first rather than joining it.
package stdio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	// stopGrace is how long the process gets at each step of shutdown.
	stopGrace = 5 * time.Second

	// stderrLines is how many recent stderr lines to keep for diagnostics.
	stderrLines = 20

	// maxStderrLine bounds a line that never ends.
	maxStderrLine = 1 << 20
)

// Config describes the child to run.
type Config struct {
	Command string
	Args    []string
	Env     []string // extra variables, appended to this process's environment
	Dir     string
}

// Child is one MCP server process, bound to one stream.
type Child struct {
	cfg  Config
	tail *stderrTail

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	waitDone chan error

	stopOnce sync.Once
	stopErr  error
}

// New prepares a child. Nothing is spawned until Start.
func New(cfg Config) *Child {
	return &Child{cfg: cfg, tail: &stderrTail{command: cfg.Command}}
}

// Start spawns the process.
func (c *Child) Start(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, c.cfg.Command, c.cfg.Args...)
	cmd.Dir = c.cfg.Dir
	cmd.Env = append(environ(), c.cfg.Env...)

	// A process group, so signals reach what the command spawned. Wrappers
	// such as npx exec the real server as a grandchild.
	setProcessGroup(cmd)

	// Without this, cancelling ctx kills only the direct process, which
	// leaves that grandchild running.
	cmd.Cancel = func() error {
		terminateGroup(cmd)
		return nil
	}

	// A grandchild inherits stdout and can hold it open after the child
	// exits. Without a delay the relay never sees EOF and the stream
	// never ends.
	cmd.WaitDelay = stopGrace

	// Our own pipes rather than StdinPipe/StdoutPipe. Wait closes the
	// ones it creates as soon as the process exits, which discards
	// whatever the child had already written — its last response. These
	// it never touches, so Wait can run from here and the relay still
	// drains the pipe to EOF.
	inR, inW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("stdio: stdin pipe: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_, _ = inR.Close(), inW.Close()
		return fmt.Errorf("stdio: stdout pipe: %w", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, c.tail

	if err := cmd.Start(); err != nil {
		_, _ = inR.Close(), inW.Close()
		_, _ = outR.Close(), outW.Close()
		return fmt.Errorf("stdio: start %s: %w", c.cfg.Command, err)
	}

	// The child holds its own ends now.
	_, _ = inR.Close(), outW.Close()

	waitDone := make(chan error, 1)
	c.mu.Lock()
	c.cmd, c.stdin, c.stdout, c.waitDone = cmd, inW, outR, waitDone
	c.mu.Unlock()

	// Wait runs from here so the process is reaped exactly once, and so
	// a grandchild cannot strand the relay: once the process is gone,
	// only an inherited write end can still hold the pipe open, and
	// after a grace we close our side to end the copy.
	go func() {
		err := cmd.Wait()
		time.AfterFunc(stopGrace, func() { _ = outR.Close() })
		waitDone <- err
	}()

	slog.Info("MCP server started",
		slog.String("command", c.cfg.Command),
		slog.Int("pid", cmd.Process.Pid))

	return nil
}

// Attach copies bytes between stream and the process until either end
// finishes, then stops the process. It returns when stdout closes.
func (c *Child) Attach(ctx context.Context, stream io.ReadWriter) error {
	c.mu.Lock()
	stdin, stdout := c.stdin, c.stdout
	c.mu.Unlock()

	if stdin == nil || stdout == nil {
		return errors.New("stdio: not started")
	}

	// stream → process. Closing stdin lets a well-behaved server exit on
	// its own, without a signal.
	inDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(stdin, stream)
		_ = stdin.Close()
		inDone <- err

		// The stream is gone, so nothing will read whatever the process
		// writes next. Otherwise, run on until yamux resets the stream five
		// minutes later, reporting an error.
		_ = c.Stop()
	}()

	// process → stream. This direction decides when the session is over.
	_, outErr := io.Copy(stream, stdout)

	stopErr := c.Stop()

	select {
	case inErr := <-inDone:
		// A copy ending because the peer went away is normal.
		if outErr == nil && !isExpectedClose(inErr) {
			outErr = inErr
		}
	case <-time.After(time.Second):
		// Blocked reading a stream nobody will write to again. Stop has
		// already closed stdin.
	}

	if outErr != nil && !isExpectedClose(outErr) {
		return fmt.Errorf("stdio: relay: %w", outErr)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return stopErr
}

// Stop ends the process: close stdin, then terminate, then kill. Safe to
// call more than once.
func (c *Child) Stop() error {
	c.stopOnce.Do(func() {
		c.stopErr = c.stop()

		c.mu.Lock()
		cmd := c.cmd
		c.mu.Unlock()
		if cmd == nil || cmd.Process == nil {
			return
		}
		// Pairs with the start line, so every process an operator sees
		// can be accounted for.
		ev := slog.Info
		attrs := []any{
			slog.String("command", c.cfg.Command),
			slog.Int("pid", cmd.Process.Pid),
		}
		if c.stopErr != nil {
			ev = slog.Warn
			attrs = append(attrs, slog.String("error", c.stopErr.Error()))
		}
		ev("MCP server stopped", attrs...)
	})
	return c.stopErr
}

func (c *Child) stop() error {
	c.mu.Lock()
	cmd, stdin, exited := c.cmd, c.stdin, c.waitDone
	c.mu.Unlock()

	if cmd == nil || cmd.Process == nil || exited == nil {
		return nil
	}
	// Closing stdin lets a well-behaved server exit without a signal.
	if stdin != nil {
		_ = stdin.Close()
	}

	select {
	case err := <-exited:
		return exitError(err)
	case <-time.After(stopGrace):
	}

	if err, done := reaped(exited); done {
		return err
	}
	// Unlogged: plenty of servers hold a timer and cannot exit on stdin
	// close, so signalling them is the ordinary path. The stop line says
	// the process is gone, which is all anyone needs.
	terminateGroup(cmd)

	select {
	case err := <-exited:
		return exitError(err)
	case <-time.After(stopGrace):
	}

	if err, done := reaped(exited); done {
		return err
	}
	slog.Warn("no exit on terminate; killing",
		slog.Int("pid", cmd.Process.Pid))
	killGroup(cmd)
	return exitError(<-exited)
}

// reaped re-checks immediately before signalling. Once the process has
// been waited for, its id can be reused, and the signal goes to the
// group that inherited it rather than to ours.
func reaped(exited <-chan error) (error, bool) {
	select {
	case err := <-exited:
		return exitError(err), true
	default:
		return nil, false
	}
}

// StderrTail returns the process's most recent stderr lines.
func (c *Child) StderrTail() []string { return c.tail.snapshot() }

// stderrTail keeps the last few stderr lines. Lines are kept rather than
// logged as they arrive, because a chatty server would bury our own
// output; they surface when the process fails.
type stderrTail struct {
	command string

	mu      sync.Mutex
	partial []byte
	lines   []string
}

func (s *stderrTail) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.partial = append(s.partial, p...)

	start := 0
	for {
		i := bytes.IndexByte(s.partial[start:], '\n')
		if i < 0 {
			break
		}
		line := string(s.partial[start : start+i])
		start += i + 1

		s.lines = append(s.lines, line)
		if len(s.lines) > stderrLines {
			s.lines = s.lines[len(s.lines)-stderrLines:]
		}
		slog.Debug("stderr",
			slog.String("command", s.command),
			slog.String("line", line))
	}
	if start > 0 {
		s.partial = s.partial[:copy(s.partial, s.partial[start:])]
	}

	// Bound it if a newline never arrives.
	if len(s.partial) > maxStderrLine {
		s.partial = s.partial[:0]
	}
	return len(p), nil
}

func (s *stderrTail) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.lines))
	copy(out, s.lines)
	return out
}

// isExpectedClose reports the ordinary ways a copy ends. os.ErrClosed is
// among them: WaitDelay closes the pipes when a grandchild is still
// holding one open.
func isExpectedClose(err error) bool {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, os.ErrClosed)
}

// exitError reports a non-zero exit. A signalled exit is not an error:
// we sent the signal.
func exitError(err error) error {
	if err == nil {
		return nil
	}
	// WaitDelay elapsed with the pipes still held. Our own policy, not
	// a failure of the process.
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if exit.ProcessState.Sys() != nil && wasSignalled(exit) {
			return nil
		}
		return fmt.Errorf("exited with status %d", exit.ExitCode())
	}
	return err
}
