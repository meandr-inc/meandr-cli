package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreThenToken(t *testing.T) {
	t.Setenv("MEANDR_CONFIG_DIR", t.TempDir())

	if _, err := Store("tun-1", "tok-1"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := Token("tun-1")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "tok-1" {
		t.Fatalf("token = %q, want tok-1", got)
	}
}

// One host may run several tunnels, so storing a second must not evict
// the first.
func TestStoreKeepsOtherTunnels(t *testing.T) {
	t.Setenv("MEANDR_CONFIG_DIR", t.TempDir())

	if _, err := Store("tun-1", "tok-1"); err != nil {
		t.Fatalf("Store tun-1: %v", err)
	}
	if _, err := Store("tun-2", "tok-2"); err != nil {
		t.Fatalf("Store tun-2: %v", err)
	}
	for id, want := range map[string]string{"tun-1": "tok-1", "tun-2": "tok-2"} {
		got, err := Token(id)
		if err != nil {
			t.Fatalf("Token %s: %v", id, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", id, got, want)
		}
	}
}

// The environment wins over the file, and is used as given rather than
// checked against the tunnel id.
func TestEnvBeatsFile(t *testing.T) {
	t.Setenv("MEANDR_CONFIG_DIR", t.TempDir())
	if _, err := Store("tun-1", "from-file"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	t.Setenv(EnvToken, "from-env")

	got, err := Token("tun-1")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got != "from-env" {
		t.Fatalf("token = %q, want from-env", got)
	}
}

func TestMissingCredentialIsSentinel(t *testing.T) {
	t.Setenv("MEANDR_CONFIG_DIR", t.TempDir())
	t.Setenv(EnvToken, "")

	_, err := Token("tun-unknown")
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("want ErrNoCredential, got %v", err)
	}
	// The message must name both ways to supply a token.
	if !strings.Contains(err.Error(), EnvToken) || !strings.Contains(err.Error(), "meandr configure") {
		t.Errorf("error should name both remedies, got: %v", err)
	}
}

// A token others can read is not protected, so it is refused.
func TestGroupReadableFileRefused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEANDR_CONFIG_DIR", dir)
	t.Setenv(EnvToken, "")

	if _, err := Store("tun-1", "tok-1"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	path := filepath.Join(dir, "credentials")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	_, err := Token("tun-1")
	if err == nil {
		t.Fatal("group-readable credentials file was accepted")
	}
	if !strings.Contains(err.Error(), "0600") {
		t.Errorf("error should say what the mode must be, got: %v", err)
	}
}

func TestStoreWritesOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEANDR_CONFIG_DIR", dir)

	path, err := Store("tun-1", "tok-1")
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials mode = %04o, want 0600", perm)
	}
}

// Store renames a complete temporary file into place, so a partial write
// can never be read back as valid.
func TestStoreLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEANDR_CONFIG_DIR", dir)

	if _, err := Store("tun-1", "tok-1"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".credentials-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
