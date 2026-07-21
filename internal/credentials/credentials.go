// Package credentials persists the API token obtained via `devplat login` so
// `devplat connect` works afterwards without --token or DEVPLAT_TOKEN. Stored
// as a 0600 JSON file under the OS user-config dir (os.UserConfigDir handles
// XDG_CONFIG_HOME on Linux, ~/Library/Application Support on macOS, %AppData%
// on Windows), i.e. ~/.config/devplat/credentials.json on a typical Linux box.
//
// This is deliberately a plain file, not an OS keychain: it's the pragmatic
// first cut. The stored token is a scoped, revocable dev:run token (revoke it
// in the dashboard or via `devplat logout`), not a password — a keychain
// backend is a later hardening step, not a correctness requirement.
package credentials

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Credentials struct {
	Token  string `json:"token"`
	APIURL string `json:"api_url,omitempty"`
}

// path returns the credentials file location, creating the parent dir with
// 0700 so a fresh machine doesn't fail on first save.
func path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config dir: %w", err)
	}
	return filepath.Join(dir, "devplat", "credentials.json"), nil
}

// Load returns the stored credentials, or (nil, nil) if none are saved. A
// missing file is not an error — it just means the user hasn't logged in.
func Load() (*Credentials, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

// Save writes credentials with 0600 permissions, creating the parent dir.
func Save(c Credentials) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file in the same dir then rename, so a crash mid-write
	// can't leave a truncated credentials file that breaks every later run.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Delete removes the stored credentials. A missing file is a no-op success so
// `devplat logout` is idempotent.
func Delete() error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Path returns the credentials file location for display in CLI messages.
func Path() string {
	p, err := path()
	if err != nil {
		return "(unknown)"
	}
	return p
}
