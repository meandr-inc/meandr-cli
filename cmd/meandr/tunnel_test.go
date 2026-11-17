package main

import "testing"

// Hosted containers' commands and flags are not the cli's: it refuses them.
func TestTheHostedCommandsAndFlagsAreRefused(t *testing.T) {
	for _, args := range [][]string{
		{"pty", "--id", "01a0-agent", "--", "true"},
		{"http", "--id", "01a0-app", "--port", "8080", "--", "true"},
		{"tunnel", "--id", "01a0-tunnel", "--env-url", "http://127.0.0.1:1/", "--", "true"},
		{"tunnel", "--id", "01a0-tunnel", "--expand-env", "--", "true"},
		{"tunnel", "--id", "01a0-tunnel", "--storage", t.TempDir(), "--", "true"},
	} {
		if code := run(args); code != exitUsage {
			t.Errorf("%v: exit = %d, want %d", args, code, exitUsage)
		}
	}
}
