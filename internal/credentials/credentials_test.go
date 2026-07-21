package credentials

import (
	"os"
	"runtime"
	"testing"
)

// isolateConfigDir points os.UserConfigDir at a temp dir for the test by
// setting the platform's config-dir env var, so these tests never touch the
// developer's real credentials file.
func isolateConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	switch runtime.GOOS {
	case "darwin":
		t.Setenv("HOME", dir) // ~/Library/Application Support
	case "windows":
		t.Setenv("AppData", dir)
	default:
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
}

func TestSaveLoadDeleteRoundTrip(t *testing.T) {
	isolateConfigDir(t)

	if c, err := Load(); err != nil || c != nil {
		t.Fatalf("Load with no file: want (nil,nil), got (%v,%v)", c, err)
	}

	want := Credentials{Token: "dvp_dev_abc123", APIURL: "https://api.example"}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil || got == nil {
		t.Fatalf("Load after save: %v / %v", got, err)
	}
	if got.Token != want.Token || got.APIURL != want.APIURL {
		t.Fatalf("round-trip mismatch: got %+v want %+v", *got, want)
	}

	// The file holds a bearer token — it must not be world/group readable.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(Path())
		if err != nil {
			t.Fatalf("stat credentials file: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("credentials file perms = %o, want 600", perm)
		}
	}

	if err := Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if c, err := Load(); err != nil || c != nil {
		t.Fatalf("Load after delete: want (nil,nil), got (%v,%v)", c, err)
	}
	// Delete again is a no-op success.
	if err := Delete(); err != nil {
		t.Fatalf("second Delete should be a no-op, got %v", err)
	}
}
