package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// logFlags carries --log-level and --log-format. Every subcommand binds
// them, so a warning from any package has somewhere to go.
type logFlags struct {
	level  *string
	format *string
}

func bindLogFlags(fs *flag.FlagSet) *logFlags {
	return &logFlags{
		level:  fs.String("log-level", "info", "debug | info | warn | error"),
		format: fs.String("log-format", "text", "text | json"),
	}
}

// install builds the logger and makes it the slog default, so packages
// under internal/ can log without a logger threaded through them.
func (l *logFlags) install() error {
	var lvl slog.Level
	switch strings.ToLower(*l.level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return fmt.Errorf("%w: unknown --log-level %q", errUsage, *l.level)
	}

	opts := &slog.HandlerOptions{Level: lvl}

	// Logs go to stderr, so stdout carries only command output.
	var h slog.Handler
	switch strings.ToLower(*l.format) {
	case "text":
		h = slog.NewTextHandler(os.Stderr, opts)
	case "json":
		h = slog.NewJSONHandler(os.Stderr, opts)
	default:
		return fmt.Errorf("%w: unknown --log-format %q", errUsage, *l.format)
	}

	slog.SetDefault(slog.New(h))
	return nil
}
