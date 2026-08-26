# Scraps Homelab / Proxmox TODO

Goal: run the Scraps worker stack in an ordinary Linux VM on Proxmox, connect
local Pi to it over the shared Tailscale network, and keep the deployment
private, authenticated, repeatable, and recoverable.

## Definition of done

From the developer machine:

```bash
export SCRAP_DAEMON_URL=https://scraps-worker.<tailnet>.ts.net
export SCRAP_TOKEN=<secret>
scrap status
pi
# In Pi: /scrap
```

This must create and attach a remote OpenShell workspace whose project-facing
Pi tools run in `/workspace`. Restarting Pi, the worker services, or the Proxmox
VM must preserve expected workspace state. The daemon must not be exposed to
the public internet or unauthenticated tailnet clients.

## Current state

- [x] Pi can use remote, workspace-backed `bash`, `read`, `write`, `edit`, `ls`,
  `find`, and `grep` tools.
- [x] The CLI and Pi extension accept `SCRAP_DAEMON_URL`.
- [x] The CLI and Pi extension send `SCRAP_TOKEN` as a bearer token.
- [x] `scrapd` supports `SCRAPD_LISTEN_ADDR` and `SCRAPD_TOKEN`.
- [x] The worker payload is architecturally portable: ordinary Linux VM,
  Docker, OpenShell, `scrapd`, SQLite, images, and workspace data.
- [x] Pi-extension type checking passes.
- [x] All 45 current Pi-extension tests pass.
- [x] The current Go checkout builds and passes `make check`.
- [ ] A generic/manual Linux worker deployment exists independently of Lima.
- [ ] A Proxmox worker installation is documented and exercised.
- [ ] Tailscale transport and remote authentication are configured end to end.
- [ ] Upgrade, rollback, backup, and recovery procedures exist.

### Baseline status

The original build blockers were resolved in the worker-VM-only runtime
migration. Provider-neutral helpers were retained for OpenShell, obsolete
direct-Docker and directory production providers were removed, and the full
Go and TypeScript validation suites pass.

## Phase 1 — Restore a green baseline

- [x] Move provider-neutral helpers still needed by OpenShell into appropriate
  provider files rather than restoring obsolete providers wholesale.
- [x] Remove or replace remaining references to the deleted direct-Docker and
  directory implementations.
- [x] Run `gofmt` over `cmd` and `internal`.
- [x] Make `go test ./...` pass.
- [x] Make `go vet ./...` pass.
- [x] Keep `pnpm check` passing.
- [x] Keep `pnpm test` passing.
- [x] Make the full `make check` pass.
- [ ] Smoke-test the existing Lima flow with `make up`, `scrap status`, Pi
  `/scrap`, workspace commands, `make down`, and resume.
- [x] Commit the worker-VM refactor as a coherent green baseline.

Acceptance criteria:

```bash
make check
make up
scrap status
```

All succeed, and a local Pi `/scrap` session can read, edit, and test files in a
remote workspace.

## Phase 2 — Extract a generic Linux worker deployment

Do not make Proxmox part of workspace APIs or the OpenShell provider. Proxmox
only hosts the ordinary worker VM.

- [ ] Separate the reusable guest provisioning logic from
  `scripts/worker-vm`, which is currently Lima-specific.
- [ ] Define a versioned worker bundle containing Linux `scrap` and `scrapd`
  binaries plus deployment metadata/configuration.
- [ ] Support both `linux/amd64` and `linux/arm64` bundles.
- [ ] Write an idempotent guest installer/provisioner for Ubuntu/Debian.
- [ ] Install or validate Docker Engine.
- [ ] Install the pinned OpenShell release.
- [ ] Configure the OpenShell Docker compute driver.
- [ ] Build or install the pinned Scraps workspace image.
- [ ] Install `scrapd` under a dedicated, documented service user or explicitly
  document why a normal user service is retained.
- [ ] Ensure user services start after reboot (`loginctl enable-linger`) if
  systemd user services remain in use.
- [ ] Configure durable locations for SQLite, OpenShell state, images, and
  workspace data.
- [ ] Make installation and upgrades idempotent.
- [ ] Add worker diagnostics: service status, recent logs, disk usage, Docker
  status, OpenShell status, and `scrapd` health.
- [ ] Avoid installing the Pi extension inside the worker VM; Pi remains local.
- [ ] Preserve the Lima deployment by making it invoke the generic bundle and
  guest provisioner.

