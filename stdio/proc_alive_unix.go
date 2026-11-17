//go:build !windows

package stdio

import "syscall"

// processAlive is a test helper: signal 0 checks existence without
// delivering anything. Used to prove a child does not outlive its stream.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// killProcess is a test helper, so a failing test does not leave the
// process it was asserting about behind.
func killProcess(pid int) {
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
