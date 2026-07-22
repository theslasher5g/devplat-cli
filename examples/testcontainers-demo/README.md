# devplat CLI test project

A throwaway project for exercising `devplat connect` end to end — every TUI
feature, plus a real Testcontainers `mvn verify` run against the remote VM.
Nothing here is deployed anywhere; it only exists to be pointed at with the
CLI.

## Setup

```bash
cd examples/testcontainers-demo
devplat login          # or export DEVPLAT_TOKEN=dvp_...
devplat connect
```

Run everything below **inside** the TUI shell that opens (it already has
`DOCKER_HOST` pointed at the tunnel).

## Checklist

| # | Feature | How to trigger it | What you should see |
|---|---|---|---|
| 1 | Container sidebar + live port mirroring | `docker compose up -d` | `postgres`, `redis`, `heartbeat` appear in the sidebar within a couple seconds; postgres/redis show `→ localhost:5432` / `→ localhost:6379` |
| 8 | Bind-mount warning | happens automatically on connect (compose file has one) | a one-time warning in the output pane naming `heartbeat: ./demo-logs → /var/log/demo` — `./demo-logs` is on the **VM**, not your laptop, so it stays empty locally even though the container is writing to it |
| — | Logs overlay | Tab to the sidebar, select `postgres`, press `^l` | a scrollable log pane opens for that container |
| — | Copy port | with a container selected, press `^y` | its mapped local port is copied to your clipboard |
| — | Command history / picker | run a command (e.g. `docker ps`), then press `^r` | picker opens with that command; `^s` while typing a command stars it for next time |
| 2 | Resource HUD + TTL | just look at the header after connecting | vCPU / RAM / region and a live TTL countdown for this environment |
| 6 | Platform status | header, top right | current devplat status (pulled from `/status`) |
| 7 | Parallel usage | open a second `devplat connect` elsewhere while this one is up | header shows `N/M envs` for your account |
| — | Real Testcontainers proof | `mvn verify` (needs a JDK 17+ and Maven on your machine — only the containers run remotely) | `PostgresIT` spins up `postgres:16-alpine` **on the remote VM**, gets a mapped port back through the tunnel, and runs a live `select 1` against it. A green build here is the actual end-to-end proof that agent → backend tunnel → CLI port-mirroring works, not just each piece in isolation. |

## Cleanup

```bash
docker compose down -v
exit   # or Ctrl+D / Ctrl+C — releases the environment
```
