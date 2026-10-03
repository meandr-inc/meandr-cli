package main

import (
	"slices"
	"testing"
)

// A loaded token authenticates the tunnel and, like any token, never
// reaches the MCP server.
func TestALoadedTokenIsTakenOutOfTheServersEnvironment(t *testing.T) {
	token, vars := takeToken([]string{"A=1", "MEANDR_AUTH_TOKEN=tun_x", "B=2"})

	if token != "tun_x" {
		t.Fatalf("token = %q, want tun_x", token)
	}
	if want := []string{"A=1", "B=2"}; !slices.Equal(vars, want) {
		t.Fatalf("variables = %q, want %q", vars, want)
	}
}

func TestWithoutALoadedTokenTheVariablesAreKept(t *testing.T) {
	token, vars := takeToken([]string{"A=1"})

	if token != "" || !slices.Equal(vars, []string{"A=1"}) {
		t.Fatalf("takeToken = %q, %q; want no token and the variable kept", token, vars)
	}
}

func lookup(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := vars[name]
		return value, ok
	}
}

// A secret reaches the server as an argument without being written into it.
func TestExpandArgsReplacesKnownVariables(t *testing.T) {
	got := expandArgs([]string{"--token=${TOKEN}", "$USER_ID", "plain", "${EMPTY}x"},
		lookup(map[string]string{"TOKEN": "s3cret", "USER_ID": "42", "EMPTY": ""}))

	if want := []string{"--token=s3cret", "42", "plain", "x"}; !slices.Equal(got, want) {
		t.Fatalf("expandArgs = %q, want %q", got, want)
	}
}

// An argument that only looks like a reference is the server's own.
func TestExpandArgsLeavesTheUnknownAsWritten(t *testing.T) {
	in := []string{"${MISSING}", "$MISSING", "^a$", "cost: $5", "${", "${bad name}", "$$HOME"}
	got := expandArgs(in, lookup(map[string]string{"HOME": "/root"}))

	if want := []string{"${MISSING}", "$MISSING", "^a$", "cost: $5", "${", "${bad name}", "$HOME"}; !slices.Equal(got, want) {
		t.Fatalf("expandArgs = %q, want %q", got, want)
	}
}

// A value is substituted once, never read as references of its own.
func TestExpandArgsNeverExpandsAValue(t *testing.T) {
	got := expandArgs([]string{"${A}"}, lookup(map[string]string{"A": "${B}", "B": "no"}))

	if got[0] != "${B}" {
		t.Fatalf("expandArgs = %q, want the value as given", got)
	}
}

// Loaded variables come ahead of the process's own, as the server sees them.
func TestEnvironLookupPrefersLoadedVariables(t *testing.T) {
	t.Setenv("MEANDR_TEST_VAR", "from-process")
	t.Setenv("MEANDR_TEST_ONLY_PROCESS", "process")
	look := environLookup([]string{"MEANDR_TEST_VAR=loaded"})

	if v, _ := look("MEANDR_TEST_VAR"); v != "loaded" {
		t.Fatalf("lookup = %q, want the loaded value", v)
	}
	if v, _ := look("MEANDR_TEST_ONLY_PROCESS"); v != "process" {
		t.Fatalf("lookup = %q, want the process's value", v)
	}
}
