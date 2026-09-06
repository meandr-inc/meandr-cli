// Package config resolves the token a tunnel authenticates with.
//
// Two sources, in order: the MEANDR_AUTH_TOKEN environment variable, then
// the credentials file written by "meandr configure". The environment
// wins, so a container needs nothing on disk.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// EnvToken is the environment variable holding a token.
const EnvToken = "MEANDR_AUTH_TOKEN"

// ErrNoCredential means neither source had a token for this server.
var ErrNoCredential = errors.New("no credential")

// credentials is the on-disk shape: tunnel id to token. One host may run
// several tunnels.
type credentials struct {
	Tunnels map[string]string `json:"tunnels"`
}

// Path is the credentials file location, overridable with
// MEANDR_CONFIG_DIR.
func Path() (string, error) {
	if dir := os.Getenv("MEANDR_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "credentials"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".meandr", "credentials"), nil
}

// Token returns the token for tunnelID. A token in the environment is
// used as given, without checking which tunnel it belongs to.
func Token(tunnelID string) (string, error) {
	if t := strings.TrimSpace(os.Getenv(EnvToken)); t != "" {
		return t, nil
	}

	path, err := Path()
	if err != nil {
		return "", err
	}
	creds, err := load(path)
	if err != nil {
		return "", err
	}
	if t := strings.TrimSpace(creds.Tunnels[tunnelID]); t != "" {
		return t, nil
	}
	return "", fmt.Errorf("%w for tunnel %s: set %s, or run 'meandr configure --id %s'",
		ErrNoCredential, tunnelID, EnvToken, tunnelID)
}

// Store writes a token for tunnelID, keeping any others. The file is
// 0600 and the directory 0700.
func Store(tunnelID, token string) (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}

	creds, err := load(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if creds.Tunnels == nil {
		creds.Tunnels = map[string]string{}
	}
	creds.Tunnels[tunnelID] = token

	body, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode credentials: %w", err)
	}
	body = append(body, '\n')

	// Write to a temporary file and rename, so a crash cannot leave a
	// truncated file behind.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// After a successful rename there is nothing to remove. Any other
		// failure leaves a file containing a token in place, which the
		// operator needs to know about.
		if err := os.Remove(tmpName); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("could not remove temporary credentials file; it contains a token",
				slog.String("path", tmpName),
				slog.String("error", err.Error()))
		}
	}()

	// Close on these two paths is cleanup after a failure. The Close
	// below is the operation itself, so its error is returned.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("install credentials file: %w", err)
	}
	return path, nil
}

// load reads the credentials file. A missing file is not an error here.
func load(path string) (credentials, error) {
	var creds credentials

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return creds, nil
		}
		return creds, fmt.Errorf("stat %s: %w", path, err)
	}

	// Refuse a file others can read, rather than use it and let the
	// operator believe the token is protected.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return creds, fmt.Errorf("%s has permissions %04o; must be 0600 (chmod 600 %s)", path, perm, path)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return creds, fmt.Errorf("read %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return creds, nil
	}
	if err := json.Unmarshal(body, &creds); err != nil {
		return creds, fmt.Errorf("parse %s: %w", path, err)
	}
	return creds, nil
}
