#!/bin/sh
# Installs the devplat CLI: detects arch, downloads the current release from
# get.devplat.ch, verifies its checksum, and puts the binary on PATH.
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

echo "==> downloading ${archive} (${version})"
curl -fsSL "$archive_url" -o "${tmp_dir}/${archive}"
curl -fsSL "$checksums_url" -o "${tmp_dir}/checksums.txt"

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
