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
HTTPS endpoint. Over the existing SSH trust path (passwordless sudo required),
it reads the endpoint/token and clones the allowlisted local Pi profile into
protected worker control-plane storage. It writes connection details to a
mode-0600 `~/.config/scraps/client.json`, restarts the runner, and verifies that
the worker is independently ready before returning. Pass an explicit target
(`scrap attach operator@SCRAPS_VM`) to skip discovery; `make configure-remote-client
REMOTE=...` is the scripted equivalent.

Then:

```
pi
/scrap
```

For private repositories and pushes, run `scrap auth github`. It creates a
private GitHub App and lets you pick which repositories to grant; credentials
stay in the worker and are never exposed to workspace processes. Repository
workspaces can be created outside Pi with `scrap new --repo URL [project]`.
Git SSH/scp origins are normalized to HTTPS by scrapd, and missing authorization
or clone failures are returned as actionable API errors rather than generic
internal errors.

### Starting from scratch

Workspaces do not need a repository. `/scrap` in a non-git directory offers to
copy it into the new workspace as-is (`.git` and uncommitted changes included),
or the agent can build files from zero inside an empty workspace. Directory
transfers are explicit one-shot tar archives (ADR 0014):

```bash
scrap push [--replace] [<workspace-id>] <dir>   # local directory → workspace
scrap pull [--force] [<workspace-id>] [target]  # workspace → local directory
```

`push` requires an empty workspace unless `--replace` clears it first; `pull`
refuses to overwrite a non-empty target without `--force`. Git remains the
recommended transport for repository work — see
[ADR 0014](./docs/adr/0014-directory-push-and-pull-archives.md).

## Durable schedules (experimental)

`scrapd` has an execution-agnostic schedule clock. A schedule contains cron,
timezone, concurrency policy, and an opaque JSON payload—it does not contain a
repository, prompt, Pi configuration, or workflow.

```bash
curl -X POST "$SCRAP_DAEMON_URL/v1/schedules" \
  -H "Authorization: Bearer $SCRAP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "nightly factory event",
    "cron": "0 2 * * *",
    "timezone": "America/Toronto",
    "concurrencyPolicy": "skip",
    "payload": {"kind": "nightly-audit"}
  }'
```

Due schedules create durable occurrences. A separate software-factory harness
can claim an occurrence with `POST /v1/schedule-occurrences/claim`, then report
`completed` or `failed` using its lease token. Expired leases are reclaimable;
Scraps itself does not execute the payload. See
[ADR 0011](./docs/adr/0011-durable-execution-agnostic-schedules.md).

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

## Durable Pi turns

Scraps runs the complete Pi agent loop on the worker so an accepted turn
continues when the client or its Tailscale connection disappears. Worker
bundles include checksum-verified Node.js and a lockfile-pinned Pi runner.
`scrap attach user@worker` clones an allowlisted local Pi profile—including
protected `auth.json`, model declarations, skills, and prompt templates—over the
existing SSH/Tailscale trust path. The worker stores it outside OpenShell and
runs Pi with that cloned profile. `scraps-worker model-auth` remains available
only as a recovery/headless administration command.

Ordinary `pi` followed by `/scrap` durably captures the active local Pi branch
and routes every interactive prompt to remote turns. The worker imports that
branch once and becomes the authoritative conversation. Pi stores the run
binding locally, polls the append-only event log
over the same Tailscale Serve HTTPS endpoint, and reconnects on `/resume` or
`pi -c`. There is no separate durable user mode. `/scrap-cancel` cancels the
active remote run without treating an ordinary client disconnect as
cancellation. A worker that does not advertise `features.durableRuns` fails
closed and must be upgraded or configured; Scraps never silently falls back to
a laptop-owned agent loop. See the
[durable Pi handoff](./docs/handoff-durable-pi-default.md) for current
implementation status and remaining production work.

## Status

Scraps is an early prototype. OpenShell is the only workspace control plane,
and Lima is the first local VM driver. The Pi TUI runs locally, while the
durable agent loop runs on the worker with seven fail-closed, workspace-backed
tools: `bash`, `read`, `write`, `edit`, `ls`, `find`, and `grep`.

## Development

```bash
make check
make build
```

- [`SPEC.md`](./SPEC.md) — product direction
- [`docs/adr`](./docs/adr) — architecture
- [`cmd/scrap`](./cmd/scrap) / [`cmd/scrapd`](./cmd/scrapd) — CLI and daemon
- [`packages/pi-extension`](./packages/pi-extension) — Pi tools
