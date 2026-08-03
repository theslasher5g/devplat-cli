package config

import (
	"strings"
	"testing"
)

// The API token is a bearer credential: whoever reads it off the wire can start
// environments and read the team's history. Nothing checked the URL scheme, so
// DEVPLAT_API_URL=http://api.devplat.ch — a typo, a stale CI snippet, a
// downgrade injected upstream — sent it in the clear and said nothing.

func TestPlaintextToARemoteHostIsRefused(t *testing.T) {
	err := Config{APIURL: "http://api.devplat.ch"}.Validate()
	if err == nil {
		t.Fatal("http:// to a remote host must be refused")
	}
	// The message has to say what the risk is, not just that something is
	// wrong: the person reading it is mid-CI-debug and will otherwise "fix" it
	// by looking for another workaround.
	if !strings.Contains(err.Error(), "in the clear") {
		t.Fatalf("the error must explain the exposure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "https://") {
		t.Fatalf("the error must name the fix, got: %v", err)
	}
}

func TestHttpsIsAccepted(t *testing.T) {
	for _, u := range []string{
		"https://api.devplat.ch",
		"https://api.devplat.ch/",
		"https://staging.api.devplat.ch:8443",
	} {
		if err := (Config{APIURL: u}).Validate(); err != nil {
			t.Fatalf("%s should be accepted: %v", u, err)
		}
	}
}

func TestLoopbackOverPlaintextStaysAllowed(t *testing.T) {
	// --api-url is documented for local development, and a connection that
	// never leaves the machine has no network to be observed on. Breaking this
	// would push developers towards disabling the check entirely.
	for _, u := range []string{
		"http://localhost:3000",
		"http://LOCALHOST:3000",
		"http://127.0.0.1:3000",
		"http://[::1]:3000",
	} {
		if err := (Config{APIURL: u}).Validate(); err != nil {
			t.Fatalf("%s should be allowed for local development: %v", u, err)
		}
	}
}

func TestLoopbackLookalikesAreNotTreatedAsLocal(t *testing.T) {
	// A host that merely contains "localhost" is somebody else's machine.
	for _, u := range []string{
		"http://localhost.evil.example",
		"http://notlocalhost",
		"http://127.0.0.1.evil.example",
	} {
		if err := (Config{APIURL: u}).Validate(); err == nil {
			t.Fatalf("%s is a remote host and must be refused", u)
		}
	}
}

func TestOtherSchemesAreRefused(t *testing.T) {
	for _, u := range []string{"ftp://api.devplat.ch", "file:///etc/passwd", "javascript:alert(1)"} {
		if err := (Config{APIURL: u}).Validate(); err == nil {
			t.Fatalf("%s must be refused", u)
		}
	}
}

func TestGarbageIsRefusedWithAUsefulMessage(t *testing.T) {
	err := Config{APIURL: "api.devplat.ch"}.Validate()
	if err == nil {
		t.Fatal("a URL with no scheme must be refused rather than silently prefixed")
	}
	if !strings.Contains(err.Error(), "https://api.devplat.ch") {
		t.Fatalf("the error should show what a good value looks like, got: %v", err)
	}
}

func TestTheDefaultPasses(t *testing.T) {
	// Whatever else changes, the value every user gets without configuring
	// anything must not be one the CLI refuses to start with.
	if err := (Config{APIURL: DefaultAPIURL}).Validate(); err != nil {
		t.Fatalf("the built-in default must be valid: %v", err)
	}
}
