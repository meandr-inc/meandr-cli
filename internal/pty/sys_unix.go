//go:build linux || darwin

package pty

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// The program leads a session of its own with the PTY as its controlling
// terminal, so job control and SIGWINCH work as on any terminal.
func setControllingTerminal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
}

// setSize sizes the PTY and reports whether that changed its size.
func setSize(f *os.File, cols, rows uint16) (changed bool, err error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return false, err
	}
	if cerr := rc.Control(func(fd uintptr) {
		var ws *unix.Winsize
		if ws, err = unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ); err != nil {
			return
		}
		if ws.Col == cols && ws.Row == rows {
			return
		}
		changed = true
		err = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: rows})
	}); cerr != nil {
		return false, cerr
	}
	return changed, err
}

// redraw sends SIGWINCH to the terminal's foreground process group, or to
// the program's own where the PTY does not name one (macOS).
func redraw(f *os.File, pid int) {
	pgrp := pid
	if rc, err := f.SyscallConn(); err == nil {
		_ = rc.Control(func(fd uintptr) {
			if g, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP); err == nil && g > 0 {
				pgrp = g
			}
		})
	}
	_ = syscall.Kill(-pgrp, syscall.SIGWINCH)
}

// The negative pid addresses the program's process group, so what it
// started in that group stops with it.
func terminateGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGTERM) }
func killGroup(pid int)      { _ = syscall.Kill(-pid, syscall.SIGKILL) }
