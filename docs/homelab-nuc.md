# Homelab NUC worker runbook

This runbook deploys Scraps to an ordinary Ubuntu 24.04 LTS VM on Proxmox.
Proxmox is only the VM host: no Proxmox credentials or concepts enter Scraps'
workspace API. The service is private behind Tailscale Serve and also requires
a bearer token.

## Proxmox installation path

This is the authoritative installation path for Proxmox. It intentionally
starts with a pre-created VM and records which steps are automated so the
manual surface can shrink without hiding the security decisions that remain.

| Step | Current command or responsibility | Automation |
| --- | --- | --- |
| Create and size dedicated Ubuntu VM | Proxmox operator | Manual |
| Patch OS, configure SSH and guest agent | Proxmox/cloud-init operator | Manual |
| Join VM to Tailscale and apply grants | Tailnet operator | Manual |
| Install or upgrade Scraps payload | `make deploy-worker REMOTE=user@host` | Automated |
| Publish loopback service with Tailscale Serve | `sudo scraps-worker tailscale-serve` | One command |
| Configure the local CLI and Pi extension | `scrap attach` (or `make configure-remote-client REMOTE=user@host`) | Automated |
| Prove the installation | `make homelab-acceptance` | Automated core plus manual network checks |
| Configure encrypted backup destination | `scraps-worker enable-backups` | One-time manual policy choice |

ADR 0010 describes the target in which VM creation, secure enrollment, and
these existing commands are composed behind `scraps homelab install proxmox`.
When a step is automated, update this table rather than retaining obsolete
ceremony as part of the installation contract.

## 1. Create the VM

Create a full VM, not an LXC container, with Ubuntu Server 24.04 LTS, UEFI,
QEMU guest agent, VirtIO SCSI single, discard enabled, and a persistent 60 GiB
disk on storage covered by Proxmox backups. Start with 4 vCPU and 8 GiB RAM.
Enable **Start at boot**, use start order `20`, startup delay `30`, and shutdown
timeout `120`. Do not configure PCI/GPU passthrough for the initial worker.

During Ubuntu installation:

- create a normal sudo-capable operator account with an SSH public key;
- do not enable password SSH login or root SSH login;
- select UTC/time synchronization and a stable LAN DNS resolver;
- do not forward an SSH agent, mount a developer home/checkout, or expose a
  Docker socket from either the NUC or developer computer.

After first login, apply updates and reboot:

```bash
sudo apt update && sudo apt full-upgrade -y
sudo apt install -y qemu-guest-agent ufw
sudo systemctl enable --now qemu-guest-agent systemd-timesyncd
timedatectl status
resolvectl status
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow OpenSSH
sudo ufw enable
sudo reboot
```

Keep the Proxmox firewall enabled too. Do not add rules for TCP 8484, TCP
17670, or the Docker API. Tailscale Serve reaches `scrapd` through VM loopback.

## 2. Build and install the worker

On the developer checkout, build both versioned architectures (the NUC will
normally use amd64):

```bash
make deploy-worker REMOTE=operator@SCRAPS_VM
```

If the worker is represented by an SSH config alias, set
`SCRAPS_SSH_CONFIG=/path/to/ssh.config` and use the alias as `REMOTE`.

That command is also the routine deployment command. It detects the VM
architecture, disables SSH agent and port forwarding, and builds and uploads
the matching checksummed bundle. On a fresh VM it runs the installer through
`sudo`. On an existing worker it uses the guarded upgrade path and first runs
an encrypted application backup when backups have been configured. It prints
diagnostics after either path. The equivalent initial-install manual flow is:

```bash
make worker-bundles
scp dist/scraps-worker_*_linux_amd64.tar.gz operator@SCRAPS_VM:/tmp/worker.tar.gz
ssh operator@SCRAPS_VM
mkdir /tmp/scraps-worker && tar -xzf /tmp/worker.tar.gz -C /tmp/scraps-worker
sudo /tmp/scraps-worker/install
sudo scraps-worker diagnostics
```

The idempotent installer validates its checksums, installs/validates Docker,
installs pinned OpenShell `v0.0.113`, builds the digest-pinned workspace image,
and creates a locked-down system service. It uses the dedicated `scraps` user
with lingering enabled. Pi and its extension are deliberately not installed.
OpenShell's primary API remains on loopback; sandbox callbacks use only the
gateway address of a dedicated `openshell-scraps` Docker bridge.

Durable state is explicit:

