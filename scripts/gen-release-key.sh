#!/usr/bin/env bash
# Generates the devplat release signing key.
#
# Run this ONCE, on a machine you control, and never on the release host or in
# CI. The entire value of the signature is that the private half lives somewhere
# an attacker who owns get.devplat.ch does not — copying it onto that host, or
# into this repository, converts the whole scheme back into the checksum file it
# replaced.
#
#   ./scripts/gen-release-key.sh
#
# Writes:
#   devplat-release-key.pem      the private key — NEVER commit, never upload
#   devplat-release-key.pub.pem  the public key — paste into the three places below
#
# Afterwards, paste the public key verbatim into:
#   internal/release/release.go   (PublicKeyPEM)
#   install.sh                    (RELEASE_PUBKEY)
#   install.ps1                   ($ReleasePubKey)
# pubkey_consistency_test.go fails if those three ever drift apart.
#
# Storage: a password manager entry or an offline encrypted volume is enough for
# a one-person operation. If it ever moves into CI, use a secret store and
# accept that CI compromise now means release compromise — which is exactly the
# property this key exists to avoid, so prefer signing on your own machine.
set -euo pipefail

PRIV="devplat-release-key.pem"
PUB="devplat-release-key.pub.pem"

if [ -e "$PRIV" ]; then
  echo "refusing to overwrite an existing $PRIV — move it aside first if you really mean to rotate" >&2
  exit 1
fi

# Ed25519: small keys, no parameter choices to get wrong, and verifiable both by
# Go's crypto/ed25519 and by stock openssl, which is what lets the CLI and the
# install script check the same signature.
openssl genpkey -algorithm ed25519 -out "$PRIV"
chmod 600 "$PRIV"
openssl pkey -in "$PRIV" -pubout -out "$PUB"

echo
echo "Private key: $PRIV  (chmod 600 — do not commit, do not upload)"
echo "Public key:  $PUB"
echo
echo "Paste this into internal/release/release.go, install.sh and install.ps1:"
echo
cat "$PUB"
echo
echo "Then rotate by re-running with the old key moved aside. Rotation is not"
echo "free: clients hold the old key until they upgrade, so publish one release"
echo "signed with the old key that ships the new one before switching over."
