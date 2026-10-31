//go:build linux

// Package container is what meandr does as a container's first process: its
// stop reaches every process left in the container, as an init's does.
package container

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// inits are what a container runtime may run as PID 1 ahead of the entry
// point: Docker's --init, which ECS's initProcessEnabled is, and the like.
var inits = map[string]bool{"docker-init": true, "tini": true, "dumb-init": true, "catatonit": true}

// StopRest, in a container's first process (PID 1, or an init's child), sends
// SIGTERM to every process left once meandr's children stopped, then SIGKILL
// after wait: a daemon outside their process groups hears no other stop.
func StopRest(wait time.Duration) {
	r := &rest{proc: "/proc", self: os.Getpid(), parent: os.Getppid(), kill: syscall.Kill, sleep: time.Sleep}
	r.reap = r.reapOrphans
	r.stop(wait)
}

type rest struct {
	proc         string
	self, parent int
	kill         func(pid int, sig syscall.Signal) error
	reap         func()
	sleep        func(time.Duration)
}

func (r *rest) stop(wait time.Duration) {
	if !r.first() || len(r.others()) == 0 {
		return
	}
	// -1 is every process this one may signal but PID 1 and itself.
	_ = r.kill(-1, syscall.SIGTERM)
	for end := time.Now().Add(wait); time.Now().Before(end); r.sleep(50 * time.Millisecond) {
		r.reap()
		if len(r.others()) == 0 {
			return
		}
	}
	_ = r.kill(-1, syscall.SIGKILL)
}

// first reports whether meandr is the container's first process; a host's
// PID 1 is never one of inits.
func (r *rest) first() bool {
	if r.self == 1 {
		return true
	}
	if r.parent != 1 {
		return false
	}
	comm, err := os.ReadFile(filepath.Join(r.proc, "1", "comm"))
	return err == nil && inits[strings.TrimSpace(string(comm))]
}

// others are the processes left besides PID 1 and meandr, zombies aside.
func (r *rest) others() []int {
	entries, _ := os.ReadDir(r.proc)
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == 1 || pid == r.self {
			continue
		}
		stat, err := os.ReadFile(filepath.Join(r.proc, e.Name(), "stat"))
		if err != nil {
			continue
		}
		// The state follows the command name, in parentheses that may hold any byte.
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 || i+2 >= len(stat) || stat[i+2] == 'Z' || stat[i+2] == 'X' {
			continue
		}
		out = append(out, pid)
	}
	return out
}

// reapOrphans waits for the processes PID 1 inherits; an init's child
// inherits none.
func (r *rest) reapOrphans() {
	if r.self != 1 {
		return
	}
	for {
		var ws syscall.WaitStatus
		if pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil); pid <= 0 || err != nil {
			return
		}
	}
}
