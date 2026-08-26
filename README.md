# Scraps 🧰

**Give Pi a fresh computer without taking away what makes it yours.**

Scraps creates self-hosted agent workspaces while Pi stays comfortably local:
your skills, prompts, models, credentials, sessions, and TUI come with you;
project files and commands live in the workspace.

```text
local Pi + your personality  ─── remote tools ───▶  disposable dev computer
```

## Try it

Requirements: Docker, Go 1.24+, Node.js 22+, pnpm 10+, and `curl`.
`make up` installs and starts the pinned OpenShell release when needed.

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

OpenShell workspaces are the default. `make up` installs the pinned OpenShell
release, boots its local gateway, builds the Scraps workspace image, and starts
`scrapd`. OpenShell owns sandbox lifecycle, network policy, credential providers,
and resource controls while Scraps preserves the Pi tools and `/workspace`
contract. Override the image with `SCRAPD_OPENSHELL_IMAGE`.

The direct Docker backend remains available with `SCRAPD_PROVIDER=docker make
up`; override its image with `SCRAPD_DOCKER_IMAGE`. For trusted provider
development without isolation, use `SCRAPD_PROVIDER=directory make up`.

## Where it is today

Scraps is an early prototype. Directory mode uses **literal directories and
host processes—it is not a security sandbox**. OpenShell is now the default
sandbox control plane, backed locally by its detected container runtime. The
intended deployment places the whole OpenShell/container pool inside one
protective Proxmox VM.

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
