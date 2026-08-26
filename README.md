# Scraps

Give your agents a private cloud, hosted on the scraps of your own compute.

Scraps runs each workspace as an OpenShell container on a self-hosted worker
VM and points Pi's tools at it. Pi itself stays exactly as you know it — same
skills, prompts, models, credentials, sessions, and TUI — it just runs on
that remote machine. Project files and commands live in the workspace and
can be deleted at any time.

## Install

Prebuilt binaries are published for macOS and Linux (amd64/arm64).

```bash
curl -fsSL https://raw.githubusercontent.com/peelar/scraps/main/scripts/install.sh | bash
```

Pin a release with `SCRAPS_VERSION=v0.1.0`.

```bash
brew install peelar/tap/scrap
```

```bash
go install github.com/peelar/scraps/cmd/scrap@latest
go install github.com/peelar/scraps/cmd/scrapd@latest   # only for a remote worker
```

## Try it

From a source checkout (requires Go 1.25+, Node.js 22+, pnpm 10+, and
[Lima 2.0+](https://lima-vm.io/)):

```bash
pnpm install
make configure  # worker VM CPU, memory, and disk sizing
make up
```

Defaults are 4 CPUs, 8 GiB memory, and 60 GiB disk. Sizing applies when the VM
is created; use `make vm-delete` before recreating an existing VM at a
different size. `make up` creates the worker VM, installs OpenShell and the
workspace image, and starts the daemon. `make down` stops the VM;
`make vm-delete` removes it and all workspace data.

Then start Pi and activate the workspace:

```
pi
/scrap
```

`/scrap [project]` creates a workspace and switches Pi's project tools to it.
`/scrap-select ID` attaches to an existing one, and `/scrap toss` permanently
deletes the attached workspace and returns tools to the local machine. The
workspace association is stored in the Pi session, so `/resume` reconnects to
the same workspace.

See `scrap --help` for all client commands.

## Remote worker

The default setup runs the worker VM locally with Lima. To host it on a remote
machine instead, see the [Proxmox + Tailscale runbook](./docs/homelab-nuc.md).
In short, from a source checkout — worker side:

```bash
make deploy-worker REMOTE=operator@SCRAPS_VM   # also the upgrade command
sudo scraps-worker tailscale-serve
```

and client side:

```bash
make install
scrap attach
scrap status
```

`scrap attach` finds the worker on your tailnet by probing every online peer
tagged `tag:scraps-worker` (or named `scraps-worker*`) on its Tailscale Serve
HTTPS endpoint, then reads the endpoint and bearer token over the existing
SSH trust path (passwordless sudo required) and writes them to a mode-0600
`~/.config/scraps/client.json` after verifying the daemon answers an
authenticated `/v1/info`. Pass an explicit target (`scrap attach
operator@SCRAPS_VM`) to skip discovery; `make configure-remote-client
REMOTE=...` is the scripted equivalent.

Then:

```
pi
/scrap
```

For private repositories and pushes, run `scrap auth github`. It creates a
private GitHub App and lets you pick which repositories to grant; credentials
stay in the worker and are never exposed to workspace processes.

## Environment variables

For software that genuinely requires a raw environment variable, approve its
name once, then start Pi with the value in its environment:

```bash
scrap env allow DATABASE_URL STRIPE_API_KEY
op run --env-file=.env.1password -- pi   # or: doppler run -- pi / infisical run -- pi
```

Scraps stores only approved names, never their values. Run `scrap env list`,
`scrap env deny NAME`, or `scrap env clear` to inspect or revoke approvals,
then restart Pi. An approval is global to every Scraps workspace used from
that client profile. Approved values are intentionally readable by every
command and all code in the sandbox; prefer `scrap auth github` and other
brokered credentials when they are available.
Raw values are sent only over HTTPS, except when the daemon is on loopback.

You do not need to memorize this flow. While a workspace is active, the Pi
agent is told which approved variable names were loaded or missing. If software
reports a missing variable, the agent will explain the boundary and give you
the exact local command. Scraps never asks you to paste the value into chat.

## Preview a running service

When the agent starts a dev server inside a workspace, `scrap open` tunnels
it onto your machine:

```bash
scrap open                 # auto-detects the workspace and port
scrap open quiet-river 5173   # explicit workspace and port
```

Pi shows a short hint when a workspace port starts listening; `scrap ls` also
shows active ports. The local listener binds `127.0.0.1` only and opens your
browser at `http://localhost:<port>`. Each browser connection streams through scrapd's
authenticated API into the workspace's loopback interface — the same channel
every other tool uses — so no workspace port is ever published to a network,
locally or on the tailnet. Interactive traffic (Vite HMR websockets and the
like) passes through unchanged, and edits the agent makes appear in your
browser as the dev server reloads.

## Status

Scraps is an early prototype. OpenShell is the only workspace control plane,
and Lima is the first local VM driver. Pi runs locally with seven fail-closed,
workspace-backed tools: `bash`, `read`, `write`, `edit`, `ls`, `find`, and
`grep`.

## Development

```bash
make check
make build
```

- [`SPEC.md`](./SPEC.md) — product direction
- [`docs/adr`](./docs/adr) — architecture
- [`cmd/scrap`](./cmd/scrap) / [`cmd/scrapd`](./cmd/scrapd) — CLI and daemon
- [`packages/pi-extension`](./packages/pi-extension) — Pi tools
