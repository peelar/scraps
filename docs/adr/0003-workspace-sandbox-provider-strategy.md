# ADR 0003: Workspace sandbox provider strategy

- Status: Accepted
- Date: 2026-08-24

> ADRs 0008 and 0009 supersede the default-provider and production-topology
> portions of this decision: OpenShell is the default control plane, with a
> shared container pool inside one ordinary Linux worker VM. Proxmox is one
> future VM host, not a requirement. ADR 0009 was subsequently amended to
> remove the directory and direct-Docker implementations; those sections below
> remain only as historical context for the provider abstraction.

## Context

The M1 implementation deliberately uses a directory-backed provider. Each
workspace is a literal directory under the `scrapd` data directory and command
execution is a normal host process whose working directory is that directory.
Path containment in file APIs prevents accidental traversal, but it is not a
security boundary: shell commands run as the `scrapd` user and share the host's
kernel, processes, network, resources, and environment.

Scraps ultimately promises that a workspace is an agent-owned development
computer. It must safely run untrusted repository code, package install hooks,
tests, browsers, development servers, and Docker workloads without granting
those processes access to the developer machine or neighboring workspaces.

The original target in `SPEC.md` was one self-hosted Proxmox installation with
a VM per workspace. ADRs 0008 and 0009 replace that topology with one portable
worker VM containing a shared OpenShell container pool.

Pi itself remains local per ADR 0001. Its TUI, provider credentials, sessions,
skills, and prompts are not part of the workspace sandbox. Project-facing tools
cross the authenticated `scrapd` API and execute inside the selected sandbox.

## Decision

### Provider progression

Scraps will implement three providers in this order:

1. **Directory provider** — retained for API tests and trusted local
   development only. It must be labeled explicitly as having no isolation.
2. **Docker provider** — implemented as the local Linux sandbox and provider
   contract test. It validates provider boundaries, images, persistent workspace
   storage, lifecycle, resource limits, and exec semantics with short feedback
   loops. Docker Desktop, OrbStack, and a native Linux Docker Engine are
   acceptable runtimes behind the same provider.
3. **OpenShell provider inside a worker VM** — selected by ADRs 0008 and 0009.
   A shared ordinary Linux VM provides the host-protection boundary while
   OpenShell containers provide economical per-workspace lifecycle and policy.
   Lima is the first worker-VM driver; Proxmox may host the same worker later.

### Provider boundary

Workspace implementation details will move behind an internal interface whose
capabilities, rather than transport mechanics, drive the API. The initial shape
is expected to include:

```go
type Provider interface {
    Create(context.Context, CreateOptions) (WorkspaceHandle, error)
    Start(context.Context, WorkspaceHandle) error
    Stop(context.Context, WorkspaceHandle) error
    Delete(context.Context, WorkspaceHandle) error
    Exec(context.Context, WorkspaceHandle, ExecRequest) (ExecStream, error)
    Files(context.Context, WorkspaceHandle) FileSystem
    Snapshot(context.Context, WorkspaceHandle, SnapshotOptions) (Snapshot, error)
    Capacity(context.Context) (Capacity, error)
}
```

The exact Go types will be determined during implementation. The important
boundary is that HTTP handlers and SQLite records do not assume a host
directory or invoke host processes directly.

### Docker provider baseline

Each Docker workspace has:

- one container created from a pinned Scraps development image;
- a persistent named volume mounted at `/workspace`;
- an unprivileged default user;
- explicit CPU, memory, PID, and disk policies;
- no host Docker socket or host filesystem mounts;
- lifecycle mapped to create/start/stop/delete;
- command execution via the container runtime, with cancellation and exit
  semantics matching ADR 0002;
- an explicit, minimal environment rather than wholesale daemon inheritance;
- network policy documented and configurable, initially outbound-enabled for
  package installation and repository access.

The Docker provider is a development bridge, not the final multi-tenant
security claim. Its threat model and runtime dependencies must be visible in
status and workspace metadata.

