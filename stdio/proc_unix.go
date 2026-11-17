//go:build !windows

package stdio

import (
	"os"
	"os/exec"
	"syscall"
)

// environ is the environment the child inherits. The caller has already
// removed the tunnel token; an MCP server's own variables must pass
// through, so nothing else is filtered.
func environ() []string { return os.Environ() }

// setProcessGroup puts the child in its own group, so signals reach what
// it spawned. Wrappers such as npx exec the real server as a grandchild.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateGroup and killGroup signal the negative pid, which addresses
// the process group. Signalling cmd.Process alone would leave the
// grandchild running.
func terminateGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}

// wasSignalled reports whether the child died from a signal. We sent it,
// so that is not a failure.
func wasSignalled(exit *exec.ExitError) bool {
	status, ok := exit.ProcessState.Sys().(syscall.WaitStatus)
	return ok && status.Signaled()
}
