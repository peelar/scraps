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
make configure  # choose worker CPU, memory, and disk sizing
make up
pi
# Then, inside Pi:
/scrap
```

`make configure` stores worker sizing in `~/.config/scraps/worker.conf` (the
XDG config directory is respected). The defaults are 4 CPUs, 8 GiB memory, and
60 GiB disk. Size changes apply when the VM is created; use `make vm-delete`
before recreating an existing VM at a different size.

`make up` creates an ordinary Linux worker VM with Lima, copies Linux Scraps
binaries into it, installs OpenShell there, builds the workspace image, and
starts `scrapd`. It does not mount your host home or checkout into the VM. Pi,
the `scrap` client, your configuration, and the TUI remain local. Use `make
down` to stop the VM, `make vm-shell` for diagnostics, and `make vm-delete` to
remove the VM and all of its workspace data.

The packaged-user flow is ready for future release archives and Homebrew, but
has not been published yet. The worker VM is the only supported deployment;
Scraps does not run workspace processes directly on the developer host.

`/scrap [project]` creates a workspace, switches all project-facing tools to
it, and fails closed if the daemon cannot be reached. Attach to an existing
workspace with `/scrap-select ID`. `/scrap toss` permanently deletes the
attached workspace and returns tools and `!`/`!!` to the local computer. The
workspace association is stored in the Pi session, so `/resume` reconnects to
the same workspace unless it was tossed; a new Pi session starts locally until
activated.

Useful commands:

```bash
scrap ls                 # workspaces waiting for you
scrap stop ID / start ID # pause and resume
scrap rm ID              # sweep up
scrap status             # check the daemon
scrap auth github        # grant selected repositories to a GitHub App
make down                # stop the local worker VM
```

OpenShell workspaces are the default. OpenShell owns sandbox lifecycle, network
policy, credential providers, and resource controls while Scraps preserves the
Pi tools and `/workspace` contract. The worker VM supplies the host-protection
boundary; OpenShell containers supply economical per-workspace separation.

For private repositories and pushes, run `scrap auth github`. Scraps creates a
private self-hosted GitHub App and opens GitHub's repository picker. Its App key
stays in the worker control plane; one-hour installation credentials are minted
and refreshed automatically, brokered through OpenShell, and never exposed to
sandbox processes. The attached provider permits Git fetch/push while GitHub
API mutations, workflow dispatch, and repository administration stay blocked.

## Where it is today

Scraps is an early prototype. OpenShell is the only workspace control plane.
The local deployment places the entire OpenShell container pool inside one
ordinary Linux worker VM. Lima is the first local VM
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
make vm-shell  # worker diagnostics
```

- [`SPEC.md`](./SPEC.md) — product direction
- [`docs/adr`](./docs/adr) — architecture and sandbox roadmap
- `cmd/scrap` / `cmd/scrapd` — Go CLI and daemon
- `packages/pi-extension` — remote-backed Pi tools

Built from scraps, naturally. ♻️