| Path | Contents | Recovery treatment |
| --- | --- | --- |
| `/var/lib/scraps/scrapd` | SQLite and GitHub App key/state | Back up; secret |
| `/var/lib/scraps/openshell*` | OpenShell control-plane state and driver data | Back up |
| `/var/lib/docker` | workspace writable layers, volumes, images, caches | Back up for workspace recovery; images/caches alone are rebuildable |
| `/etc/scraps/scrapd.env` | daemon endpoint configuration and token | Back up; secret |
| `/opt/scraps/releases` | current and previous bundles | Rebuildable; retain previous |

## 3. Enroll and publish privately

Create a reusable=false, short-lived, appropriately tagged Tailscale auth key.
Pass it only in the SSH process environment and remove it when enrollment is
complete:

```bash
sudo env TAILSCALE_AUTHKEY='tskey-auth-...' \
  SCRAPS_TAILSCALE_HOSTNAME=scraps-worker scraps-worker tailscale-enroll
sudo scraps-worker tailscale-serve
tailscale status
tailscale serve status
```

Enable MagicDNS and HTTPS in the tailnet. A minimal grant should target only
the Pi/developer identity or device tag and the worker's HTTPS port; adapt the
identities to the tailnet rather than copying placeholders:

```json
{
  "grants": [
    {
      "src": ["group:scraps-operators"],
      "dst": ["tag:scraps-worker"],
      "ip": ["tcp:443"]
    }
  ],
  "tagOwners": {
    "tag:scraps-worker": ["group:scraps-admins"]
  }
}
```

Confirm the tailnet policy test passes before relying on the rule. Keep the
bearer token even though the ACL exists. Verify from the VM and another LAN
machine that ports 8484, 17670, and 2375/2376 are not reachable on its LAN or
Tailscale addresses. `ss -lntp` on the VM must show 8484 and 17670 on loopback
only. Never configure a router port-forward for this VM.

## 4. Configure the local client and Pi

Configure the developer machine directly over its existing SSH trust path:

```bash
scrap attach
scrap status
pi
# /scrap
```

`scrap attach` discovers the worker on the tailnet: every peer tagged
`tag:scraps-worker` (or named `scraps-worker*`) is probed on its Tailscale
Serve HTTPS endpoint with the unauthenticated `GET /healthz`. The peer Online
flag is only a sort hint; relayed or dormant workers can report offline while
remaining reachable, so the probe decides. With exactly one
live candidate it defaults the SSH user to the local user and reads the
worker's Tailscale MagicDNS name and bearer token through `sudo -n` over SSH
without printing the token, verifies an authenticated `/v1/info` names
`scrapd`, and only then atomically writes `~/.config/scraps/client.json` with
mode 0600. Pass an explicit target (`scrap attach operator@SCRAPS_VM`) to
skip discovery, and `make configure-remote-client REMOTE=...` for the same
flow without the CLI installed. Discovery never handles the token; it only
selects the host, so the SSH trust path remains the security boundary.

Both the `scrap` CLI and Pi extension load this profile automatically, so
ordinary `pi` followed by `/scrap` reaches the remote worker without sourcing
shell state. Explicit `SCRAP_DAEMON_URL` and `SCRAP_TOKEN` environment values
override the profile for CI and troubleshooting. The Pi session records only
the daemon URL and workspace association; it reloads the token from the local
profile or current environment on `/resume`.

## 5. Acceptance loop

Run this after install, reboot, upgrade, and restore. Record timings and NUC
CPU/RAM/disk (`time`, `free -h`, `df -h`, `docker system df`) in the maintenance
log.

Start with the automated loop, which exercises authenticated transport when a
token is configured, all seven Pi tool operations, cancellation cleanup,
stop/start persistence, deletion, and two concurrent workspaces. Increase the
iteration count for a soak test:

```bash
scripts/worker-acceptance 1
SCRAPS_ACCEPTANCE_CONCURRENCY=2 scripts/worker-acceptance 20
```

Then complete the UI-, network-, and host-specific checks below.

1. `scrap status`, then confirm the same call fails with `SCRAP_TOKEN=`.
2. Start Pi, run `/scrap`, and exercise `bash`, `read`, `write`, `edit`, `ls`,
   `find`, and `grep`; `pwd` must be `/workspace`.
3. Stop `scrapd`; project-facing Pi commands must fail closed, never touch the
   local checkout. Start it again.
4. Exercise `scrap ls`, `status`, `stop ID`, `start ID`, and `rm ID`.
5. Attach from a fresh Pi session with `/scrap-select ID`, then `/resume`.
6. Use `/scrap toss` and confirm local tools are restored.
7. Reboot the client and worker VM and repeat. Existing non-tossed workspace
   data must remain.
