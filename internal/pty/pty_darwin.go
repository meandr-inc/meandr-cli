package pty

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY opens a new PTY: the side this process reads and writes, and the
// terminal the program gets. Both are blocking files.
func openPTY() (master, tty *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("pty:open /dev/ptmx: %w", err)
	}
	master = os.NewFile(uintptr(fd), "/dev/ptmx")
	defer func() {
		if err != nil {
			_ = master.Close()
		}
	}()

	if err = unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return nil, nil, fmt.Errorf("pty:grant the pty: %w", err)
	}
	if err = unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return nil, nil, fmt.Errorf("pty:unlock the pty: %w", err)
	}

	// TIOCPTYGNAME fills a 128-byte buffer, and x/sys has no call for it.
	var buf [128]byte
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME),
		uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		err = errno
		return nil, nil, fmt.Errorf("pty:name the pty: %w", err)
	}
	end := bytes.IndexByte(buf[:], 0)
	if end <= 0 {
		err = errors.New("pty: the pty has no name")
		return nil, nil, err
	}

	name := string(buf[:end])
	tfd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("pty:open %s: %w", name, err)
	}
	return master, os.NewFile(uintptr(tfd), name), nil
}
