#!/usr/bin/env bash
# Exercises scripts/gen-release-key.sh against a throwaway copy of the repo.
#
# Never against the real tree: the point of the script is that it writes a
# private key and rewires three source files, and a test that does that in place
# leaves a test key in the repository — which is the one outcome the whole
# feature exists to prevent.
set -uo pipefail

W=$(mktemp -d)
trap 'rm -rf "$W"' EXIT
pass=0; fail=0
check() { if [ "$2" -eq 0 ]; then echo "PASS  $1"; pass=$((pass+1)); else echo "FAIL  $1"; fail=$((fail+1)); fi; }

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mkdir "$W/repo"
tar -C "$REPO_ROOT" --exclude=./dist --exclude=./.git -cf - . | tar -C "$W/repo" -xf -
cd "$W/repo"
git init -q . && git add -A >/dev/null 2>&1

./scripts/gen-release-key.sh > "$W/out.txt" 2>&1
check "gen-release-key.sh completes" $?

test -f devplat-release-key.pem
check "the private key is written" $?

[ "$(stat -c '%a' devplat-release-key.pem 2>/dev/null)" = "600" ]
check "the private key is chmod 600" $?

# The claim the script prints must be true.
git add -A >/dev/null 2>&1
git status --porcelain | grep -q "devplat-release-key.pem"
[ $? -ne 0 ]
check "the private key is gitignored and cannot be staged" $?

# The PEM block must no longer be the placeholder. Not "the file no longer
# contains that string": release.go legitimately keeps it as the constant the
# IsConfigured() switch compares against, and its own comments mention it.
for f in internal/release/release.go install.sh install.ps1; do
  python3 - "$f" >/dev/null 2>&1 <<'PY2'
import re, sys
block = re.search(r'-----BEGIN PUBLIC KEY-----.*?-----END PUBLIC KEY-----',
                  open(sys.argv[1]).read(), re.S).group(0)
sys.exit(1 if 'PLACEHOLDER' in block else 0)
PY2
  check "the key block in $f is a real key, not the placeholder" $?
done

# All three must hold the SAME key — the failure mode that breaks every user.
go test ./internal/release/ -run Consistency >/dev/null 2>&1
check "all three copies carry the same key" $?

# And the key in the files must be the public half of the key just generated.
openssl pkey -in devplat-release-key.pem -pubout 2>/dev/null > "$W/expected.pub"
python3 - "$W/expected.pub" >/dev/null 2>&1 <<'PY'
import re, sys
want = open(sys.argv[1]).read().strip()
got = re.search(r'-----BEGIN PUBLIC KEY-----.*?-----END PUBLIC KEY-----',
                open('internal/release/release.go').read(), re.S).group(0).strip()
sys.exit(0 if want == got else 1)
PY
check "the embedded key is the public half of the generated private key" $?

grep -q "12/12 passed" "$W/out.txt"
check "the script ran the full signing chain and it passed" $?

# Running it again must refuse rather than silently rotate.
./scripts/gen-release-key.sh > "$W/again.txt" 2>&1
[ $? -ne 0 ]
check "a second run refuses instead of overwriting the key" $?
grep -qi "rotat" "$W/again.txt"
check "the refusal explains what a rotation actually requires" $?

# A configured tree can then actually cut a signed release.
DEVPLAT_RELEASE_KEY="$W/repo/devplat-release-key.pem" ./scripts/build-release.sh v1.0.0 >/dev/null 2>&1
check "a signed release can be built with the new key" $?
test -f dist/v1.0.0/checksums.txt.sig
check "that release carries a signature" $?

echo
echo "$pass/$((pass+fail)) passed"
[ "$fail" -eq 0 ]
