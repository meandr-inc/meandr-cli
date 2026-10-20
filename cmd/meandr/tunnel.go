package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/meandr-inc/meandr-cli/internal/config"
	"github.com/meandr-inc/meandr-cli/internal/envurl"
	"github.com/meandr-inc/meandr-cli/internal/stdio"
	"github.com/meandr-inc/meandr-cli/internal/tunnel"
)

// defaultEndpoint is set at build time; see ENDPOINT in the Makefile.
var defaultEndpoint = "tun.meandr.io"

// Endpoint reports the service address this binary was built for.
func Endpoint() string { return defaultEndpoint }

func runTunnel(ctx context.Context, args []string) error {
	ctx, stop := signalContext(ctx)
	defer stop()

	s, err := prepare(ctx, "tunnel", args, "npx -y @modelcontextprotocol/server-filesystem /srv/data")
	if err != nil {
		return err
	}

	mgr := &tunnel.Manager{
		Dialer: s.dialer(""),
		Spawn: func(ctx context.Context) (tunnel.Child, error) {
			child := stdio.New(stdio.Config{Command: s.argv[0], Args: s.argv[1:], Env: s.env})
			if err := child.Start(ctx); err != nil {
				return nil, err
			}
			return child, nil
		},
	}
	return mgr.Run(ctx)
}

// signalContext ends ctx on the first SIGINT or SIGTERM, which starts the
// drain. The handler then goes, so a second reaches the default one and
// Ctrl-C twice always ends the process.
func signalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	context.AfterFunc(ctx, stop)
	return ctx, stop
}

// startup is what tunnel and pty both run from.
type startup struct {
	tunnelID string
	endpoint string
	token    string
	argv     []string // the command, expanded with --expand-env
	env      []string // --env-url's variables, the token taken out
	storage  string   // pty: where pasted files go
}

// prepare parses the flags, installs the logger, loads --env-url and
// resolves the token, which it leaves in no environment.
func prepare(ctx context.Context, name string, args []string, example string) (*startup, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	tunnelID := fs.String("id", "", "tunnel ID, as shown in the dashboard")
	endpoint := fs.String("endpoint", defaultEndpoint, "service address to connect to")
	envURL := fs.String("env-url", "", "load extra environment variables for the command from this URL")
	expandEnv := fs.Bool("expand-env", false, "expand $NAME and ${NAME} in the command from its environment")
	storage := new(string)
	if name == "pty" {
		storage = fs.String("storage", "", "write files pasted into the terminal into this directory")
	}
	logs := bindLogFlags(fs)

	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("%w: %v", errUsage, err)
	}

	// flag stops at "--", so the remaining arguments are the command.
	command := fs.Args()

	switch {
	case *tunnelID == "":
		return nil, fmt.Errorf("%w: %s requires --id <tunnel-id>", errUsage, name)
	case len(command) == 0:
		return nil, fmt.Errorf("%w: %s requires a command after `--`, e.g.\n"+
			"  meandr %s --id %s -- %s",
			errUsage, name, name, *tunnelID, example)
	}

	if err := logs.install(); err != nil {
		return nil, err
	}

	// Loaded variables count as set in this process's environment, ahead of
	// it: a loaded token authenticates the tunnel like any other.
	var env []string
	if *envURL != "" {
		var err error
		if env, err = (&envurl.Fetcher{}).Fetch(ctx, *envURL); err != nil {
			return nil, err
		}
		slog.Info("environment loaded", slog.Int("variables", len(env)))
	}

	token, env := takeToken(env)
	if token == "" {
		var err error
		if token, err = config.Token(*tunnelID); err != nil {
			return nil, err
		}
	}

	// The command inherits this process's environment. Clear the token so
	// it is never passed to a program we did not write.
	if os.Getenv(config.EnvToken) != "" {
		if err := os.Unsetenv(config.EnvToken); err != nil {
			return nil, fmt.Errorf("clear %s from the environment: %w", config.EnvToken, err)
		}
	}

	// Logged as written: an expanded argument may carry a secret.
	slog.Info("starting",
		slog.String("tunnel", *tunnelID),
		slog.String("endpoint", *endpoint),
		slog.String("command", strings.Join(command, " ")))

	argv := command
	if *expandEnv {
		argv = expandArgs(command, environLookup(env))
	}
	return &startup{tunnelID: *tunnelID, endpoint: *endpoint, token: token, argv: argv, env: env, storage: *storage}, nil
}

func (s *startup) dialer(kind string) *tunnel.Dialer {
	return &tunnel.Dialer{Endpoint: s.endpoint, TunnelID: s.tunnelID, Token: s.token, Kind: kind}
}

// takeToken removes the token from loaded variables and returns it.
func takeToken(vars []string) (string, []string) {
	var token string
	kept := make([]string, 0, len(vars))
	for _, v := range vars {
		if value, ok := strings.CutPrefix(v, config.EnvToken+"="); ok {
			token = strings.TrimSpace(value)
			continue
		}
		kept = append(kept, v)
	}
	return token, kept
}
