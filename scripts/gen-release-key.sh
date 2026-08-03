#!/usr/bin/env bash
# Generates the devplat release signing key AND wires it into all three places
# that hold it. One command, run once, on a machine you control.
#
#   ./scripts/gen-release-key.sh
#
# It does the whole job on purpose. The earlier version generated a key and told
# you to paste it into internal/release/release.go, install.sh and install.ps1 —
# which is three chances to do two of three. A key rotation that lands in the
# binary but not in the published installer tells every user that a genuine
# release failed verification, and the thing they learn from that is to reach
# for the skip flag. So the pasting is not left to a human.
#
# WHERE TO RUN THIS
#
# On your own machine. Not on the VPS that serves get.devplat.ch, not in CI, not
# in a container you do not control and cannot attest to. The entire value of
# this key is that its private half lives somewhere an attacker who owns the
# release host does not — copying it onto that host, into this repository, or
# through any channel that keeps a log, converts the whole scheme back into the
# bare checksum file it was built to replace.
#
# Writes:
#   devplat-release-key.pem   the private key, chmod 600 — never commit, never upload
#   (the public half is written straight into the three source files)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PRIV="${REPO_ROOT}/devplat-release-key.pem"
MARKER="PLACEHOLDER-NO-RELEASE-KEY-CONFIGURED"

GO_FILE="${REPO_ROOT}/internal/release/release.go"
SH_FILE="${REPO_ROOT}/install.sh"
PS_FILE="${REPO_ROOT}/install.ps1"

# Rotation is not this script's job, and doing it by accident would be bad.
#
# Clients hold the old key until they upgrade, so a straight swap makes every
# installed CLI reject the next genuine release. A rotation has to publish one
# release signed with the OLD key that carries the NEW one, wait for people to
# move, and only then switch. Silently overwriting here would skip all of that.
# Ordered before the "does the key file exist" check on purpose. Both mean "not
# again", but only this one explains the thing the user actually needs to know,
# and after a successful run both are true — so whichever fires first is the
# message they get.
# Reads the PEM block, not the file. release.go legitimately keeps the marker as
# the constant IsConfigured() compares against, and mentions it in comments, so a
# plain `grep "$MARKER" release.go` succeeds forever — this check never fired,
# and on a configured tree whose key file had been moved to a password manager
# (which is exactly what step 1 below tells you to do) the script would have
# generated a second key and silently overwritten a live one, skipping the whole
# rotation warning. Found by scripts/verify-keysetup.sh, not by reading.
configured() {
  python3 - "$GO_FILE" <<'PY_CFG'
import re, sys
block = re.search(r"-----BEGIN PUBLIC KEY-----.*?-----END PUBLIC KEY-----",
                  open(sys.argv[1]).read(), re.S)
sys.exit(1 if block is None or "PLACEHOLDER" in block.group(0) else 0)
PY_CFG
}

if configured; then
  cat >&2 <<'MSG'
a release key is already configured.

Rotating is a sequence, not a swap: everyone who has already installed holds the
old key, so replacing it outright means the next genuine release fails
verification for all of them at once. Publish one release signed with the OLD
key whose binary and install scripts carry the NEW key, give people time to
upgrade, and switch signing over after that.

If you are certain you want to start over — for instance nothing has shipped
yet — restore the placeholder in internal/release/release.go, install.sh and
install.ps1, then run this again.
MSG
  exit 1
fi

if [ -e "$PRIV" ]; then
  echo "refusing to overwrite an existing $PRIV." >&2
  echo "  The sources still hold the placeholder, so a previous run stopped" >&2
  echo "  part-way. Either finish it by putting that key's public half into" >&2
  echo "  release.go, install.sh and install.ps1, or move $PRIV aside and rerun." >&2
  exit 1
fi

command -v openssl >/dev/null 2>&1 || { echo "openssl is required" >&2; exit 1; }

echo "==> generating an Ed25519 keypair"
umask 077
openssl genpkey -algorithm ed25519 -out "$PRIV"
chmod 600 "$PRIV"
PUB="$(openssl pkey -in "$PRIV" -pubout)"

echo "==> writing the public key into the binary and both installers"
PUB="$PUB" python3 - "$GO_FILE" "$SH_FILE" "$PS_FILE" <<'PY'
import os, re, sys

pub = os.environ["PUB"].strip()
placeholder = re.compile(
    r"-----BEGIN PUBLIC KEY-----\s*\n\s*PLACEHOLDER-NO-RELEASE-KEY-CONFIGURED\s*\n\s*-----END PUBLIC KEY-----"
)
for path in sys.argv[1:]:
    text = open(path).read()
    patched, n = placeholder.subn(pub, text)
    if n != 1:
        # Better to stop with two of three files written and say so than to
        # leave a half-configured tree that looks finished.
        sys.exit(f"expected exactly one placeholder in {path}, found {n}")
    open(path, "w").write(patched)
    print(f"    {os.path.relpath(path)}")
PY

echo "==> checking the three copies agree"
( cd "$REPO_ROOT" && go test ./internal/release/ -run Consistency )

echo "==> proving the whole signing chain end to end"
# Independent of the key just generated: verify-signing.sh makes its own
# throwaway one. What it proves here is that a configured tree still builds,
# signs, installs, and refuses both a tampered and an unsigned release.
( cd "$REPO_ROOT" && ./scripts/verify-signing.sh )

cat <<MSG

Done. Three things follow.

1. Store $PRIV somewhere durable and private — a password manager entry or an
   offline encrypted volume. If you lose it you cannot sign again without a
   rotation; if someone else gets it they can sign releases as you.

2. It must never be committed. Confirm:
     git status --short   # devplat-release-key.pem must not appear
   (.gitignore already covers it.)

3. Review and commit the three patched files, then cut a signed release:
     git diff -- internal/release/release.go install.sh install.ps1
     DEVPLAT_RELEASE_KEY=$PRIV ./scripts/build-release.sh vX.Y.Z

From the moment that release is published, clients built from this tree refuse
any release whose signature is missing or wrong.
MSG
