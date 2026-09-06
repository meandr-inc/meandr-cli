package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/meandr-inc/meandr-cli/internal/config"
	"github.com/meandr-inc/meandr-cli/internal/stdio"
	"github.com/meandr-inc/meandr-cli/internal/tunnel"
)

// defaultEndpoint is set at build time; see ENDPOINT in the Makefile.
var defaultEndpoint = "tun.meandr.io"

// Endpoint reports the service address this binary was built for.
func Endpoint() string { return defaultEndpoint }

func runTunnel(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tunnel", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	tunnelID := fs.String("id", "", "tunnel ID, as shown in the dashboard")
	endpoint := fs.String("endpoint", defaultEndpoint, "service address to connect to")
	logs := bindLogFlags(fs)

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}

	// flag stops at "--", so the remaining arguments are the MCP server
	// command.
	command := fs.Args()

	switch {
	case *tunnelID == "":
		return fmt.Errorf("%w: tunnel requires --id <tunnel-id>", errUsage)
	case len(command) == 0:
		return fmt.Errorf("%w: tunnel requires a command after `--`, e.g.\n"+
			"  meandr tunnel --id %s -- npx -y @modelcontextprotocol/server-filesystem /srv/data",
			errUsage, *tunnelID)
	}

	if err := logs.install(); err != nil {
		return err
	}

	token, err := config.Token(*tunnelID)
	if err != nil {
		return err
	}

	// The MCP server inherits this process's environment. Clear the token
	// so it is never passed to a program we did not write.
	if os.Getenv(config.EnvToken) != "" {
		if err := os.Unsetenv(config.EnvToken); err != nil {
			return fmt.Errorf("clear %s from the environment: %w", config.EnvToken, err)
		}
	}

	slog.Info("starting",
		slog.String("tunnel", *tunnelID),
		slog.String("endpoint", *endpoint),
		slog.String("command", strings.Join(command, " ")))

	mgr := &tunnel.Manager{
		Dialer: &tunnel.Dialer{
			Endpoint: *endpoint,
			TunnelID: *tunnelID,
			Token:    token,
		},
		Spawn: func(ctx context.Context) (tunnel.Child, error) {
			child := stdio.New(stdio.Config{Command: command[0], Args: command[1:]})
			if err := child.Start(ctx); err != nil {
				return nil, err
			}
			return child, nil
		},
	}
	return mgr.Run(ctx)
}
