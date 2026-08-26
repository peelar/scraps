# ADR 0005: Separate workspace, run, and client session identity

- Status: Proposed
- Date: 2025-05-09

## Context

ADR 0001 makes local Pi the canonical interactive experience: Pi's TUI, model
client, personalization, and session history remain on the developer machine,
while project-facing tools execute in a Scraps workspace. This is desirable for
interactive work, but a local agent loop cannot continue after the laptop
disconnects, wake in response to an external event, or be called by automation.

A workspace, an execution of an agent, and the UI observing that execution have
different lifetimes. Treating all three as one resource would either couple
workspace persistence to a laptop process or force every interactive Pi session
into the remote sandbox, reversing ADR 0001.

Scraps needs to enable both local interactive agents and eventually durable
headless agents without prematurely designing a universal agent protocol.

## Decision

Scraps distinguishes three identities:

- A **workspace** is the durable development computer: filesystem, Git state,
  project environment, runtime state, services, and provider lifecycle.
- A **run** is one bounded agent execution against a workspace. A run has an
  owner, input, state, timestamps, result, and artifact references.
- A **client session** is a local or future remote UI that attaches to a
  workspace or observes/controls a run. Its connection is not the lifetime of
  either resource.

Pi session identity remains distinct from Scraps identity as established by ADR
0001. A Pi session binding records the workspace and, when relevant, the run it
is observing; it does not own their lifecycle.

### Interactive mode

The canonical `scrap pi` flow continues to run Pi locally and route its
project-facing tools to the workspace. The local Pi process owns the agent loop,
so disconnecting it ends that interactive execution but does not delete the
workspace.

Interactive executions need not initially be persisted as full run resources.
The API must nevertheless avoid assumptions that the attached client owns the
workspace or provider runtime.

### Headless mode

Scraps will add a Pi-specific headless runner for automation and disconnected
work. A headless run:

- is created through an authenticated API or CLI request;
- executes the Pi agent loop on managed remote infrastructure against one
  workspace;
- continues when the creating client disconnects;
- persists state transitions, output references, and final result;
- may wake a sleeping workspace and permits it to sleep again after completion;
- can be inspected, cancelled, and followed by more than one authorized client.

The first runner is explicitly for Pi. Scraps will not define a generic agent or
ACP execution protocol until the Pi implementation demonstrates the required
contract.

Headless execution is disabled until model access can be supplied without
baking permanent provider credentials into images, snapshots, or workspace
state. A follow-up identity decision must define short-lived or otherwise
scoped model authorization.

### Run state and ownership

The initial run state model is:

```text
queued -> starting -> running -> succeeded
   |          |          |  \-> failed
   |          |          \----> cancelled
   |          \---------------> failed
   \--------------------------> cancelled
```

A run records at least:

- stable run ID and workspace ID;
- runner kind and version;
- creator and trigger provenance;
- requested task, creation time, start time, and finish time;
- state and structured terminal reason;
- references to logs, result, changes, and artifacts;
- effective environment build and workspace policy identifiers.

Runs do not silently push, merge, or deploy. Those are explicit capabilities
and policy decisions.

### Concurrency

A workspace permits one mutating agent run by default. An interactive client
may observe or take over according to an explicit control operation, but Scraps
does not allow two independent writers merely because both can attach.
Parallel attempts should normally fork into separate workspaces. A future
shared/multiplayer mode must define coordination semantics explicitly.

External events target either:

- a new run request, which may allocate a fresh workspace; or
- an existing durable workspace, which may wake and create a new run there.

Event transport, validation, and deduplication are follow-up decisions.

## Consequences

### Positive

- ADR 0001's local-native interactive experience remains intact.
- Workspaces can outlive laptops, terminals, Pi sessions, and individual tasks.
- Cron jobs, webhooks, and other automation gain a clear execution resource
  instead of impersonating an attached terminal.
- A future web or mobile client can observe the same run without owning it.
- Agent-specific implementation can evolve from Pi rather than a speculative
  universal abstraction.

### Negative

- Scraps eventually operates an agent runtime in addition to workspace
  infrastructure.
- Headless model authentication, durable output, cancellation, and recovery add
  security and operational responsibilities.
- Interactive and headless modes will not initially have identical persistence
  or control semantics.
- Default single-writer behavior limits in-place multi-agent collaboration.

## Alternatives considered

### Make every Pi session a remote process

This naturally survives a local terminal disconnect but gives up the local
personalization and credential boundary accepted in ADR 0001. It remains the
implementation model for headless runs, not canonical interactive use.

### Treat a workspace as one agent invocation

Rejected because useful workspaces must survive multiple prompts, sessions,
runs, sleeps, and resumptions. It also makes environment reuse and investigation
handoff unnecessarily destructive.

### Keep all agent loops local and expose only workspace APIs

Rejected as the complete model because no automation can continue while the
client computer is offline. It remains sufficient for the current interactive
milestones.

### Define a generic agent protocol now

Rejected. Pi is the concrete specification during exploration; generalization
will follow demonstrated requirements.

## Follow-up work

1. Define the run HTTP resource, event stream, cancellation, and retention.
2. Define scoped model authorization for headless Pi.
3. Implement a minimal Pi headless runner after workspace lifecycle and project
   environment contracts are stable.
4. Define explicit observe, control, and takeover behavior.
5. Define event provenance, validation, and deduplication before webhook support.
