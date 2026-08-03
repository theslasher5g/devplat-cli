package release

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The signing key necessarily exists three times: compiled into the CLI, and
// pasted into each install script, because neither shell nor PowerShell can
// read a Go constant. Three copies of a trust anchor is a drift problem waiting
// to happen, and the way it would show up is the worst kind — a key rotation
// where the Go binary and install.sh disagree, so half the users get "signature
// verification failed" on a release that is perfectly genuine, learn to reach
// for the skip flag, and the whole mechanism is dead.
//
// This is the guard. It reads the two scripts off disk and compares them to the
// constant, so the copies cannot part ways without the build going red.

func repoRoot(t *testing.T) string {
	t.Helper()
	// internal/release -> repo root
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// normalise strips whatever quoting and line endings each file format needs, so
// the comparison is about the key material and not about shell syntax.
func normalise(pemText string) string {
	pemText = strings.ReplaceAll(pemText, "\r\n", "\n")
	var out []string
	for _, line := range strings.Split(pemText, "\n") {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, `"`)
		line = strings.TrimSuffix(line, "@")
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

var pemBlock = regexp.MustCompile(`(?s)-----BEGIN PUBLIC KEY-----.*?-----END PUBLIC KEY-----`)

func keyFromFile(t *testing.T, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("cannot read %s: %v", rel, err)
	}
	found := pemBlock.FindString(string(body))
	if found == "" {
		t.Fatalf("%s contains no PUBLIC KEY block — the release key must be pasted into it", rel)
	}
	return normalise(found)
}

func TestInstallScriptsCarryTheSameKeyAsTheBinary(t *testing.T) {
	want := normalise(PublicKeyPEM)
	for _, script := range []string{"install.sh", "install.ps1"} {
		if got := keyFromFile(t, script); got != want {
			t.Errorf("%s has a different release key than internal/release/release.go.\n"+
				"Rotating the key means updating all three together — a client and an installer\n"+
				"that disagree reject genuine releases and teach people to skip verification.\n"+
				"  %s:\n%s\n  release.go:\n%s", script, script, got, want)
		}
	}
}

func TestTheScriptsAgreeOnWhetherAKeyExistsAtAll(t *testing.T) {
	// The placeholder is not just a value, it is the switch that decides
	// whether a missing signature is tolerated or is treated as tampering. If
	// one file still held the placeholder while another had a real key, the CLI
	// and the installer would disagree about whether signing is in force.
	binaryConfigured := IsConfigured()
	for _, script := range []string{"install.sh", "install.ps1"} {
		scriptConfigured := !strings.Contains(keyFromFile(t, script), placeholderMarker)
		if scriptConfigured != binaryConfigured {
			t.Errorf("%s says signing is configured=%v, the binary says %v — "+
				"one of them will accept releases the other refuses",
				script, scriptConfigured, binaryConfigured)
		}
	}
}