8. From an unauthorized tailnet identity and an ordinary LAN host, confirm the
   HTTPS service is denied/unreachable.

For GitHub, run `scrap auth github` against the tailnet HTTPS URL. Confirm the
manifest/setup callbacks return through that URL; clone, fetch, and push only
a selected private repository. Inspect service logs and the sandbox environment
to confirm the App key, bearer token, and installation credentials are absent.
Remove/re-authorize the App through GitHub settings when repository selection
changes.

## 6. Upgrade, rollback, backup, and recovery

Before an upgrade, run diagnostics and make a consistent backup. Generate an
age identity on an offline/admin machine, store its recipient on the VM, and
keep the identity outside the worker. The backup
briefly stops `scrapd`, OpenShell, and Docker so SQLite and volumes agree:

```bash
sudo scraps-worker diagnostics
sudo env SCRAPS_BACKUP_AGE_RECIPIENT='age1...' \
  scraps-worker enable-backups /mnt/backups
sudo systemctl start scraps-backup.service  # initial backup now
sudo scraps-worker upgrade /tmp/new-worker.tar.gz
sudo scraps-worker diagnostics
```

For normal source deployments from the developer checkout, prefer the same
convergent command used for initial installation:

```bash
make deploy-worker REMOTE=operator@SCRAPS_VM
```

It selects `scraps-worker upgrade` automatically when a worker is already
installed. Afterward, source the client environment and run
`make homelab-acceptance`. Rollback remains an explicit operator decision so a
client-network or credential failure is not mistaken for a bad worker release.

`enable-backups` writes the public age recipient to a root-only configuration
file and enables a persistent daily timer with a randomized two-hour window.
Inspect it with `systemctl list-timers scraps-backup.timer` and alert on a
failed `scraps-backup.service` unit.

The installer is idempotent. Upgrade verifies the bundle checksum, format,
architecture, and minimum free disk before changing the active release. It preserves `/etc/scraps` and
`/var/lib/scraps`, records the previous release, and atomically changes
`/opt/scraps/current`. Each bundle builds a version-tagged workspace image, so
the prior image remains available. If validation fails and no database migration has been
performed, use `sudo scraps-worker rollback`. Bundle format 1 currently has no
database migration; a future migration must document its irreversible boundary
and restore requirement before changing the format.

Also schedule encrypted Proxmox backups of the entire VM. Application backup
is the consistency boundary; take a Proxmox snapshot or backup while the three
services are stopped, or immediately after the application archive completes.
Test recovery quarterly by creating a replacement VM, installing the same
bundle, copying the archive and checksum to it, and running:

```bash
sudo env SCRAPS_BACKUP_AGE_IDENTITY=/mnt/keys/scraps-age-key.txt \
  scraps-worker restore /mnt/backups/scraps-TIMESTAMP.tar.gz.age
sudo scraps-worker diagnostics
```

Then repeat the acceptance loop and GitHub checks. The backup command refuses
to create plaintext because every archive contains `scrapd.env` and may contain
GitHub App state.

For maintenance, use `journalctl -u scrapd`, `scraps-worker diagnostics`, and
`docker system df`. Journald is capped at 512 MiB/30 days by the installer;
the daemon logs neither request authorization headers nor systemd environment
values. A five-minute `scraps-health.timer` checks Docker, OpenShell, scrapd,
disk usage, restart count, and encrypted-backup age. Its thresholds and an
optional JSON webhook are root-only settings in `/etc/scraps/health.env`;
verify delivery with `sudo scraps-worker healthcheck`. Keep an external check
against the tailnet HTTPS URL as well, since a powered-off VM cannot send its
own alert. Review before cleanup;
delete obsolete stopped workspaces through `scrap rm`, then prune only unused
Docker objects with `docker image prune`/`docker builder prune`. Keep current
and previous workspace images. Emergency shutdown is `sudo systemctl stop
scrapd` followed by `sudo systemctl stop docker`; rotate the bearer token with
`sudo scraps-worker rotate-token`, and re-enroll a compromised Tailscale node
after expiring/removing it in the admin console.

Trust boundary: the local Pi process and authenticated VM operator are trusted.
Workspace workloads are untrusted and contained by OpenShell/Docker inside the
outer VM. Resource defaults are 2 CPU and 4 GiB per workspace; validate the
expected concurrency on the NUC and reduce capacity before allowing aggregate
workspaces to exhaust VM memory.