### Worker VM baseline

The worker VM is outside the workspace-provider boundary: it contains `scrapd`,
OpenShell, the runtime, and many workspace containers. Workspace records do not
name its hypervisor. Local Lima and a future remote Proxmox deployment carry the
same worker payload; transport, readiness, persistence, and reconciliation are
deployment-driver concerns. See ADR 0009.

### Provider-neutral paths

The original M1 absolute-host-path contract was replaced before the Docker
provider: API paths are workspace-relative, Pi sees `/workspace`, provider
layout remains server-side, and workspace resources declare the versioned
`workspace-relative-v1` contract. See ADR 0002 for migration behavior.

### Personalization and sandbox boundary

Per ADR 0001, containerizing the workspace must not erase the user's agent
personalization. Global Pi skills and prompts remain available in ordinary
local Pi; model credentials remain local as well. They can instruct the agent,
but all project-facing tool calls must execute in the selected sandbox.
Executable global Pi extensions are a separate host trust boundary and require
an explicit policy because they may register local tools outside Scraps'
replacements.

## Consequences

### Positive

- Docker provides a real Linux workspace environment without coupling the API
  to a particular hypervisor.
- An ordinary worker VM gives the whole agent pool a kernel boundary from the
  developer or homelab host without paying for one VM per workspace.
- The local Pi experience retains user skills and credentials while repository
  code runs behind a sandbox boundary.
- Provider-neutral paths and operations avoid leaking host/container layout to
  Pi and future clients.

### Negative

- Workspace-container and outer worker-VM lifecycle have different semantics;
  deployment state must not leak into the provider contract.
- The provider-neutral path contract requires daemon and extension versions
  that explicitly agree; mixed transitional/current versions fail closed.
- Filesystem APIs may need a workspace agent, archive protocol, or runtime exec
  implementation; direct host `os.*` calls no longer work for every provider.
- VM template creation, readiness, networking, snapshots, and reconciliation
  add significant operational complexity.

### Security implications

- The directory provider must never be presented as safe for untrusted code.
- Docker containers must not receive privileged mode, host filesystem mounts,
  or the Docker socket by default.
- Workspace environments and credentials must use explicit allowlists and
  brokering; inheriting the full `scrapd` environment is forbidden for sandbox
  providers.
- Resource and network policies are part of sandbox correctness, not optional
  production polish.
- Keeping Pi skills local does not weaken process isolation, but executable Pi
  extensions remain trusted host code and need separate controls.

## Alternatives considered

### Require Proxmox or use one VM per workspace

Rejected by ADR 0009. Requiring a management platform makes local use needlessly
heavy, and one VM per workspace spends resources and startup time without a
corresponding need in the current single-user threat model. Proxmox remains a
possible host for the shared worker VM.

### Run Pi inside every sandbox

Rejected as the canonical interactive mode by ADR 0001. It loses the user's
local skills, sessions, configuration, and provider setup and requires a remote
terminal transport. It remains possible for headless automation later.

### Bind-mount host repositories into containers

Rejected as the default because it weakens the workspace as the authoritative
computer and risks exposing host paths. Workspace state belongs in provider-
managed persistent storage and moves through explicit clone, Git, artifact, or
sync flows.

## Follow-up work

1. Refactor directory behavior behind the provider interface without changing
   M1 externally observable behavior.
2. Version the API path contract and migrate the Pi extension to relative paths
   with `/workspace` as its stable visible root.
3. Maintain the implemented Docker provider, pinned development image, volume
   lifecycle, resource limits, cancellation, and provider conformance tests.
4. Define sandbox environment, network, secret-brokering, and threat-model
   policies.
5. Define the Pi resource policy that preserves skills/prompts while governing
   executable global extensions and additional tools.
6. Stabilize the generic worker bundle, remote authentication, networking,
   readiness, persistence, reconciliation, and failure-recovery contract.
7. Implement a Proxmox deployment driver after that generic contract is stable.
