# ADR 0001: Run Pi locally with workspace tools executed remotely

- Status: Accepted
- Date: 2026-08-24

## Context

Scraps provides persistent, isolated Linux development workspaces hosted on
self-managed infrastructure. A developer should be able to use Pi from their
normal terminal while the repository, commands, dependencies, services, and
agent-created changes remain inside a Scraps workspace.

There are two viable integration models:

1. Run Pi inside the remote workspace and connect a local terminal to it.
2. Run Pi's TUI, model client, and session locally, while a Pi extension replaces
   its filesystem, search, and shell tools with implementations backed by the
   remote workspace.

Daytona's `@daytona/pi`, Upstash's `@upstash/box-pi`, and the E2B Pi extension
demonstrate that the second model is supported by Pi's extension API and can
retain the normal local Pi experience. They replace Pi's built-in `bash`,
`read`, `write`, `edit`, `ls`, `find`, and `grep` tools with remote-backed
implementations.

This decision supersedes the narrower preference in `SPEC.md` to run the Pi
process itself inside the workspace. It does not change the product requirement
that the development environment and all project-affecting execution be remote.

## Decision

The canonical interactive Scraps integration will run Pi locally and execute
all project tools in a remote Scraps workspace.

```text
Developer machine                         Linux infrastructure
┌────────────────────────┐                ┌──────────────────────────┐
│ Pi TUI and model client│     HTTPS      │ scrapd                   │
│ Pi session history     │───────────────▶│ workspace lifecycle      │
│ provider credentials   │                │ exec/files/search/git    │
│ Scraps Pi extension    │                │ persistent workspace     │
└────────────────────────┘                └──────────────────────────┘
```

The primary direct invocation will be:

```bash
pi --scrap
pi --scrap --workspace <workspace>
```

`scrap pi [prompt]` will remain part of the public CLI. It is a convenience
launcher that resolves or creates a workspace and starts local Pi with the
Scraps extension and workspace identity. It is not a separate agent runtime.

The extension will:

- replace all seven project-facing Pi tools: `bash`, `read`, `write`, `edit`,
  `ls`, `find`, and `grep`;
- create, select, start, stop, and reconnect to workspaces through `scrapd`;
- stream command output and propagate exit status, timeouts, aborts, working
  directory, and environment additions;
- provide binary-safe file operations and preserve Pi's exact-match edit
  behavior;
- add remote workspace context to the system prompt;
- show an always-visible remote-workspace indicator in Pi's UI;
- expose Pi commands for workspace status, selection, lifecycle, previews, and
  explicit synchronization.

While remote mode is active, project-facing tools must fail closed. Loss of the
daemon or workspace must produce a visible error and must never cause silent
fallback to the developer machine.

`scrapd` will expose structured, authenticated workspace operations rather than
requiring the extension to assemble SSH commands. The initial API surface will
cover workspace lifecycle, streaming command execution, file operations,
search, Git operations, and service previews.

Pi session identity and Scraps workspace identity are distinct:

- a Pi session may reconnect to its previous workspace;
- a new Pi session may attach to an existing workspace;
- a workspace may outlive, stop independently from, or later be forked from a
  Pi session.

The initial implementation will keep Git state in the remote workspace. It will
not require automatic GitHub branch creation or use GitHub as filesystem
transport. Explicit diff, push, pull request, and local synchronization flows
will be added separately.

## Consequences

### Positive

- Developers retain Pi's normal local TUI, configuration, authentication, model
  providers, and session experience.
- LLM provider credentials do not need to enter workspace VMs.
- Scraps integrates through Pi's public extension surface instead of building a
  terminal transport or maintaining Pi inside every workspace image.
- Persistent workspaces are not forced into a one-workspace-per-session model.
- The Go daemon remains the authority for lifecycle, authentication, policy,
  persistence, and provider behavior; the TypeScript extension stays an adapter.

### Negative

- Scraps must faithfully implement and maintain remote equivalents of Pi's
  built-in tools.
- Every tool operation incurs network latency and must tolerate disconnects,
  cancellation, workspace sleep, and resume.
- The local filesystem shown by the shell is not the filesystem visible to the
  agent, which requires clear UI and system-prompt signaling.
- Features added to Pi outside the replaced tool surface may require additional
  interception or explicit remote integration.

### Security implications

- Pi and model-provider credentials remain on the developer machine.
- Project content and project-affecting execution remain in the Linux workspace.
- Workspace secrets must be supplied through Scraps-controlled brokering rather
  than copied wholesale from the local environment.
- Any local repository inspection used to select or initialize a workspace must
  be minimal and visible.
- A partially initialized or disconnected extension must not expose local tools
  under remote tool names.

## Alternatives considered

### Run Pi entirely inside each workspace

This produces a clean containment boundary and remains useful for headless jobs
or automation. It is not the canonical interactive mode because it requires a
remote terminal/session transport, places Pi configuration and model credentials
inside the workspace, complicates upgrades and session persistence, and weakens
the local-native experience.

### Expose only additional remote tools

Registering tools such as `scrap_exec` alongside Pi's local tools is rejected.
The model could accidentally inspect or modify the local checkout, and every
prompt would require it to choose the correct tool family.

### Synchronize a local checkout continuously

Continuous bidirectional filesystem synchronization is rejected for the initial
implementation. It creates conflict and consistency problems and weakens the
workspace as the authoritative development computer. Handoff will instead use
explicit Git, diff, artifact, or sync operations.

## Follow-up work

1. Define the remote tool compatibility contract and failure semantics.
2. Define the authenticated, streaming `scrapd` workspace API.
3. Implement workspace selection and Pi-session binding.
4. Replace the seven built-in Pi tools behind an explicit `--scrap` flag.
5. Add remote-state UI, system-prompt context, and disconnect tests.
6. Implement `scrap pi` as the convenience launcher.

## References

- [Pi extension documentation](https://pi.dev/docs/latest/extensions)
- [Daytona Pi extension architecture](https://www.daytona.io/docs/en/guides/pi/pi-extension/)
- [Upstash Box Pi extension](https://github.com/upstash/box-pi)
- [E2B Pi extension](https://github.com/edlsh/pi-extension-e2b)
