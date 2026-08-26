# Scraps 🧰

**Give Pi a fresh computer without taking away what makes it yours.**

Scraps creates self-hosted agent workspaces while Pi stays comfortably local:
your skills, prompts, models, credentials, sessions, and TUI come with you;
project files and commands live in the workspace.

```text
local Pi + your personality  ─── remote tools ───▶  disposable dev computer
```

## Try it

Requirements: Go 1.24+, Node.js 22+, and pnpm 10+.

```bash
pnpm install
make up
scrap pi
```

Or arrive with a task:

```bash
scrap pi --repo https://github.com/you/project.git "fix the flaky test"
```

Useful commands:

```bash
scrap ls                 # workspaces waiting for you
scrap pi --workspace ID  # jump back in
scrap stop ID / start ID # pause and resume
scrap rm ID              # sweep up
scrap status             # check the daemon
scrap down               # lights out
```

## Docker workspaces

Directory workspaces remain the trusted-development default. For an isolated
Linux workspace, build the pinned image and switch providers in one command:

```bash
make docker-up
```

Override the image with `SCRAPD_DOCKER_IMAGE`. Docker Engine, Docker Desktop,
and OrbStack are supported through the Docker CLI.

## Where it is today

Scraps is an early prototype. Directory mode uses **literal directories and
host processes—it is not a security sandbox**. Docker mode adds an unprivileged
container and private volume with CPU, memory, and PID limits, but is not the
final hostile multi-tenant boundary. Proxmox VMs are the production
destination.

Pi already runs locally with seven fail-closed, workspace-backed tools:
`bash`, `read`, `write`, `edit`, `ls`, `find`, and `grep`.

## Hacking

```bash
make check
make build
make dev-daemon  # foreground logs
```

- [`SPEC.md`](./SPEC.md) — product direction
- [`docs/adr`](./docs/adr) — architecture and sandbox roadmap
- `cmd/scrap` / `cmd/scrapd` — Go CLI and daemon
- `packages/pi-extension` — remote-backed Pi tools

Built from scraps, naturally. ♻️
