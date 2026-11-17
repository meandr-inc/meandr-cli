//go:build windows

package stdio

// Unimplemented on Windows; the tests that use these skip there, along
// with everything else needing a POSIX shell.
func processAlive(int) bool { return false }

func killProcess(int) {}
