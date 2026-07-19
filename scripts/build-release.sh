#!/usr/bin/env bash
# Cross-compiles and packages devplat-cli for release: what get.devplat.ch
# serves and what the Download page's checksum table lists. Linux + Windows,
# both amd64, for now — macOS/arm64 are a later addition (same layout, just
# more `build` calls below), not a rewrite.
#
# Usage: ./scripts/build-release.sh <version>   # e.g. v0.4.2
# Output: dist/<version>/{*.tar.gz,*.zip,checksums.txt}
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

echo "${VERSION}" > "${DIST_DIR}/../version.txt"

echo "==> done: ${DIST_DIR}"
ls -la "$DIST_DIR"
echo ""
echo "Publish by copying ${DIST_DIR} and dist/version.txt onto the"
echo "get.devplat.ch host — see devplat-cli/README.md's Distribution section."