Acceptance criteria: a manually prepared Linux VM can be turned into a working
Scraps worker without Lima and without editing source files in the VM.

## Phase 3 — Provision the Proxmox VM

Initial scope is a manually created VM, not a Proxmox API driver.

- [ ] Choose and document the guest OS version.
- [ ] Create a dedicated VM rather than an LXC container; the VM is the outer
  kernel/isolation boundary for untrusted workspace containers.
- [ ] Allocate initial resources (current Lima defaults are 4 CPUs, 8 GiB RAM,
  and 60 GiB disk; adjust from observed usage).
- [ ] Use a persistent virtual disk suitable for Docker layers and workspace
  data.
- [ ] Configure VM autostart and sensible Proxmox shutdown ordering.
- [ ] Configure time synchronization and DNS.
- [ ] Create the operator/service account and SSH access needed for deployment.
- [ ] Do not forward the developer SSH agent, home directory, checkout, or host
  Docker socket into the worker or its workspaces.
- [ ] Apply OS updates and a basic host firewall policy.
- [ ] Run the generic Linux worker installer.
- [ ] Reboot the VM and verify Docker, OpenShell, Tailscale, and `scrapd`
  recover automatically.
- [ ] Record CPU, memory, disk, and startup-time observations.

Deferred:

- [ ] Decide whether a Proxmox API deployment driver is worthwhile only after
  the generic remote-worker contract is stable.
- [ ] If implemented, keep Proxmox credentials and lifecycle operations out of
  workspace records and provider APIs.

## Phase 4 — Tailscale transport and authentication

Preferred initial topology: `scrapd` listens on VM loopback and Tailscale Serve
publishes it privately over tailnet HTTPS.

- [ ] Install Tailscale in the worker VM.
- [ ] Enroll it in the shared tailnet using an appropriately scoped and
  short-lived auth key.
- [ ] Assign a stable machine name such as `scraps-worker`.
- [ ] Confirm MagicDNS and tailnet HTTPS naming.
- [ ] Keep `scrapd` bound to `127.0.0.1:8484` where possible.
- [ ] Publish it with Tailscale Serve, for example:

  ```bash
  tailscale serve --bg http://127.0.0.1:8484
  ```

- [ ] Verify the resulting URL from the Pi machine, for example:

  ```text
  https://scraps-worker.<tailnet>.ts.net
  ```

- [ ] Generate a high-entropy bearer token.
- [ ] Store the server copy securely and inject it as `SCRAPD_TOKEN` through
  systemd without committing it to the repository.
- [ ] Export/store the client copy as `SCRAP_TOKEN` on the Pi machine without
  persisting it in Pi session metadata.
- [ ] Set `SCRAP_DAEMON_URL` on the Pi machine.
- [ ] Add narrow Tailscale ACL/grant rules so only intended users/devices can
  reach the worker service.
- [ ] Retain bearer-token authentication even with Tailscale ACLs.
- [ ] Confirm port 8484 is not reachable from the public internet or ordinary
  LAN unless explicitly intended.
- [ ] Confirm the OpenShell gateway and Docker API are not exposed over LAN or
  Tailscale.
- [ ] Document token rotation and tailnet device re-enrollment.

Fallback only if Tailscale Serve is unsuitable:

- [ ] Bind `scrapd` specifically to the VM's Tailscale address, not blindly to
  `0.0.0.0`, and enforce host firewall plus Tailscale ACLs.
- [ ] Decide how TLS is terminated; do not settle on unauthenticated plain HTTP
  over a broadly shared network.

Acceptance criteria:

```bash
SCRAP_DAEMON_URL=https://scraps-worker.<tailnet>.ts.net \
SCRAP_TOKEN=<secret> \
scrap status
```

succeeds from an authorized Pi machine, fails without the token, and is not
reachable from unauthorized devices.

## Phase 5 — End-to-end Pi workflow

- [ ] Install the local `scrap` CLI and Pi extension on the developer machine.
- [ ] Provide a durable local environment-loading method for
  `SCRAP_DAEMON_URL` and `SCRAP_TOKEN`.
- [ ] Start ordinary local Pi and run `/scrap`.
- [ ] Create a workspace and verify all seven remote tools.
- [ ] Verify shell working directory and file paths use `/workspace`.
- [ ] Verify local project files are not silently accessed while attached.
- [ ] Verify a daemon outage fails closed instead of running project commands
  locally.
- [ ] Test `scrap ls`, `scrap status`, `scrap stop`, `scrap start`, and
  `scrap rm` against the remote daemon.
