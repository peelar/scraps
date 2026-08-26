# Scraps 🧰

**Give Pi a fresh computer without taking away what makes it yours.**

Scraps creates self-hosted agent workspaces while Pi stays comfortably local:
your skills, prompts, models, credentials, sessions, and TUI come with you;
project files and commands live in the workspace.

```text
local Pi + your personality  ─── remote tools ───▶  disposable dev computer
```

## Try it

From a source checkout, install the contributor dependencies (Go 1.25+, Node.js
22+, pnpm 10+, and [Lima 2.0+](https://lima-vm.io/)) and bootstrap everything:

```bash
pnpm install
make up
pi
# Then, inside Pi:
/scrap
```

`make up` creates an ordinary Linux worker VM with Lima, copies Linux Scraps
binaries into it, installs OpenShell there, builds the workspace image, and
starts `scrapd`. It does not mount your host home or checkout into the VM. Pi,
the `scrap` client, your configuration, and the TUI remain local. Use `make
down` to stop the VM, `make vm-shell` for diagnostics, and `make vm-delete` to
remove the VM and all of its workspace data.

The packaged-user flow is ready for future release archives and Homebrew, but
has not been published yet. `scrap setup` followed by `scrap up` remains the
explicit, weaker-boundary host mode; contributors can invoke it with `make
host-up`.

`/scrap [project]` creates a workspace, switches all project-facing tools to
it, and fails closed if the daemon cannot be reached. Attach to an existing
workspace with `/scrap-select ID`.

Useful commands:

```bash
scrap ls                 # workspaces waiting for you
scrap stop ID / start ID # pause and resume
scrap rm ID              # sweep up
scrap status             # check the daemon
scrap auth github        # broker a fine-grained PAT for fetch/push
make down                # stop the local worker VM
```

OpenShell workspaces are the default. OpenShell owns sandbox lifecycle, network
policy, credential providers, and resource controls while Scraps preserves the
Pi tools and `/workspace` contract. The worker VM supplies the host-protection
boundary; OpenShell containers supply economical per-workspace separation.

For private repositories and pushes, create a fine-grained GitHub PAT limited
to selected repositories with **Contents: read/write**, then run `scrap auth
github`. Scraps sends it to the OpenShell gateway inside the worker VM without
putting it in process arguments, attaches a push-only provider to workspaces,
and leaves GitHub API mutations blocked. `--from-gh` imports the host `gh`
credential; `--token-stdin` reads from a password manager or pipe.

The direct Docker backend remains available for host development with
`SCRAPD_PROVIDER=docker make host-up`. For trusted development without
isolation, use `SCRAPD_PROVIDER=directory make host-up`.

## Where it is today

Scraps is an early prototype. Directory mode uses **literal directories and
host processes—it is not a security sandbox**. OpenShell is the default sandbox
control plane. The default local deployment places the entire OpenShell
container pool inside one ordinary Linux worker VM. Lima is the first local VM
driver, not an architectural requirement; a VM hosted by Proxmox is one future
remote deployment target.

Pi already runs locally with seven fail-closed, workspace-backed tools:
`bash`, `read`, `write`, `edit`, `ls`, `find`, and `grep`.

## Packaging status

Release packaging is defined in `.goreleaser.yml` for macOS and Linux on amd64
and arm64. Releases are intentionally drafted, and no Homebrew tap or formula
has been published yet.

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
