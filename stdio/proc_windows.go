//go:build windows

package stdio

import (
	"os"
	"os/exec"
)

// Windows is not a release target yet, but the build stays green so the
// tests can run there. There is no process group and no SIGTERM, so a
// child is killed outright. Doing it properly needs a Job Object.

func environ() []string { return os.Environ() }

func setProcessGroup(*exec.Cmd) {}

func terminateGroup(cmd *exec.Cmd) { killGroup(cmd) }

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// No signal status to inspect, so every non-zero exit reads as a failure.
func wasSignalled(*exec.ExitError) bool { return false }
