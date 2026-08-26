# ADR 0008: OpenShell as the default sandbox control plane

- Status: Accepted
- Date: 2026-08-26

## Context

The direct Docker provider proved Scraps' provider and `/workspace` contracts,
but making `scrapd` itself responsible for sandbox networking, credentials,
runtime hardening, and pooling would create an infrastructure product. Scraps
is primarily the Pi integration and friendly workspace application.

This is a personal, single-user deployment rather than a hostile multi-tenant
cloud. The intended host-protection boundary is one ordinary Linux worker VM
around the entire agent resource pool; inexpensive containers may share that
VM. ADR 0009 makes the VM host replaceable and treats Proxmox as one future
deployment target.

## Decision

OpenShell is the default provider. `make up` pins, installs, and starts the
supported OpenShell release, builds the pinned Scraps image derived from
OpenShell's maintained agent base, builds Scraps, and starts `scrapd`.

`scrapd` remains the stable application control plane. It owns Pi integration,
workspace/session identity, repository intent, provider-neutral paths, and the
HTTP API. OpenShell owns sandbox lifecycle and supplies its policy, credential,
resource, and compute-driver mechanisms. Scraps invokes the versioned OpenShell
CLI for now; a native SDK adapter may replace it without changing clients.

The default OpenShell compute driver is the container driver inside one
dedicated Linux worker VM. Lima supplies the initial local VM; a remote
hypervisor such as Proxmox may supply it later. We do not require a VM per
workspace.

The direct Docker provider remains an explicit development/fallback backend.
Directory mode remains non-isolated trusted development only.

## User-visible success criteria

OpenShell is retained as the default only if:

1. users can immediately observe useful policy or credential isolation beyond
   the direct Docker prototype; and
2. installation, startup, workspace latency, upgrades, recovery, and removal
   remain suitable for one personally managed server.

Issue #10 owns the live policy demonstration, conformance suite, benchmarks,
and operating runbook.

## Consequences

- Scraps relies on an upstream security and runtime project rather than
  inventing those mechanisms.
- The workspace image is larger because it begins with OpenShell's supported
  agent environment instead of a minimal Debian image.
- `make up` requires network access on first install and may invoke the native
  package manager through OpenShell's pinned installer.
- OpenShell release and base-image upgrades are deliberate compatibility
  changes and remain pinned in the repository.
- A gateway failure fails closed; `scrapd` does not fall back to direct Docker
  or host execution.
- The worker VM protects the developer or homelab host from compromise of the
  shared agent-container pool, while containers provide economical workspace
  lifecycle and separation inside that VM.
