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

## Usage

```
devplat connect [--token TOKEN] [--api-url URL]
devplat version
```

Token resolution: `--token` flag, then `DEVPLAT_TOKEN` env var. Create a
scoped `ci:run` token in the dashboard under Tokens — the same flow the
Download page's "CI runners" tab documents. `--api-url` (or
`DEVPLAT_API_URL`) overrides the control-plane URL; defaults to
`https://api.devplat.ch`. Mainly useful for local development against a
non-production backend.

## Status

v1: token-auth `connect` only. `devplat login` (browser device-code flow +
OS keychain storage, for interactive local use without a pre-issued token)
is not yet implemented — token auth via `--token`/`DEVPLAT_TOKEN` covers CI
and local dev today.

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

**Publishing to get.devplat.ch** (a static file server — see
`deploy/docker-compose.get.yml` + `deploy/nginx.get.conf`, same
paste-into-the-VPS's-compose-file convention as `devplat-backend`'s and
`devplat-agent`'s own `deploy/` snippets):
```bash
# on the VPS, /opt/devplat/get/public is the volume mount target
mkdir -p /opt/devplat/get/public/v0.4.2
cp dist/v0.4.2/* /opt/devplat/get/public/v0.4.2/
cp dist/version.txt /opt/devplat/get/public/version.txt
cp install.sh install.ps1 /opt/devplat/get/public/

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
`checksums.txt`, verify the hash, and install onto `PATH` (`/usr/local/bin`
on Linux — falls back to `sudo` if that's not writable; `%LOCALAPPDATA%\devplat\bin`
on Windows, added to the user `PATH`).
