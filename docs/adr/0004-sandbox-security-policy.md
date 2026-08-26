# ADR 0004: Sandbox security policy

- Status: Accepted
- Date: 2026-08-26

## Context

A runtime type is not a security policy. Repository code can read inherited
secrets, exhaust the machine, expose services, download malicious dependencies,
or leave processes behind even when it runs in a container or VM. Scraps needs
defaults that are enforceable by providers and visible to operators.

This ADR defines the baseline for the directory, Docker, and Proxmox VM
providers. ADR 0003 still governs provider selection and accurately labels the
directory provider as unisolated.

## Threat model

Scraps treats repository contents, build scripts, package lifecycle hooks,
tests, browser content, and model-generated shell commands as untrusted.
Attackers may attempt to:

- read daemon, host, model-provider, SSH, cloud, or Git credentials;
- escape the workspace or access another workspace;
- consume CPU, memory, PIDs, disk, or network bandwidth indefinitely;
- bind externally reachable services or attack private-network services;
- persist processes after cancellation or workspace deletion;
- exploit stale or untrusted base images and dependencies.

The authenticated user and the local Pi process are trusted. Denial of service
against a single-user development laptop is lower impact than credential theft,
but deterministic limits remain required for sandbox providers.

### Effective boundaries

| Provider | Boundary | Security claim |
| --- | --- | --- |
| Directory | path validation only | Trusted development convenience. Commands are host processes and can deliberately access anything available to the daemon user. |
| Docker | unprivileged container sharing the host kernel | Local sandbox against ordinary repository code, not a hostile multi-tenant or kernel-exploit boundary. |
| Proxmox VM | dedicated VM kernel and disks | Production isolation target; Proxmox management and the workspace agent remain trusted infrastructure. |

No provider claim protects against vulnerabilities in its runtime, host kernel,
hypervisor, workspace agent, or `scrapd` itself.

## Decision

### Environment

Providers construct command environments from scratch. They must never append
the complete `scrapd` environment.

The baseline contains only provider-defined values for `HOME`, `PATH`, `SHELL`,
`TMPDIR`, locale/terminal settings when present, and
`SCRAP_WORKSPACE_ROOT=/workspace`. `HOME` and `TMPDIR` live inside provider-owned
workspace storage. Explicit per-command environment values in the authenticated
exec request may override the baseline; this is an intentional client action,
not inheritance.

The directory provider enforces this minimal environment to prevent accidental
secret leakage, while remaining explicitly bypassable by arbitrary host shell
commands because it has no process isolation.

### Credentials and secret brokering

No host credential directory, SSH agent, Docker socket, cloud environment,
model credential, or daemon token is mounted or inherited. Repository cloning
uses the existing unauthenticated public URL flow initially.

Future private GitHub access must use a broker that issues a short-lived,
workspace- and operation-scoped credential. The broker must record workspace,
credential kind, scope, issuance time, expiry, and result without logging the
secret. Credentials are delivered only to the operation that requested them,
never baked into an image, snapshot, workspace environment, or persistent Git
remote. Other services require their own explicit broker policy.

### Network

- **Directory:** host networking, unrestricted. This is reported as
  `host-unrestricted` and is another reason it is not a sandbox.
- **Docker default:** outbound internet enabled for repository and package
  access; inbound host publishing disabled; container-to-container and private
  host/LAN access denied where the runtime supports enforceable rules. DNS is
  provided by the managed sandbox network. No ports are published implicitly.
- **Proxmox VM default:** private per-workspace identity with outbound internet
  through controlled NAT; no unsolicited inbound connectivity and no access to
  management networks or neighboring workspaces.

Exceptions (published ports, private destinations, or disabled outbound access)
must be explicit configuration tied to a workspace, shown by diagnostics, and
audit logged. “Outbound enabled” is not an exfiltration defense; credential
non-disclosure is the primary control.

### Resources and lifecycle

Sandbox providers enforce explicit defaults. Initial Docker defaults are:

- 2 CPUs;
- 4 GiB memory, with swap disabled or capped at the same total;
- 512 PIDs;
- 20 GiB provider-managed workspace storage;
- the API's explicit command timeout, with a 15-minute client ceiling;
- stop grace period of 10 seconds followed by force termination.

Limits are configuration, not image-controlled settings, and effective values
must be reported. A command cancellation kills its complete process tree.
Stopping or deleting a workspace stops the runtime; deletion removes its
writable container state and managed volume. Startup reconciliation cleans up
or reports orphaned provider resources deterministically. Disk-limit support
must be verified for the selected Docker storage backend; DockerProvider must
refuse sandbox mode rather than claim an unenforced disk limit.

The directory provider supplies only process-group cancellation and reports
CPU, memory, PID, disk, and network controls as host-unlimited. Descendants that
escape the process group are possible; directory mode is not suitable for
untrusted code.

Proxmox limits follow the same categories but are enforced through VM CPU,
memory, disk, firewall, and agent process controls. Exact defaults may be
revised in the Proxmox-specific ADR.

### Images and updates

Docker and VM templates are built from reviewed source in this repository or a
documented trusted build pipeline. Runtime configuration pins immutable image
digests/template versions; mutable tags such as `latest` are rejected.
Production artifacts include provenance and an SBOM and are scanned before
promotion. Critical known vulnerabilities block promotion unless a documented,
time-bounded exception is approved. Base images are rebuilt at least monthly
and promptly for critical security fixes. Existing workspaces do not silently
change image underneath running state; status reports stale image/template
versions so they can be recreated or migrated.

### Visibility and audit

`GET /v1/info` and `scrap status` report effective provider policy summaries for
environment, network, resources, credential access, and process cleanup. A
future workspace-level policy endpoint will report concrete limit values and
exceptions once configurable Docker/VM policies exist.

Security-relevant lifecycle operations, network exceptions, limit overrides,
and credential grants must produce structured audit events. Secret values and
command environment values are never logged.

A provider must report what it actually enforces. Unsupported required controls
cause sandbox startup to fail closed; they must not be silently downgraded.

## Consequences

- Host secrets are absent from normal command environments even in directory
  mode, reducing accidental disclosure during development.
- Directory status remains candid about unrestricted host network and resources.
- DockerProvider has a concrete, testable baseline and must verify runtime
  capabilities before claiming container isolation.
- Package installation works by default, but outbound traffic can exfiltrate any
  secret deliberately granted to a workspace.
- Private repository support waits for scoped credential brokering rather than
  exposing developer credentials.
- Strong defaults add runtime capability checks, networking setup, accounting,
  reconciliation, image maintenance, and audit work.

## Required provider conformance tests

Every sandbox provider must prove that:

1. unrelated daemon environment variables and known credential variables are
   absent;
2. only explicit command environment values are added;
3. CPU, memory, PID, disk, timeout, cancellation, and stop/delete behavior match
   reported policy;
4. no host mount, runtime socket, implicit published port, management network,
   or neighboring workspace is reachable;
5. credential exceptions are scoped, expire, and are auditable;
6. image/template identity is immutable and surfaced;
7. unsupported mandatory controls fail startup rather than weakening policy.

Directory-provider tests cover environment minimization, virtual `HOME`, and
process-group cancellation, but do not turn those checks into an isolation
claim.
