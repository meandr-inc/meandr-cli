//go:build !linux

// Package container is what meandr does as a container's first process: its
// stop reaches every process left in the container, as an init's does.
package container

import "time"

// StopRest does nothing outside Linux, where meandr runs in no container.
func StopRest(time.Duration) {}
