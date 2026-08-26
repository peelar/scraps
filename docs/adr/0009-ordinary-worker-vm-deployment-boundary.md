# ADR 0009: Ordinary worker VM as the deployment boundary

- Status: Accepted
- Date: 2026-08-26

## Context

ADR 0008 selected OpenShell with a shared container pool and described the
outer boundary as a Proxmox VM. That wording accidentally made one hypervisor
and its management product appear mandatory. A user running Scraps on a laptop
should not need to install or operate Proxmox, and Scraps does not need
Proxmox-specific behavior to obtain a separate kernel boundary around untrusted
workspace workloads.

The outer worker boundary and the inner workspace provider solve different
problems. The worker VM protects the developer or homelab host from compromise
of the agent compute pool. OpenShell provides fast workspace lifecycle and
policy inside that worker. Making every workspace a VM would be much more
expensive and is not required for the current single-user threat model.

## Decision

The deployment unit is one ordinary Linux **worker VM** containing:

- the container runtime;
- the OpenShell gateway and CLI;
- `scrapd`, SQLite state, images, and workspace data; and
- many OpenShell-managed workspace containers.

Pi, its configuration and model credentials, and the `scrap` client remain on
the developer machine. They reach `scrapd` through an authenticated private
endpoint or a host-loopback tunnel.

A worker-VM driver is deployment machinery, not a workspace `Provider`.
Workspace APIs and records must not expose Lima, Proxmox, or another
hypervisor. The same Linux/OpenShell/Scraps payload must be portable across VM
hosts.

### Initial local driver

Lima 2.0+ is the first implemented worker-VM driver for macOS and Linux. The
checked-in template creates a headless Ubuntu VM with Docker Engine, 4 CPUs,
8 GiB memory, and 60 GiB disk. It forwards guest-loopback port 8484 to host
loopback so local Pi retains the stable daemon URL.

The VM receives a purpose-built deployment bundle. It does not mount the host
home, checkout, SSH agent, or Docker socket. `make up`, `make down`, `make
vm-shell`, and `make vm-delete` own its lifecycle. A user systemd service in
the VM owns `scrapd`; local clients only communicate with its forwarded API.

### Future drivers

A Proxmox-hosted Linux VM is a future remote target, not a prerequisite or the
architecture itself. Other viable drivers include libvirt/KVM, Hyper-V, VMware,
or a manually prepared Linux VM. Driver-specific creation, networking,
credentials, and upgrades belong behind deployment tooling and focused ADRs.

The remote transport must eventually use authentication and a private network
such as Tailscale. The Lima implementation binds only to host loopback and does
not by itself define the remote deployment security model.

### No host mode

Direct host, directory, and direct-Docker modes are unsupported. The worker VM
and OpenShell path is mandatory so normal and development workflows exercise
the same protection boundary.

## Consequences

- Users can run Scraps locally without installing or learning Proxmox.
- The expensive VM is amortized across many inexpensive workspace containers.
- A worker compromise is separated from the developer/homelab host by a VM
  kernel boundary, subject to the selected hypervisor and its integration
  surface.
- Local startup includes VM provisioning and consumes a fixed resource budget.
- Lima is a concrete initial implementation but is replaceable rather than part
  of the client or workspace contract.
- Proxmox work is narrowed to deploying the same worker payload remotely,
  including authentication, networking, persistence, backup, and recovery.

## Follow-up work

1. Exercise the Lima flow in CI or a documented smoke test on both supported
   host architectures.
2. Add configurable worker CPU, memory, disk, and daemon port without weakening
   defaults.
3. Define a versioned worker bundle and upgrade/rollback behavior.
4. Specify authenticated remote-worker enrollment and transport.
5. Implement a Proxmox deployment driver only after the generic remote-worker
   contract is stable.
