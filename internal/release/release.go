// Package release verifies that a downloaded devplat build is the one we
// published.
//
// The problem it solves: until now the only integrity check was a
// checksums.txt served from the same host as the archives it describes. That
// protects against a truncated download and against nothing else — whoever can
// replace the binary on get.devplat.ch can replace the checksum next to it in
// the same breath, and both the install script and `devplat upgrade` would
// have reported success. For a tool that installs itself into /usr/local/bin
// and then holds an API token, that is the wrong trust model.
//
// The fix is a detached Ed25519 signature over checksums.txt, made with a key
// that never sits on the release host. Verifying it needs the public key, which
// is compiled into this binary and pasted into the install scripts — so an
// attacker who owns get.devplat.ch entirely still cannot produce a manifest any
// devplat client will accept.
//
// Ed25519 specifically, in PKIX/PEM form, because that is the one shape both
// Go's crypto/ed25519 and stock `openssl pkeyutl -verify -rawin` read without
// conversion. The install script has no Go available and the CLI has no openssl
// guaranteed; a format both accept is what lets the two paths verify the same
// bytes against the same key.
package release

import (
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strings"
)

// PublicKeyPEM is the release signing key.
//
// PLACEHOLDER — no signing key has been generated yet. While it holds this
// value, IsConfigured() reports false and the verifying paths say plainly that
// releases are unsigned rather than pretending to have checked something.
// Replace it (and the copies in install.sh / install.ps1, which
// pubkey_consistency_test.go pins together) with the output of
// scripts/gen-release-key.sh.
//
// The private half must never exist on the release host or in this repository.
// Its whole value is being somewhere an attacker who owns get.devplat.ch is
// not.
const PublicKeyPEM = `-----BEGIN PUBLIC KEY-----
PLACEHOLDER-NO-RELEASE-KEY-CONFIGURED
-----END PUBLIC KEY-----`

const placeholderMarker = "PLACEHOLDER-NO-RELEASE-KEY-CONFIGURED"

// activeKey is what the verifying functions actually read. It is seeded from
// the constant above and is only ever reassigned by export_test.go, which is
// how the verification paths get exercised end to end without the real signing
// key needing to exist anywhere near this repository. Nothing outside this
// package can reach it.
var activeKey = PublicKeyPEM

// ErrUnsigned is returned when a release carries no signature but the client
// holds a real key — i.e. we know signatures exist and this download has none.
var ErrUnsigned = errors.New("this release is not signed")

// IsConfigured reports whether a real signing key is compiled in.
//
// This is the switch that makes the rollout safe in both directions. Before a
// key exists, verification is honestly reported as unavailable and nothing
// breaks for people already installing. Once the key is in, a *missing*
// signature becomes a hard failure — which is what closes the obvious
// downgrade, where an attacker who controls the host simply deletes the .sig
// and hopes the client shrugs.
func IsConfigured() bool {
	return !strings.Contains(activeKey, placeholderMarker)
}

// PublicKey parses the compiled-in key.
func PublicKey() (ed25519.PublicKey, error) {
	if !IsConfigured() {
		return nil, errors.New("no release signing key is configured in this build")
	}
	block, _ := pem.Decode([]byte(activeKey))
	if block == nil {
		return nil, errors.New("release public key is not valid PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("release public key could not be parsed: %w", err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("release public key is %T, expected ed25519", parsed)
	}
	return key, nil
}

// VerifyManifest checks a detached signature over the checksums manifest.
//
// signature is the raw 64-byte Ed25519 signature exactly as
// `openssl pkeyutl -sign -rawin` writes it, so the file the release script
// produces is the file both this and the install script consume — no encoding
// step for either side to get wrong.
func VerifyManifest(manifest, signature []byte) error {
	key, err := PublicKey()
	if err != nil {
		return err
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("signature is %d bytes, expected %d — the file is truncated or not a signature",
			len(signature), ed25519.SignatureSize)
	}
	// crypto.Hash(0) selects pure Ed25519 (sign the message itself), which is
	// what -rawin does. Anything else would verify a different thing than the
	// shell path and the two would disagree only on some releases.
	if err := ed25519.VerifyWithOptions(key, manifest, signature, &ed25519.Options{Hash: crypto.Hash(0)}); err != nil {
		return fmt.Errorf("release signature does not match: %w", err)
	}
	return nil
}

// ChecksumFor pulls one file's expected SHA-256 out of a sha256sum-format
// manifest.
//
// Exact filename match, never a substring: "devplat-v1.0.0-linux-amd64.tar.gz"
// must not be satisfied by a line for some other archive whose name happens to
// contain it.
func ChecksumFor(manifest []byte, filename string) (string, error) {
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		// sha256sum writes "<hash>  <name>" and prefixes the name with '*' in
		// binary mode; accept both rather than depending on which produced it.
		name := strings.TrimPrefix(fields[1], "*")
		if name == filename {
			hash := strings.ToLower(fields[0])
			if len(hash) != sha256.Size*2 {
				return "", fmt.Errorf("checksum for %s is %d characters, expected %d", filename, len(hash), sha256.Size*2)
			}
			if _, err := hex.DecodeString(hash); err != nil {
				return "", fmt.Errorf("checksum for %s is not hexadecimal", filename)
			}
			return hash, nil
		}
	}
	return "", fmt.Errorf("the signed manifest lists no entry for %s", filename)
}

// VerifyStream hashes r and compares it to expectedHex.
//
// Streaming rather than taking a []byte so a download can be verified without
// holding the whole archive in memory, and so the caller cannot accidentally
// check one copy of the bytes while writing another to disk.
func VerifyStream(r io.Reader, expectedHex string) error {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return fmt.Errorf("could not read the download: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != strings.ToLower(expectedHex) {
		return fmt.Errorf("checksum mismatch: the download hashes to %s, the signed manifest says %s", got, expectedHex)
	}
	return nil
}
