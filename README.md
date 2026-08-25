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
in a remote Scraps workspace. The daemon API is specified in
[ADR 0002](./docs/adr/0002-scrapd-workspace-api.md).

The convenience launcher resolves or creates a workspace, then starts local
Pi with the extension (embedded in the `scrap` binary) and fail-closed tool
replacement:

```bash
scrapd &                       # or: make dev-daemon
scrap pi                       # fresh workspace, interactive Pi
scrap pi "fix the flaky test"  # fresh workspace + opening prompt
scrap pi --workspace <id>      # attach to an existing workspace
scrap pi --repo <http-url>     # clone a (public) repo into the workspace
scrap ls                       # list workspaces
scrap rm <id>...               # remove workspaces
```

Set `SCRAP_EXTENSION_PATH` to point `scrap pi` at a source checkout of the
extension during development. While `--scrap` is active the tools fail
closed — with no reachable workspace they error visibly and never fall back
to the local machine.

An LLM-free integration harness for the full extension↔daemon surface lives
at `packages/pi-extension/scripts/integration.ts`:

```bash
node packages/pi-extension/scripts/integration.ts http://127.0.0.1:8484 [workspace-id]
```

## Configuration

`scrapd` recognizes:

- `SCRAPD_LISTEN_ADDR` — HTTP listen address (default `127.0.0.1:8484`)
- `SCRAPD_DATA_DIR` — data directory for the SQLite store and workspace
  directories (default `~/.config/scrapd`)
- `SCRAPD_TOKEN` — when set, all `/v1` requests require
  `Authorization: Bearer <token>`

`scrap` recognizes `SCRAP_DAEMON_URL` and `SCRAP_TOKEN`. The workspace API
covers lifecycle (`POST/GET/DELETE /v1/workspaces`), streaming execution
(`POST /v1/workspaces/{id}/exec`, NDJSON events), and file/search operations
(`POST /v1/workspaces/{id}/files/{read,write,mkdir,stat,access,readdir,glob,grep}`).
