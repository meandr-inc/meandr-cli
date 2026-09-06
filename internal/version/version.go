// Package version reports what this binary is, for "meandr version" and
// for the client identifier sent when connecting.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set with -ldflags at build time; see the Makefile. A plain "go build"
// leaves them as they are.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// Version is the release tag, or "dev".
func Version() string { return version }

// String is the one-line form printed by "meandr version".
func String() string {
	return fmt.Sprintf("meandr %s (%s, built %s, %s/%s, %s)",
		version, commit, date, runtime.GOOS, runtime.GOARCH, runtime.Version())
}

// UserAgent identifies the build to the service. Only the version: the
// build date and Go version would say nothing useful there.
func UserAgent() string {
	return "meandr-cli/" + version
}

// FromBuildInfo recovers the version for a binary installed with "go
// install", where the Makefile's ldflags never ran.
func FromBuildInfo() {
	if version != "dev" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return
	}
	version = info.Main.Version
}
