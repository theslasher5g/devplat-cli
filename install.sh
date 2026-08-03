#!/bin/sh
# Installs the devplat CLI: detects arch, downloads the current release from
# get.devplat.ch, verifies its SIGNATURE and checksum, and puts the binary on
# PATH.
#
# Why a signature and not just the checksum: checksums.txt is served by the same
# host as the archive it describes, so on its own it proves the download was not
# truncated and nothing more — whoever can replace the binary replaces the
# checksum in the same breath. The signature is made with a key that never
# touches that host, and the public half is pasted into this script, which you
# fetched over TLS from a different origin. That is the part an attacker who
# owns the release host cannot forge.
#
#   curl -fsSL https://get.devplat.ch | sh
#
# Prefer to read it first? Same script, two steps:
#   curl -fsSLO https://get.devplat.ch/install.sh
#   less install.sh && sh install.sh
#
# Deliberately shaped like get.docker.com/rustup.rs's install scripts — this
# exact "curl | sh" pattern is one people already know how to audit first.
set -eu

BASE_URL="${DEVPLAT_INSTALL_BASE:-https://get.devplat.ch}"
INSTALL_DIR="${DEVPLAT_INSTALL_DIR:-/usr/local/bin}"

# Release signing key. Keep byte-identical with internal/release/release.go and
# install.ps1 — pubkey_consistency_test.go fails the build if they drift.
# While this is the placeholder, no key has been generated yet: the script says
# so out loud and installs on the checksum alone, rather than printing a
# verification it did not perform.
RELEASE_PUBKEY="-----BEGIN PUBLIC KEY-----
PLACEHOLDER-NO-RELEASE-KEY-CONFIGURED
-----END PUBLIC KEY-----"

os="$(uname -s)"
arch="$(uname -m)"

case "$os" in
  Linux) goos="linux" ;;
  *)
    echo "devplat: this script installs the Linux build only (detected: $os)." >&2
    echo "See https://devplat.ch/download for other platforms." >&2
    exit 1
    ;;
esac

case "$arch" in
  x86_64|amd64) goarch="amd64" ;;
  *)
    echo "devplat: unsupported architecture '$arch' — only amd64 is published right now." >&2
    exit 1
    ;;
esac

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

echo "==> resolving latest version"
version="$(curl -fsSL "${BASE_URL}/version.txt")"

archive="devplat-${version}-${goos}-${goarch}.tar.gz"
archive_url="${BASE_URL}/${version}/${archive}"
checksums_url="${BASE_URL}/${version}/checksums.txt"
signature_url="${checksums_url}.sig"

echo "==> downloading ${archive} (${version})"
curl -fsSL "$archive_url" -o "${tmp_dir}/${archive}"
curl -fsSL "$checksums_url" -o "${tmp_dir}/checksums.txt"

# --- signature -------------------------------------------------------------
# The order matters: the signature is checked over checksums.txt BEFORE the
# checksum is used for anything. Verifying the archive against an unverified
# manifest would be checking a claim against itself.
if printf '%s' "$RELEASE_PUBKEY" | grep -q PLACEHOLDER-NO-RELEASE-KEY-CONFIGURED; then
  echo "==> NOTE: this installer has no release signing key configured yet."
  echo "    Falling back to the checksum alone, which cannot detect a"
  echo "    compromised release host. See devplat-cli/scripts/gen-release-key.sh."
else
  if ! command -v openssl >/dev/null 2>&1; then
    echo "devplat: openssl is required to verify the release signature but was not found." >&2
    echo "         Install openssl, or set DEVPLAT_ALLOW_UNVERIFIED=1 to install without" >&2
    echo "         verifying — which means trusting whatever ${BASE_URL} serves you." >&2
    [ "${DEVPLAT_ALLOW_UNVERIFIED:-}" = "1" ] || exit 1
    echo "==> WARNING: installing WITHOUT signature verification (DEVPLAT_ALLOW_UNVERIFIED=1)"
  else
    echo "==> verifying release signature"
    # A missing signature is a failure, not a reason to skip the check. If it
    # were skippable, an attacker holding the host would simply delete the file.
    if ! curl -fsSL "$signature_url" -o "${tmp_dir}/checksums.txt.sig"; then
      echo "devplat: ${version} has no signature at ${signature_url}." >&2
      echo "         This installer expects every release to be signed, so it is" >&2
      echo "         refusing rather than downgrading to an unverified install." >&2
      exit 1
    fi
    printf '%s\n' "$RELEASE_PUBKEY" > "${tmp_dir}/release-pub.pem"
    if ! openssl pkeyutl -verify -pubin -inkey "${tmp_dir}/release-pub.pem" -rawin \
        -in "${tmp_dir}/checksums.txt" -sigfile "${tmp_dir}/checksums.txt.sig" >/dev/null 2>&1; then
      echo "devplat: SIGNATURE VERIFICATION FAILED for ${version}." >&2
      echo "         The checksums file was not signed by the devplat release key." >&2
      echo "         Do not install this. Please report it to security@devplat.ch." >&2
      exit 1
    fi
    echo "    signature ok — checksums.txt is authentic"
  fi
fi

echo "==> verifying checksum"
( cd "$tmp_dir" && grep " ${archive}\$" checksums.txt | sha256sum -c - )

echo "==> extracting"
tar -xzf "${tmp_dir}/${archive}" -C "$tmp_dir"

mkdir -p "$INSTALL_DIR" 2>/dev/null || true
if [ -w "$INSTALL_DIR" ]; then
  mv "${tmp_dir}/devplat" "${INSTALL_DIR}/devplat"
  chmod +x "${INSTALL_DIR}/devplat"
else
  echo "==> ${INSTALL_DIR} isn't writable, using sudo"
  sudo mv "${tmp_dir}/devplat" "${INSTALL_DIR}/devplat"
  sudo chmod +x "${INSTALL_DIR}/devplat"
fi

echo ""
echo "devplat ${version} installed to ${INSTALL_DIR}/devplat"
echo ""
echo "Next:"
echo "  devplat connect --token \$DEVPLAT_TOKEN"
