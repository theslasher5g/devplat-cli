#!/usr/bin/env bash
# Cross-compiles and packages devplat-cli for release: what get.devplat.ch
# serves and what the Download page's checksum table lists. Linux + Windows,
# both amd64, for now — macOS/arm64 are a later addition (same layout, just
# more `build` calls below), not a rewrite.
#
# Usage: ./scripts/build-release.sh <version>   # e.g. v0.4.2
# Output: dist/<version>/{*.tar.gz,*.zip,checksums.txt,checksums.txt.sig}
#
# The signature is the point of the manifest. A checksums.txt sitting on the
# same host as the archives it describes proves only that the download was not
# truncated: whoever can replace the binary replaces the checksum beside it.
# Signing it with a key that never touches the release host is what makes the
# manifest worth reading.
#
# Set DEVPLAT_RELEASE_KEY to the private key from scripts/gen-release-key.sh.
# Without it the script refuses to package a release, unless
# DEVPLAT_ALLOW_UNSIGNED=1 says otherwise — which is only reasonable before the
# key exists at all.
set -euo pipefail

VERSION="${1:?usage: build-release.sh <version> (e.g. v0.4.2)}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${REPO_ROOT}/dist/${VERSION}"

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

build() {
  local goos="$1" goarch="$2" ext="$3" archive_ext="$4"
  local out_dir bin_name archive_name
  out_dir="$(mktemp -d)"
  bin_name="devplat${ext}"
  archive_name="devplat-${VERSION}-${goos}-${goarch}"

  echo "==> building ${goos}/${goarch}"
  ( cd "$REPO_ROOT" && GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build \
      -ldflags "-X main.version=${VERSION} -s -w" \
      -o "${out_dir}/${bin_name}" ./cmd/devplat )

  if [ "$archive_ext" = "zip" ]; then
    ( cd "$out_dir" && zip -q "${archive_name}.zip" "$bin_name" )
    mv "${out_dir}/${archive_name}.zip" "$DIST_DIR/"
  else
    tar -czf "${DIST_DIR}/${archive_name}.tar.gz" -C "$out_dir" "$bin_name"
  fi
  rm -rf "$out_dir"
}

build linux   amd64 ""     tar.gz
build windows amd64 ".exe" zip

echo "==> writing checksums"
( cd "$DIST_DIR" && sha256sum -- *.tar.gz *.zip > checksums.txt )

RELEASE_KEY="${DEVPLAT_RELEASE_KEY:-}"
if [ -n "$RELEASE_KEY" ]; then
  if [ ! -r "$RELEASE_KEY" ]; then
    echo "DEVPLAT_RELEASE_KEY is set to '$RELEASE_KEY' but that file cannot be read" >&2
    exit 1
  fi
  echo "==> signing checksums.txt"
  # -rawin selects pure Ed25519 over the file itself, which is what both
  # `openssl pkeyutl -verify -rawin` in install.sh and crypto/ed25519 in
  # internal/release check. Any other mode and the two paths would disagree.
  openssl pkeyutl -sign -inkey "$RELEASE_KEY" -rawin \
    -in "${DIST_DIR}/checksums.txt" -out "${DIST_DIR}/checksums.txt.sig"

  # Verify what we just produced, with the public half derived from the same
  # key. Publishing a signature nobody can check is worse than publishing none:
  # every client would fail closed and the install would break for everyone.
  openssl pkey -in "$RELEASE_KEY" -pubout -out "${DIST_DIR}/.verify-pub.pem"
  if ! openssl pkeyutl -verify -pubin -inkey "${DIST_DIR}/.verify-pub.pem" -rawin \
      -in "${DIST_DIR}/checksums.txt" -sigfile "${DIST_DIR}/checksums.txt.sig" >/dev/null; then
    echo "the signature we just wrote does not verify — refusing to publish" >&2
    exit 1
  fi
  # Publish the public half too, at a stable path, so anyone can check a
  # download by hand — that is what the Download page's instructions use.
  # Derived from the private key rather than copied from the repo, so the
  # published key and the one that signed this release cannot disagree.
  mv "${DIST_DIR}/.verify-pub.pem" "${DIST_DIR}/../devplat-release.pub.pem"
  echo "    signature verified against its own key"
elif [ "${DEVPLAT_ALLOW_UNSIGNED:-}" = "1" ]; then
  echo "==> WARNING: packaging an UNSIGNED release (DEVPLAT_ALLOW_UNSIGNED=1)"
  echo "    Clients built with a real signing key will refuse to install this."
else
  echo "" >&2
  echo "refusing to package an unsigned release." >&2
  echo "  Set DEVPLAT_RELEASE_KEY=/path/to/devplat-release-key.pem" >&2
  echo "  (generate one with ./scripts/gen-release-key.sh)" >&2
  echo "  or set DEVPLAT_ALLOW_UNSIGNED=1 if no key exists yet." >&2
  exit 1
fi

echo "${VERSION}" > "${DIST_DIR}/../version.txt"

# The install scripts ship WITH the release, not separately.
#
# What users actually run is the copy sitting on get.devplat.ch, and the key it
# carries has to be the key that signed the release it fetches. Leaving that to
# a remembered second copy step is a rotation waiting to go half-done: the repo
# gets the new key, the published installer keeps the old one, and every user
# is told a genuine release failed verification. Putting them in dist/ makes the
# publish step "copy dist/" and removes the chance to forget.
cp "${REPO_ROOT}/install.sh" "${REPO_ROOT}/install.ps1" "${DIST_DIR}/../"

# Sanity: the key we are about to publish must be the one clients hold. A
# mismatch here means a rotation went half-done and every verifying client is
# about to reject a genuine release.
if [ -f "${DIST_DIR}/../devplat-release.pub.pem" ]; then
  published=$(grep -v -- '-----' "${DIST_DIR}/../devplat-release.pub.pem" | tr -d '\n')
  embedded=$(grep -A2 'BEGIN PUBLIC KEY' "${REPO_ROOT}/install.sh" | grep -v -- '-----' | tr -d '\n" ')
  if [ "$published" != "$embedded" ]; then
    echo "" >&2
    echo "the key this release was signed with does not match the one in install.sh." >&2
    echo "  Every client holding the embedded key would reject this release." >&2
    echo "  Finish the rotation (release.go, install.sh, install.ps1) first." >&2
    exit 1
  fi
fi

echo "==> done: ${DIST_DIR}"
ls -la "$DIST_DIR"
echo ""
if [ -f "${DIST_DIR}/checksums.txt.sig" ]; then
  echo ""
  echo "checksums.txt.sig must be published alongside checksums.txt — clients"
  echo "with a signing key configured treat a missing signature as tampering."
fi
echo "Publish by copying ${DIST_DIR} and dist/version.txt onto the"
echo "get.devplat.ch host — see devplat-cli/README.md's Distribution section."
