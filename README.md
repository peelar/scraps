# Scraps

Self-hosted agent workspaces on hardware you already own.

This repository contains the Linux-targeted Scraps control plane, CLI, and agent
integration. macOS is supported as a development environment and as a future
CLI client; production workloads will run on Linux.

See [SPEC.md](./SPEC.md) for the product direction and
[docs/adr](./docs/adr) for accepted architectural decisions.

## Repository layout

```text
cmd/scrap/                 local CLI
cmd/scrapd/                Linux daemon
internal/                  private Go packages
packages/pi-extension/     Pi extension: local Pi, remote workspace tools (ADR 0001)
```

## Requirements

- Go 1.24+
- Node.js 22+
- pnpm 10+

## Development

```bash
pnpm install
make check
make build
```

Built executables are written to `bin/`. Start the development daemon with
`make dev-daemon`; it listens on `127.0.0.1:8484` by default.

## Pi integration

Per [ADR 0001](./docs/adr/0001-local-pi-with-remote-workspace-tools.md), the
canonical interactive integration runs Pi locally and executes the seven
project-facing tools (`bash`, `read`, `write`, `edit`, `ls`, `find`, `grep`)
in a remote Scraps workspace:

```bash
pi --extension packages/pi-extension/src/index.ts --scrap [--workspace <id>]
```

`scrap pi [prompt]` will become the convenience launcher for this. While
`--scrap` is active the tools fail closed — with no reachable workspace they
error visibly and never fall back to the local machine.

`packages/pi-extension/scripts/dev-scrapd.mjs` is a stand-in daemon for
developing the extension until the Go daemon implements the workspace API
(the endpoint contract lives in `packages/pi-extension/src/client.ts`):

```bash
pnpm --filter @scraps/pi-extension dev:scrapd
SCRAP_DAEMON_URL=http://127.0.0.1:8484 SCRAP_WORKSPACE_ID=dev \
  pi --extension packages/pi-extension/src/index.ts --scrap
```

## Configuration

`scrapd` currently recognizes:

- `SCRAPD_LISTEN_ADDR` — HTTP listen address (default `127.0.0.1:8484`)

The daemon currently exposes `GET /healthz` and `GET /v1/info`. Workspace APIs
will be added once their state model is implemented.
