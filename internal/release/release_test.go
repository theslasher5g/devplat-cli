package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These cover the one question the whole package exists to answer: would a
// compromised release host be able to get this client to install its binary?

// testKey builds a throwaway keypair and swaps it in for the compiled-in one,
// so verification can be exercised end to end without the real signing key
// existing anywhere near this repository.
func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	original := PublicKeyPEM
	setKey(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	t.Cleanup(func() { setKey(original) })
	return priv
}

func sign(t *testing.T, priv ed25519.PrivateKey, msg []byte) []byte {
	t.Helper()
	sig, err := priv.Sign(rand.Reader, msg, crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestPlaceholderKeyReportsItselfAsUnconfigured(t *testing.T) {
	// The rollout depends on this being honest. If the placeholder ever read as
	// "configured", every install would fail closed against a signature that
	// cannot exist yet; if a real key read as "unconfigured", verification would
	// silently stop happening.
	if IsConfigured() {
		t.Fatal("the checked-in placeholder must report as unconfigured")
	}
	if _, err := PublicKey(); err == nil {
		t.Fatal("parsing the placeholder must fail rather than yield a usable key")
	}
}

func TestAGenuineSignatureVerifies(t *testing.T) {
	priv := testKey(t)
	if !IsConfigured() {
		t.Fatal("a real key must report as configured")
	}
	manifest := []byte("abc123  devplat-v1.0.0-linux-amd64.tar.gz\n")
	if err := VerifyManifest(manifest, sign(t, priv, manifest)); err != nil {
		t.Fatalf("a correctly signed manifest must verify: %v", err)
	}
}

func TestATamperedManifestIsRejected(t *testing.T) {
	// The attack this is all for: the host swaps the binary and rewrites the
	// checksum to match, but cannot re-sign.
	priv := testKey(t)
	original := []byte("aaa  devplat-v1.0.0-linux-amd64.tar.gz\n")
	sig := sign(t, priv, original)
	tampered := []byte("bbb  devplat-v1.0.0-linux-amd64.tar.gz\n")
	if err := VerifyManifest(tampered, sig); err == nil {
		t.Fatal("a rewritten manifest must not verify against the old signature")
	}
}

func TestASignatureFromTheWrongKeyIsRejected(t *testing.T) {
	testKey(t) // configures key A
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte("aaa  devplat.tar.gz\n")
	if err := VerifyManifest(manifest, sign(t, otherPriv, manifest)); err == nil {
		t.Fatal("a signature from a key we do not trust must be rejected")
	}
}

func TestATruncatedSignatureIsRejectedWithAClearReason(t *testing.T) {
	priv := testKey(t)
	manifest := []byte("aaa  devplat.tar.gz\n")
	sig := sign(t, priv, manifest)
	err := VerifyManifest(manifest, sig[:32])
	if err == nil {
		t.Fatal("a half signature must not verify")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("the error should say what is wrong with the file, got: %v", err)
	}
}

func TestVerifyingWithoutAKeyIsAnErrorNotASuccess(t *testing.T) {
	// Fail closed. A build with no key must never treat "cannot check" as "ok".
	manifest := []byte("aaa  devplat.tar.gz\n")
	if err := VerifyManifest(manifest, make([]byte, ed25519.SignatureSize)); err == nil {
		t.Fatal("verification with no configured key must fail")
	}
}

/* ---- manifest parsing ---- */

const manifest = `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  devplat-v1.0.0-linux-amd64.tar.gz
5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03  devplat-v1.0.0-windows-amd64.zip
`

func TestChecksumLookupIsExact(t *testing.T) {
	got, err := ChecksumFor([]byte(manifest), "devplat-v1.0.0-linux-amd64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("wrong checksum: %s", got)
	}
}

func TestASubstringDoesNotSatisfyALookup(t *testing.T) {
	// "linux-amd64.tar.gz" must not be answered by the line for the full name:
	// a loose match is how a client ends up validating the wrong artefact.
	if _, err := ChecksumFor([]byte(manifest), "linux-amd64.tar.gz"); err == nil {
		t.Fatal("a partial filename must not match")
	}
}

func TestAMissingEntryIsAnError(t *testing.T) {
	if _, err := ChecksumFor([]byte(manifest), "devplat-v1.0.0-darwin-arm64.tar.gz"); err == nil {
		t.Fatal("a platform absent from the manifest must be an error, not an empty hash")
	}
}

func TestAMalformedChecksumIsRejected(t *testing.T) {
	for _, bad := range []string{
		"nothex  devplat.tar.gz\n",
		"abc  devplat.tar.gz\n",
		"zzzz0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  devplat.tar.gz\n",
	} {
		if _, err := ChecksumFor([]byte(bad), "devplat.tar.gz"); err == nil {
			t.Fatalf("must reject %q", strings.TrimSpace(bad))
		}
	}
}

func TestBinaryModeAsteriskIsAccepted(t *testing.T) {
	// sha256sum writes "*name" in binary mode; a release built on a machine
	// where that is the default must not become uninstallable.
	m := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 *devplat.tar.gz\n"
	if _, err := ChecksumFor([]byte(m), "devplat.tar.gz"); err != nil {
		t.Fatalf("binary-mode entries must parse: %v", err)
	}
}

func TestStreamVerification(t *testing.T) {
	payload := []byte("some archive bytes")
	sum := sha256.Sum256(payload)
	if err := VerifyStream(bytes.NewReader(payload), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("matching content must verify: %v", err)
	}
	if err := VerifyStream(bytes.NewReader([]byte("different")), hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("different content must not verify")
	}
}

/* ---- archive extraction ---- */

func tarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractsTheBinary(t *testing.T) {
	dir := t.TempDir()
	out, err := ExtractBinary(tarGz(t, map[string]string{"devplat": "#!/bin/true"}), dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(out) != dir {
		t.Fatalf("extracted outside the target directory: %s", out)
	}
	if b, _ := os.ReadFile(out); string(b) != "#!/bin/true" {
		t.Fatalf("wrong contents: %q", b)
	}
}

func TestAPathTraversingEntryCannotEscape(t *testing.T) {
	// The archive is attacker-controlled in exactly the scenario that matters,
	// and this extractor can run under sudo. A "../../etc/cron.d/x" entry must
	// go nowhere near that path — we only ever write our own constant name into
	// our own directory.
	dir := t.TempDir()
	out, err := ExtractBinary(tarGz(t, map[string]string{"../../../../tmp/devplat": "pwned"}), dir)
	if err != nil {
		t.Fatalf("an entry whose basename is devplat should still extract safely: %v", err)
	}
	if filepath.Dir(out) != dir {
		t.Fatalf("extraction escaped the target directory: %s", out)
	}
}

func TestAnArchiveWithoutTheBinaryIsAnError(t *testing.T) {
	if _, err := ExtractBinary(tarGz(t, map[string]string{"README": "hi"}), t.TempDir()); err == nil {
		t.Fatal("an archive with no devplat binary must be reported, not silently ignored")
	}
}

func TestGarbageIsNotMistakenForAnArchive(t *testing.T) {
	if _, err := ExtractBinary([]byte("not gzip at all"), t.TempDir()); err == nil {
		t.Fatal("non-gzip input must be rejected")
	}
}

func TestArchiveNamePerPlatform(t *testing.T) {
	if got := ArchiveName("v1.2.3", "linux", "amd64"); got != "devplat-v1.2.3-linux-amd64.tar.gz" {
		t.Fatalf("linux: %s", got)
	}
	if got := ArchiveName("v1.2.3", "windows", "amd64"); got != "devplat-v1.2.3-windows-amd64.zip" {
		t.Fatalf("windows: %s", got)
	}
}
