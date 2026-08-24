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
packages/pi-extension/     extension loaded by Pi in every workspace
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

The Pi extension is TypeScript source because Pi loads TypeScript extensions
directly. In a workspace it receives its identity through environment variables:

```bash
SCRAP_WORKSPACE_ID=example \
SCRAP_PROJECT=owner/repository \
SCRAP_DAEMON_URL=http://scrapd.internal:8484 \
pi --extension packages/pi-extension/src/index.ts
```

## Configuration

`scrapd` currently recognizes:

- `SCRAPD_LISTEN_ADDR` — HTTP listen address (default `127.0.0.1:8484`)

The daemon currently exposes `GET /healthz` and `GET /v1/info`. Workspace APIs
will be added once their state model is implemented.
