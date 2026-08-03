# devplat-cli

The client CLI described on [devplat's Download page](https://devplat.ch/download):
a static Go binary that requests a remote Testcontainers microVM from the
devplat control plane, tunnels its Docker API back to a local TCP port, and
prints the `DOCKER_HOST` export for whatever test command runs next.

```
$ devplat connect --token $DEVPLAT_TOKEN
Requesting an environment…
✓ Assigned (request 6aea97af-...)
✓ Tunnel active

  export DOCKER_HOST=tcp://127.0.0.1:52731

Run your test command in this shell, or eval the line above. Ctrl+C to disconnect.
```

## How it fits together

This repo is the piece of the system that runs on a user's laptop or CI
runner — not to be confused with
[`devplat-agent`](https://github.com/theslasher5g/devplat-agent), which runs
on devplat's own Firecracker hosts and manages the microVMs themselves.

```
this CLI  --(REST, public internet)-->  devplat-backend  --(WireGuard)-->  devplat-agent + VM's dockerd
          <--(WebSocket tunnel)------                  <-------------
```

A VM's Docker daemon only accepts connections sourced from the control
plane's own WireGuard subnet (see devplat-backend's security model docs) —
this CLI is never on that mesh, so it can't reach a VM directly. Instead:

1. `POST /environments` asks the scheduler for a VM; `GET /environments/:id`
   is polled until it's `assigned` (or the request fails/queues).
2. The CLI opens a local TCP listener and prints its `DOCKER_HOST` export.
3. For each connection a Docker client makes to that local port, the CLI
   opens a **new** WebSocket connection to
   `{apiURL}/environments/{id}/tunnel` (Bearer-token authenticated) and
   relays raw bytes both ways. The control-plane server dials the VM's
   `docker_endpoint` over its own WireGuard link and does the same relay on
   its side — see `devplat-backend/src/routes/tunnel.ts`.
4. Ctrl+C releases the environment (`DELETE /environments/:id`) before the
   process exits.

One WebSocket per local TCP connection, not one multiplexed over many — this
keeps the relay a straightforward byte pipe with no framing protocol of its
own.

## Building

```
go build ./cmd/devplat
```

Requires Go 1.23+. No other local dependencies — this binary talks to the
control plane over HTTPS/WSS only.

```
go test ./...
```

## Usage

```
devplat login   [--token TOKEN] [--api-url URL]
devplat connect [--token TOKEN] [--api-url URL] [--exec "CMD"]
devplat doctor  [--token TOKEN] [--api-url URL]
devplat upgrade
devplat logout
devplat version
```

`devplat doctor` runs a read-only self-check — CLI version and available
updates, which token is in use and where it came from, control-plane
reachability, and whether that token is accepted — and creates nothing.
`devplat upgrade` pulls the latest release via the official installer.
Interactive `devplat connect` also prints a one-line notice when a newer
CLI is available (CI runs with `--exec` stay silent).

Token resolution: `--token` flag, then `DEVPLAT_TOKEN` env var, then the token
saved by `devplat login`. Create a scoped `ci:run` token in the dashboard under
Tokens — the same flow the Download page's "CI runners" tab documents.
`--api-url` (or `DEVPLAT_API_URL`) overrides the control-plane URL; defaults to
`https://api.devplat.ch`. Mainly useful for local development against a
non-production backend.

`devplat login` with no flags runs the browser device-code flow: it prints a
short code, you approve it in the dashboard, and the resulting token is saved
to your user config dir. `devplat logout` revokes it server-side and removes
it locally, so a logged-out machine can't keep starting environments.

## Account restrictions the CLI reports

The control plane can reject a request for reasons that have nothing to do with
capacity, and a raw JSON error in a CI log at 3am is useless. `internal/apiclient/errors.go`
maps each code to advice that names the command to run:

| Code | What it means |
|---|---|
| `api_token_expired` | The token had an expiry date and passed it. Create a new one, then `devplat login --token <new>`. |
| `invalid_api_token` | Revoked or never valid. |
| `ip_not_allowed` | The token has an IP allowlist and this address isn't in it — CI runners usually egress from a different range than a laptop. |
| `two_factor_required` | The team requires 2FA and this account hasn't enrolled. |
| `seat_limit_reached` | The team's plan has no seats left. |
| `session_revoked` | The session was signed out elsewhere. |
| `email_not_verified` | The account's address hasn't been confirmed. |

Codes not in that table fall through to the server's own wording, so a new
server-side error is never swallowed. `devplat doctor` surfaces the same
diagnosis without creating anything — it's the first thing to run when a
previously working token stops working.

## Distribution

Two platforms published so far, both amd64: Linux and Windows. macOS/arm64
are a later addition — same pipeline, just more `build` calls in the script
below, not a rewrite.

**Cutting a release:**
```bash
./scripts/build-release.sh v0.4.2
# -> dist/v0.4.2/{devplat-v0.4.2-linux-amd64.tar.gz,
#                 devplat-v0.4.2-windows-amd64.zip,
#                 checksums.txt}
# -> dist/version.txt (just "v0.4.2")
```

**Release signing.** `checksums.txt` on its own proves only that a download
wasn't truncated: it sits on the same host as the archives it describes, so
whoever can replace a binary can rewrite the checksum beside it. Every release
is therefore signed with an Ed25519 key that never touches the release host.

```bash
# once, on a machine you control — never on the VPS, never in CI:
./scripts/gen-release-key.sh
# then paste the public key into all three of:
#   internal/release/release.go  (PublicKeyPEM)
#   install.sh                   (RELEASE_PUBKEY)
#   install.ps1                  ($ReleasePubKey)
# internal/release/pubkey_consistency_test.go fails the build if they drift.

DEVPLAT_RELEASE_KEY=~/devplat-release-key.pem ./scripts/build-release.sh v0.4.2
```

`build-release.sh` refuses to package an unsigned release unless
`DEVPLAT_ALLOW_UNSIGNED=1`, and verifies the signature it just wrote before
finishing — publishing one nobody can check would fail closed for every user at
once.

Who checks what:

| path | signature | notes |
|---|---|---|
| `install.sh` | yes, via `openssl` | refuses to install if the signature is missing or wrong |
| `devplat upgrade` | yes, in Go | no external tool needed; downloads and replaces the binary itself |
| `install.ps1` | only if `openssl` is on PATH | Windows PowerShell 5.1 has no Ed25519; it says so and points at `devplat upgrade` |

A *missing* signature is a hard failure, not a reason to skip the check — if it
were skippable, an attacker holding the host would simply delete the file.
`checksums.txt.sig` must therefore be published alongside `checksums.txt`.

**Publishing to get.devplat.ch** (a static file server — see
`deploy/docker-compose.get.yml` + `deploy/nginx.get.conf`, same
paste-into-the-VPS's-compose-file convention as `devplat-backend`'s and
`devplat-agent`'s own `deploy/` snippets):
```bash
# on the VPS, /opt/devplat/get/public is the volume mount target
mkdir -p /opt/devplat/get/public/v0.4.2
cp dist/v0.4.2/* /opt/devplat/get/public/v0.4.2/   # includes checksums.txt.sig
cp dist/version.txt /opt/devplat/get/public/version.txt
cp dist/devplat-release.pub.pem /opt/devplat/get/public/   # for manual verification
cp dist/install.sh dist/install.ps1 /opt/devplat/get/public/   # carry the same key as the release

# first time only: copy deploy/docker-compose.get.yml's `get:` service
# block into /opt/devplat/docker-compose.yml, and
# deploy/nginx.get.conf to /opt/devplat/get/nginx.get.conf
docker compose up -d get
```
Old version directories are left in place (immutable, same reasoning as
the golden image versioning in `devplat-agent`) — nothing currently
references anything but `version.txt`'s pointer, so nothing breaks by
keeping them around; delete them manually whenever it's worth reclaiming
the disk space.

**What users actually run:**
```bash
# Linux (also covers CI runners — same script, headless)
curl -fsSL https://get.devplat.ch | sh

# Windows (PowerShell)
irm https://get.devplat.ch/install.ps1 | iex
```
Both scripts resolve `version.txt`, download the matching archive +
`checksums.txt` (+ `checksums.txt.sig`), verify the signature and then the
hash, and install onto `PATH` (`/usr/local/bin`
on Linux — falls back to `sudo` if that's not writable; `%LOCALAPPDATA%\devplat\bin`
on Windows, added to the user `PATH`).
