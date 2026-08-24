# Scraps

**Self-hosted agent workspaces on the hardware you already own.**

## Premise

Cloud coding agents such as Cursor Cloud Agents, Amp Orbs, and similar systems feel powerful because compute becomes abundant: starting another isolated computer is easier than creating and managing another local worktree.

**Scraps aims to reproduce that feeling on self-hosted infrastructure.**

A developer should be able to run:

```bash
scrap pi "investigate this flaky checkout test"
```

and, within seconds, have Pi working inside a fresh isolated Linux computer on their own infrastructure, with the repository, dependencies, GitHub access, browser, and development environment already prepared.

The user should think **“spawn another agent”**, not **“provision another VM.”**

## Core UX

The CLI is the primary product surface.

```bash
scrap setup                 # configure self-hosted infrastructure
scrap auth github           # authenticate once

scrap pi                    # interactive Pi in a fresh workspace
scrap pi "fix issue #123"   # create workspace + start task

scrap ls
scrap attach <workspace>
scrap ssh <workspace>
scrap open <workspace>      # open project preview
scrap diff <workspace>
scrap sync <workspace>
scrap rm <workspace>
```

The interface should remain largely stable even as the implementation evolves.

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
scrapd                        Go
├── workspace lifecycle
├── project environments
├── GitHub credentials
├── persistence              SQLite
└── compute provider
          │
          ▼
Proxmox
└── isolated workspace VMs
```

Initial infrastructure target: **one Proxmox installation**.

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

Start with a static/fake provider if useful, then implement Proxmox.

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

**M4 — Proxmox isolation**
Replace shared directories with one VM per workspace using templates/clones.

**M5 — Fast startup**
Snapshots, linked clones/COW, warm caches and—only if necessary—warm machines. Optimize until spawning another agent feels cheap.

**M6 — Sleep/resume**
Idle workspaces consume minimal resources and wake transparently.

**M7 — Browser + services**
Chromium/Playwright in every workspace; detect/expose dev-server ports; `scrap open`.

**M8 — Handoff**
Easy diff, PR creation, and optional sync back to a local checkout.

Only after these work well: additional agents, ACP, multiple compute nodes, scheduling, shared organizational compute, or an agentic IDE.

## Explicit Non-Goals for Early Versions

No Kubernetes, federation, billing, public compute marketplace, multi-user control plane, custom hypervisor, custom sandbox runtime, full IDE, elaborate web UI, generic scheduler, or cloud-provider support.

### First Exploration Question

Build the smallest prototype that proves:

> **From a local terminal, `scrap pi` starts a normal-feeling Pi session whose entire development computer lives on a self-hosted machine—and using it feels easier than managing another local worktree.**

Everything else should be evaluated against whether it moves Scraps toward that experience.
