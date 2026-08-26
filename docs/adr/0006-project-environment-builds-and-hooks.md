# ADR 0006: Build versioned project environments and expose agent-legible hooks

- Status: Proposed
- Date: 2025-05-09

## Context

A freshly provisioned VM or container is not necessarily ready for useful work.
Repository cloning, toolchain installation, dependency resolution, database
initialization, and service setup can dominate startup time and fail in ways
that force an agent to guess how the project works.

`SPEC.md` already describes base, project, and workspace layers and proposes a
`.scrap/setup` hook. GitHub issue #7 asks for the base/project boundary and setup
contract; issue #9 asks for reusable environments, caches, and startup
measurement. ADR 0003 requires provider snapshot support, while ADR 0004
forbids secrets in images and reusable snapshots.

The environment needs to be fast for humans, deterministic enough to debug,
and legible enough that an agent can act, observe, and recover rather than infer
hidden machine state.

## Decision

Scraps uses three environment layers:

```text
immutable base environment
        +
immutable project environment build
        +
mutable workspace
```

A provider may realize these layers with images, snapshots, linked clones,
volumes, caches, or equivalent mechanisms. The observable build identity and
lifecycle are provider-neutral.

### Base environment

The base environment is a pinned, versioned Scraps image or VM template. It
defines:

- Linux distribution and architecture;
- unprivileged user, `HOME`, shell, locale, certificates, and `/workspace`;
- tools guaranteed in every workspace;
- package installation policy;
- image provenance and security policy from ADR 0004.

The default base remains deliberately small. Large language runtimes, browser
stacks, databases, and similar capabilities belong in explicit profiles or the
project layer rather than accumulating silently in every workspace.

Profiles, if introduced, are named and immutable inputs to the build. A client
can request a profile but cannot depend on tools that status does not report.

### Project environment build

A project environment build executes the repository's `.scrap/setup` file, when
present, on top of a selected base environment. Setup runs with the repository
available at `/workspace` and produces a reusable immutable snapshot/template.

The build key includes at least:

- base image or template digest;
- architecture and provider compatibility class;
- selected profile and version;
- repository identity and commit;
- `.scrap/setup` content digest;
- explicitly declared setup inputs and Scraps contract version.

The repository commit is part of the default key because setup may compile or
otherwise depend on arbitrary source files. Projects may later opt into a
narrower declared-input key containing lock files, toolchain files,
schema/bootstrap inputs, or other paths. Narrowing the key is an explicit
correctness tradeoff, not an automatic guess by Scraps.

A successful build records its complete key, logs, duration, base identity, and
provider snapshot reference. Failed builds remain inspectable but are never
used to create workspaces. Rebuild and invalidation are explicit operations.

### Snapshot boundary

Reusable project environments must not contain:

- user or daemon credentials;
- model-provider credentials;
- short-lived access tokens;
- repository-specific write credentials;
- client environment variables not explicitly classified as non-secret build
  inputs;
- user work or state from an existing workspace.

Network access during setup follows the effective sandbox policy and is logged
at the policy level. A secret-dependent setup step must be redesigned to use a
post-clone runtime operation or an explicitly scoped non-snapshotted credential.

New workspaces prefer copy-on-write clones or an equivalent cheap derivation
from the project build. Providers that cannot snapshot may reuse managed caches
and rerun setup, but must report that weaker capability and must not claim a
warm project environment.

### Project hooks

The initial project contract is:

- `.scrap/setup` — create the reusable, secret-free project environment;
- `.scrap/resume` — restore ephemeral runtime state after workspace creation or
  wake;
- `.scrap/check` — report whether the workspace is ready and explain failures.

Hooks are executable files rooted in the repository and run from `/workspace`.
They must be safe to invoke more than once. Scraps supplies lifecycle context
through documented `SCRAP_*` variables and captures stdout, stderr, exit status,
duration, and hook version.

`.scrap/check` should emit a future versioned JSON readiness shape when invoked
with a Scraps-defined flag. Plain exit status and text are accepted initially.
The long-term requirement is structured diagnostics: an agent should be told
which dependency, service, credential, or migration is missing rather than only
that readiness failed.

Service declaration and artifact publication will receive separate contracts.
They may use data produced by these hooks but are not hidden side effects that
Scraps must guess from process tables or arbitrary files.

### Readiness and measurement

Workspace creation is not reported as ready until:

1. the provider runtime is available;
2. the project environment is materialized;
3. repository/workspace Git state is correct;
4. `.scrap/resume`, if present, succeeds;
5. `.scrap/check`, if present, succeeds.

Scraps measures and exposes these phases separately:

- allocation and provider start;
- project build lookup/materialization;
- clone or workspace Git preparation;
- resume;
- readiness check;
- time to first successful command.

The product target is time to useful execution, not raw VM boot time.

## Consequences

### Positive

- Expensive setup is paid once and reused safely.
- Base images remain smaller, auditable, and easier to update.
- Build provenance and phase timing make slow or broken startup diagnosable.
- Agents receive paved setup, resume, and readiness operations instead of
  guessing about machine state.
- OpenShell/runtime and worker-VM deployment drivers can optimize differently behind one observable model.

### Negative

- Cache key and invalidation mistakes can produce stale environments or needless
  rebuilds.
- Projects must maintain setup and readiness scripts.
- Snapshot support and secret scrubbing require provider conformance tests.
- Some setup steps cannot be cached because they require runtime identity or
  mutable external state.

## Alternatives considered

### Put all common tools in one large base image

This improves some cold starts but makes every workspace expensive, increases
patching and vulnerability surface, and still cannot encode project-specific
state.

### Run setup for every workspace

Simpler, but it places dependency latency and package-network reliability in the
critical path of every agent attempt. It fails the requirement that another
workspace feel trivial.

### Snapshot a configured user workspace

Rejected because it risks capturing credentials, mutable work, and surprising
runtime state. Project builds are produced in a dedicated build lifecycle with a
secret-free boundary.

### Require one specific development-container format

Rejected for now. Scraps needs a small concrete contract before adopting the
complexity and semantics of another ecosystem. Importers may be added later.

## Follow-up work

1. Resolve GitHub issue #7 with the concrete base tool list and setup contract.
2. Define project build records, default input discovery, and invalidation.
3. Add setup/build phase logs and timing to status and diagnostics.
4. Implement Docker project builds and prove snapshots contain no injected
   secrets.
5. Map project builds to portable worker storage; evaluate Proxmox-specific snapshot optimizations only in its future deployment ADR.
6. Specify the structured `.scrap/check` output after exercising the text/exit
   contract on real repositories.
