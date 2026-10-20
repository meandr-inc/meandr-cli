//go:build !linux && !darwin

package pty

import (
	"errors"
	"os"
	"os/exec"
)

// Windows is not released, and the build stays green so the tests can run
// there; this package opens a PTY on Linux and macOS only.
var errUnsupported = errors.New("pty: runs on Linux and macOS")

const noFollow = 0

func openPTY() (*os.File, *os.File, error)           { return nil, nil, errUnsupported }
func setControllingTerminal(*exec.Cmd)               {}
func setSize(*os.File, uint16, uint16) (bool, error) { return false, errUnsupported }
func redraw(*os.File, int)                           {}
func terminateGroup(int)                             {}
func killGroup(int)                                  {}
