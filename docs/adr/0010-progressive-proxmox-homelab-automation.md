# ADR 0010: Progressive Proxmox homelab automation

- Status: Accepted
- Date: 2026-08-26

## Context

ADR 0009 makes an ordinary Linux VM the worker boundary and keeps Proxmox out
of the workspace API. The generic worker bundle, installer, diagnostics,
backup, upgrade, rollback, and acceptance loop now make a manually prepared
remote VM a viable target. The remaining operator experience is fragmented:
an administrator creates a VM, establishes SSH and Tailscale access, chooses
between an initial install and an upgrade, publishes the endpoint, transfers
client configuration, and runs validation as separate operations.

The desired homelab experience is eventually a resumable command such as
`scraps homelab install proxmox`. It should create a dedicated Ubuntu cloud-init
VM through the Proxmox API, enroll it in Tailscale, deploy the worker, configure
the local client, and prove the installation. Day-two commands should expose
status, logs, upgrades, backups, restore, and diagnostics without requiring the
operator to remember implementation paths.

Implementing that entire surface before deploying to a real Proxmox VM would
prematurely commit Scraps to assumptions about Proxmox permissions, storage,
bridges, cloud images, Tailscale policy, secret storage, and recovery. Those
choices should be informed by an actual installation and restore drill.

## Decision

We will build homelab automation in progressive layers while preserving the
ordinary-worker-VM boundary.

### Target experience

The eventual Proxmox driver will run from the trusted operator machine and:

1. use a least-privilege Proxmox API token to create or reconcile one dedicated
   cloud-init VM;
2. bootstrap a dedicated SSH identity and a short-lived, tagged Tailscale node
   identity without persisting enrollment secrets in VM metadata;
3. install a released, checksummed Scraps worker bundle;
4. publish only the authenticated `scrapd` endpoint through Tailscale Serve;
5. store client secrets in an OS credential store and keep reconstructible,
   non-secret deployment state locally;
6. validate health and the remote acceptance contract; and
7. provide transactional upgrade, rollback, backup, and replacement-VM restore
   workflows.

The driver is deployment tooling. Proxmox identifiers and credentials must not
enter workspace records, the workspace provider interface, or the `scrapd` API.
The VM remains independently operable through SSH and `scraps-worker` if the
local orchestration state is lost.

### Current implementation scope

The next supported boundary is a **pre-created dedicated Linux VM**. An
operator is responsible for VM creation, sizing, patching, SSH access, and
initial Tailscale membership. Scraps owns only the payload inside that VM.

`make deploy-worker REMOTE=user@host` is the convergent entry point for this
boundary. It builds the matching architecture locally, uploads one checksummed
bundle, and then:

- runs the bundle installer when no worker is installed;
- runs `scraps-worker upgrade` when an existing worker is detected;
- takes an encrypted pre-upgrade application backup when backups have already
  been configured; and
- runs remote diagnostics after activation.

Initial Tailscale Serve publication, tailnet grants, local bearer-token storage,
acceptance testing, and backup destination setup remain explicit runbook steps.
They involve site-specific security choices and must not be silently guessed by
the deployment command.

## Consequences

- A fresh install and routine source deployment use the same command, reducing
  operator branching and exercising the supported upgrade path continuously.
- The VM does not need a Git checkout, compiler, release registry, Proxmox
  credentials, or developer SSH agent.
- Configured installations receive a consistent encrypted backup before an
  upgrade; installations without a configured encryption recipient are not
  given an unsafe plaintext fallback.
- VM creation and Tailscale policy remain manual for now, so this is not yet a
  one-command Proxmox installation.
- The first real deployment can reveal the stable inputs required by a future
  Proxmox driver without coupling the application to the hypervisor.
- Automatic rollback remains deferred until the acceptance command can
  distinguish an application regression from an unreachable or misconfigured
  client. The previous release remains available through the existing explicit
  rollback command.

## Follow-up work

1. Deploy to a dedicated Proxmox VM and record provisioning, resource, network,
   upgrade, reboot, and failure-recovery observations.
2. Perform an encrypted restore into a replacement VM and verify workspaces and
   GitHub integration.
3. Define a reconstructible deployment profile and OS-backed credential storage.
4. Implement idempotent Proxmox cloud-image and cloud-init VM reconciliation
   behind a least-privilege API token.
5. Add secure Tailscale OAuth enrollment and policy verification.
6. Compose provisioning, deployment, client setup, acceptance, and safe rollback
   behind `scraps homelab` commands.

