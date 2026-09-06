package main

import (
	"io"
	"strings"
	"testing"
)

// The token is read straight from the file descriptor after this, so the
// prompt must not consume a byte past its own newline. A buffered read
// would swallow a pasted token and leave the password read with nothing.
func TestPromptDoesNotReadPastTheNewline(t *testing.T) {
	r := strings.NewReader("01a0-tunnel\ntun_secret\n")

	got, err := prompt(r, "Tunnel ID: ")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got != "01a0-tunnel" {
		t.Errorf("prompt = %q, want the first line only", got)
	}

	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read the remainder: %v", err)
	}
	if string(rest) != "tun_secret\n" {
		t.Errorf("remainder = %q, want the token still unread", rest)
	}
}

func TestPromptTrimsAndAcceptsAMissingNewline(t *testing.T) {
	got, err := prompt(strings.NewReader("  01a0-tunnel  "), "Tunnel ID: ")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if got != "01a0-tunnel" {
		t.Errorf("prompt = %q", got)
	}
}

func TestPromptRefusesAnEmptyInput(t *testing.T) {
	if _, err := prompt(strings.NewReader(""), "Tunnel ID: "); err == nil {
		t.Error("no error for input that ended before anything was typed")
	}
}

// An unterminated line must not grow without bound.
func TestPromptRefusesAnOversizedLine(t *testing.T) {
	if _, err := prompt(strings.NewReader(strings.Repeat("x", maxPromptLine+10)), "Tunnel ID: "); err == nil {
		t.Error("no error for a line past the cap")
	}
}
