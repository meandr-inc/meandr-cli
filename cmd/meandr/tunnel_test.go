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
