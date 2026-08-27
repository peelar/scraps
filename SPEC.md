# Scraps

**Self-hosted agent workspaces on the hardware you already own.**

## Premise

Cloud coding agents such as Cursor Cloud Agents, Amp Orbs, and similar systems feel powerful because compute becomes abundant: starting another isolated computer is easier than creating and managing another local worktree.

**Scraps aims to reproduce that feeling on self-hosted infrastructure.**

A developer should be able to open Pi and run:

```text
/scrap investigate this flaky checkout test
```

and, within seconds, have Pi working inside a fresh isolated Linux computer on their own infrastructure, with the repository, dependencies, GitHub access, browser, and development environment already prepared.

The user should think **“spawn another agent”**, not **“provision another VM.”**

## Core UX

Pi is the primary interactive product surface:

```text
/scrap                     # create and activate a fresh workspace
/scrap fix issue #123      # create a named/task-oriented workspace
/scrap-select <workspace>  # attach this Pi session to an existing workspace
```

The CLI manages workspaces through the daemon; onboarding records worker VM
CPU, memory, and disk sizing before `make up` creates it. `make up` and `make
down` manage the local worker VM:

```bash
make configure
make up
scrap status
scrap ls
scrap start <workspace>
scrap stop <workspace>
scrap rm <workspace>
make down
```

The interfaces should remain largely stable even as the implementation evolves.

## Core Model

A **workspace is an agent-owned computer**, not a directory or worktree.

Each workspace eventually contains:

```text
isolated machine
repository + branch
agent session
terminal/processes
project dependencies
browser
development services
credentials
artifacts/diffs
lifecycle state
```

Workspaces may sleep when unused and resume automatically.

## Environment Model

Fresh workspace **must not mean fresh setup**.

Use three layers:

```text
Base environment
Ubuntu + git + gh + common runtimes + Chromium/Playwright
        ↓
Project environment
dependencies + services + setup, built once and cached
        ↓
Workspace
fresh Git state + agent changes + runtime state
```

A repository may initially define setup through:

```text
.scrap/setup
```

The expensive project setup should run once, producing a reusable snapshot/template. New workspaces should use copy-on-write clones or equivalent mechanisms.

**Target eventually: useful agent execution within ~5–10 seconds.**

## Initial Architecture

Optimize for simplicity, not generality.

```text
Developer machine
├── scrap CLI                 Go
└── Pi integration            TypeScript
          │
          │ private network / Tailscale
          ▼
ordinary Linux worker VM
├── scrapd                    Go
├── OpenShell gateway
├── workspace lifecycle
├── project environments
├── GitHub credentials
├── persistence              SQLite
└── OpenShell container pool
```

Initial infrastructure target: **one ordinary Linux worker VM** containing many
cheap workspace containers. Lima is the first local VM driver. The same worker
may later run as a VM on Proxmox or another remote hypervisor; users do not need
Proxmox to run Scraps.

Use existing virtualization and security primitives. Scraps must **not** implement its own VM/container isolation.

The compute abstraction should nevertheless be replaceable later:

```go
type Provider interface {
    Create(...)
    Start(...)
    Stop(...)
    Delete(...)
    Snapshot(...)
    Capacity(...)
}
```

Start with local VM deployment and keep the worker-host mechanism replaceable.
A Proxmox deployment driver may be implemented later without changing workspace
or client contracts.

## Pi First

Pi is the first and only supported agent during exploration.

Prefer the model:

```text
local terminal/UI
        ↓
Pi/session running in workspace
        ↓
filesystem + shell + MCP + browser all remote
```

rather than individually proxying every local Pi tool indefinitely.

Pi should serve as the concrete specification from which a more generic agent interface can later emerge. Do **not** design a universal agent protocol prematurely. ACP may become relevant later.

## Schedule Clock

Scrapd provides a narrow, durable schedule and occurrence primitive. It owns
time, missed firings, concurrency policy, occurrence history, and renewable
consumer leases. Schedule payloads are opaque JSON.

Scraps does **not** interpret a scheduled payload or turn it into a Pi task. A
separate software-factory harness may later claim occurrences and request
Scraps compute. This preserves the boundary between compute, time, and workflow
semantics while avoiding a generic scheduler.

## What Makes Scraps Good

Prioritize these properties above feature count:

1. **Starting another workspace feels trivial.**
2. **Project setup is remembered and reused.**
3. **GitHub authentication happens once.**
4. **Workspaces are isolated from each other and the host.**
5. **The agent can run the real application, tests, Docker, browsers, and services.**
6. **Previewing and reviewing results requires almost no ceremony.**
7. **Infrastructure details remain invisible during normal use.**

The benchmark is the feeling of Amp Orbs / commercial cloud agents, not merely successful remote execution.

## Exploration Milestones

**M0 — CLI contract**
Prototype commands and workspace model.

**M1 — Remote Pi**
Run Pi against one manually prepared remote Linux machine. Validate that the interaction feels good.

**M2 — Project environments**
`.scrap/setup`, dependency caching, reusable project state.

**M3 — Identity**
One-time GitHub authentication; `git` and `gh` work automatically inside workspaces.

**M4 — Worker VM isolation**
Run the shared OpenShell/container pool inside an ordinary protective Linux VM;
start with Lima locally and keep Proxmox as a future remote target.

**M5 — Fast startup**
Snapshots, linked clones/COW, warm caches and—only if necessary—warm machines. Optimize until spawning another agent feels cheap.

**M6 — Sleep/resume**
Idle workspaces consume minimal resources and wake transparently.

**M7 — Browser + services**
Chromium/Playwright in every workspace; detect/expose dev-server ports; `scrap open`.

**M8 — Handoff**
Easy diff, PR creation, and optional sync back to a local checkout.

Only after these work well: additional agents, ACP, multiple compute nodes, schedule-driven agent execution, shared organizational compute, or an agentic IDE.

## Explicit Non-Goals for Early Versions

No Kubernetes, federation, billing, public compute marketplace, multi-user control plane, custom hypervisor, custom sandbox runtime, full IDE, elaborate web UI, generic scheduler, or cloud-provider support.

### First Exploration Question

Build the smallest prototype that proves:

> **In ordinary local Pi, `/scrap` activates a development computer on self-hosted infrastructure—and using it feels easier than managing another local worktree.**

Everything else should be evaluated against whether it moves Scraps toward that experience.
