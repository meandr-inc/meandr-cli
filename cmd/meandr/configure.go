package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/meandr-inc/meandr-cli/internal/config"
	"golang.org/x/term"
)

// runConfigure stores a tunnel's token so "tunnel" can find it later.
// Nothing is sent anywhere: the token is checked the first time a tunnel
// dials.
//
// Both values are prompted for when they are not supplied, so the usual
// path is a bare "meandr configure".
func runConfigure(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	tunnelID := fs.String("id", "", "tunnel ID, as shown in the dashboard")
	logs := bindLogFlags(fs)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if err := logs.install(); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: unexpected argument %q", errUsage, fs.Arg(0))
	}

	interactive := term.IsTerminal(int(os.Stdin.Fd()))

	id := strings.TrimSpace(*tunnelID)
	if id == "" {
		if !interactive {
			return fmt.Errorf("%w: not a terminal, so pass --id <tunnel-id>", errUsage)
		}
		var err error
		if id, err = prompt(os.Stdin, "Tunnel ID: "); err != nil {
			return err
		}
		if id == "" {
			return fmt.Errorf("%w: empty tunnel ID", errUsage)
		}
	}

	token, err := readToken(interactive)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("%w: empty token", errUsage)
	}

	path, err := config.Store(id, token)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stderr, "Stored the token for tunnel %s in %s\n", id, path)
	return nil
}

// maxPromptLine bounds an answer typed at a prompt.
const maxPromptLine = 4096

// prompt asks for one line on stderr, so stdout stays clean for a caller
// to capture.
//
// Read a byte at a time rather than through a bufio.Reader: the token is
// read straight from the file descriptor next, and a buffered read here
// would swallow it whenever both lines arrive together.
func prompt(r io.Reader, label string) (string, error) {
	_, _ = fmt.Fprint(os.Stderr, label)

	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			if line = append(line, buf[0]); len(line) > maxPromptLine {
				return "", fmt.Errorf("%s is too long", strings.TrimSuffix(label, ": "))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				break
			}
			return "", fmt.Errorf("read %s: %w", strings.TrimSuffix(label, ": "), err)
		}
	}
	return strings.TrimSpace(string(line)), nil
}

// readToken reads the token without echoing it, or from stdin when piped:
//
//	echo "$TOKEN" | meandr configure --id <tunnel-id>
func readToken(interactive bool) (string, error) {
	if !interactive {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) && line == "" {
			return "", fmt.Errorf("read token from stdin: %w", err)
		}
		return strings.TrimSpace(line), nil
	}

	_, _ = fmt.Fprint(os.Stderr, "Token: ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}