- [ ] Test `/scrap-select ID` from a second/new Pi session.
- [ ] Test `/resume` reconnects to the persisted workspace association while
  obtaining the token from the current environment.
- [ ] Test `/scrap toss` deletes the workspace and restores local tools.
- [ ] Reboot the Pi machine and repeat connection tests.
- [ ] Reboot the Proxmox worker and verify workspace recovery.
- [ ] Measure fresh workspace and warm workspace startup latency.

## Phase 6 — GitHub authentication over the remote URL

The browser callbacks must be reachable from the developer browser. Tailnet
HTTPS should be tested explicitly rather than assuming the localhost-oriented
flow works unchanged.

- [ ] Run `scrap auth github` using the Tailscale HTTPS daemon URL.
- [ ] Verify GitHub accepts the generated manifest, redirect, and setup URLs.
- [ ] Verify browser callbacks reach `scrapd` through Tailscale Serve.
- [ ] Verify callback state is random, short-lived, and rejected after expiry.
- [ ] Verify the GitHub App private key remains only in the worker control
  plane.
- [ ] Verify installation credentials are short-lived and brokered by
  OpenShell.
- [ ] Verify credentials and bearer tokens do not appear in sandbox
  environment variables, command arguments, logs, Pi session metadata, or Git.
- [ ] Clone a selected private repository.
- [ ] Fetch and push through the configured provider.
- [ ] Confirm unselected repositories and prohibited GitHub API operations are
  blocked.
- [ ] Document reauthorization, repository selection changes, and App removal.

## Phase 7 — Persistence, backup, recovery, and upgrades

- [ ] Inventory every durable path: `scrapd` SQLite/database files, GitHub App
  state/key, OpenShell state, workspace volumes, image/cache data, and service
  configuration.
- [ ] Decide which data must be backed up versus cheaply rebuilt.
- [ ] Document a consistent backup procedure; stop or quiesce services where
  required.
- [ ] Integrate with Proxmox backup/snapshot tooling while accounting for
  database and container-volume consistency.
- [ ] Encrypt backups that contain GitHub App keys or other secrets.
- [ ] Perform and document a restore into a replacement VM.
- [ ] Verify restored workspaces and GitHub integration behave as expected.
- [ ] Define bundle version compatibility with the database and workspace
  image.
- [ ] Implement an upgrade procedure with preflight checks.
- [ ] Keep the previous bundle/image available for rollback.
- [ ] Define rollback limitations for database migrations.
- [ ] Test upgrades and rollback on disposable data before relying on them.
- [ ] Add disk-space monitoring and a cleanup procedure for old images,
  stopped workspaces, and caches.

## Phase 8 — Operational hardening

- [ ] Define expected service health checks and restart policies.
- [ ] Add log rotation and ensure secrets are redacted.
- [ ] Add basic alerts for worker offline, low disk, repeated service failure,
  and backup failure.
- [ ] Pin and deliberately update OpenShell and base-image versions.
- [ ] Review Docker and OpenShell security configuration after real workloads.
- [ ] Validate resource limits prevent one workspace from exhausting the VM.
- [ ] Validate workspace process cleanup on stop/delete.
- [ ] Test concurrent workspaces under expected homelab capacity.
- [ ] Document emergency shutdown and credential/token rotation.
- [ ] Document known trust boundaries: local Pi and authenticated operator are
  trusted; workspace workloads are contained by OpenShell containers and the
  outer worker VM.

## Later product milestones

These are not required for the first Proxmox/Tailscale proof but remain part of
the broader Scraps experience:

- [ ] Reusable project environments and `.scrap/setup` caching.
- [ ] Startup optimization toward the 5–10 second target.
- [ ] Reliable idle sleep and transparent resume.
- [ ] Browser/Playwright support and service preview URLs (`scrap open`).
- [ ] Diff/PR handoff and optional sync back to a local checkout.
- [ ] Multiple workers or scheduling only after the single-worker flow is
  dependable.

## Recommended implementation order

1. Restore `make check` and smoke-test Lima.
2. Extract the generic Linux worker bundle/provisioner.
3. Create and provision one manual Proxmox Ubuntu VM.
4. Add Tailscale Serve, ACLs, and bearer-token configuration.
5. Complete the remote Pi acceptance test.
6. Validate remote GitHub App callbacks and private repository access.
7. Add repeatable upgrades, backup/restore, diagnostics, and hardening.
8. Consider Proxmox API automation only after the above is stable.
