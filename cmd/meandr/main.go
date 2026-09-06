// Command meandr connects a locally-running MCP server to the meandr
// service. It spawns the server as a child process and carries its stdio
// session over an outbound tunnel.
//
// It opens no listening socket. Every connection is outbound, so a host
// running this needs no inbound firewall rule.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/meandr-inc/meandr-cli/internal/config"
	"github.com/meandr-inc/meandr-cli/internal/version"
)

// Exit codes, distinct so a supervisor can tell what is worth a restart.
const (
	exitOK       = 0
	exitRuntime  = 1 // ran, then failed: network, or the MCP server exited
	exitUsage    = 2 // bad flags or bad configuration
	exitNotFound = 3 // no credential for this tunnel
)

func main() {
	version.FromBuildInfo()
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitUsage
	}

	// A second signal is left to the default handler, so Ctrl-C twice
	// always ends the process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, rest := args[0], args[1:]

	var err error
	switch cmd {
	case "tunnel":
		err = runTunnel(ctx, rest)
	case "configure":
		err = runConfigure(ctx, rest)
	case "version", "--version", "-v":
		fmt.Println(version.String())
		fmt.Printf("edge %s\n", Endpoint())
		return exitOK
	case "help", "--help", "-h":
		usage(os.Stdout)
		return exitOK
	default:
		_, _ = fmt.Fprintf(os.Stderr, "meandr: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		return exitUsage
	}

	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, context.Canceled):
		// Shutdown on a signal is a clean exit, not a crash.
		return exitOK
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(os.Stderr, "meandr: %v\n", err)
		return exitUsage
	case errors.Is(err, config.ErrNoCredential):
		_, _ = fmt.Fprintf(os.Stderr, "meandr: %v\n", err)
		return exitNotFound
	default:
		_, _ = fmt.Fprintf(os.Stderr, "meandr: %v\n", err)
		return exitRuntime
	}
}

// errUsage marks bad flags or bad configuration. Commands wrap it rather
// than returning an exit code, so the mapping stays in run.
var errUsage = errors.New("usage")

func usage(w *os.File) {
	_, _ = fmt.Fprint(w, `meandr - connect a local MCP server to the meandr service

USAGE
  meandr configure [--id <tunnel-id>]
  meandr tunnel --id <tunnel-id> [flags] -- <command> [args...]
  meandr version
  meandr help

COMMANDS
  configure Prompt for a tunnel ID and its token, and store the token in
            the credentials file with 0600 permissions. Both come from
            the dashboard. Pass --id to be asked only for the token.

  tunnel    Spawn the MCP server and hold the tunnel open until signalled.
            Runs in the foreground, for systemd or a container entrypoint.

TUNNEL FLAGS
  --id <tunnel-id>    Tunnel to connect, as shown in the dashboard. Required.
  --endpoint <host>   Service address. Defaults to the address this binary
                      was built for, which "meandr version" prints.
  --log-level <level> debug | info | warn | error. Default info.
  --log-format <fmt>  text | json. Default text.

  Both logging flags are accepted by "configure" too. Logs go to stderr.

CREDENTIALS
  Checked in order, first match wins:

    MEANDR_AUTH_TOKEN       environment variable
    ~/.meandr/credentials   written by "meandr configure", keyed by tunnel id

  The MCP server inherits this process's environment, so MEANDR_AUTH_TOKEN
  is removed before spawning it.

EXIT CODES
  0  clean exit, including shutdown on SIGINT or SIGTERM
  1  runtime failure: network, or the MCP server exited
  2  bad flags or bad configuration
  3  no credential found for this tunnel

EXAMPLE
  meandr tunnel --id 01a07246-2c9f-7000-0000-000000000000 -- \
    npx -y @modelcontextprotocol/server-filesystem /srv/data
`)
}
