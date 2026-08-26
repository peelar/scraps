# ADR 0007: Reclaim idle resources aggressively and operate within explicit budgets

- Status: Proposed
- Date: 2025-05-09

## Context

Scraps is intended to run on spare, small, or irregular self-hosted hardware.
It cannot reproduce a commercial cloud's physical capacity, but it can reproduce
the experience that starting one more workspace does not require the user to
manually manage machines, ports, memory, or cleanup.

`SPEC.md` says workspaces may sleep and resume automatically. ADR 0004 defines
per-workspace sandbox limits. GitHub issue #9 asks for pools, leases, capacity,
admission control, cleanup, and disk-pressure behavior. A conventional warm-pool
design that leaves many idle VMs running would conflict with the hardware and
economic premise of Scraps.

Compute, memory, and disk must therefore be reclaimed as normal behavior, not
as exceptional operator maintenance. The durable object is the workspace; an
allocated runtime is a temporary implementation detail.

## Decision

Scraps optimizes for **zero idle CPU and memory**, bounded disk use, and honest
admission control. It prefers snapshots, copy-on-write storage, shared read-only
bases, and rebuildable caches over continuously running warm machines.

### Durable identity, temporary runtime

A workspace has durable identity and storage while its provider runtime may be
absent. Its lifecycle is:

```text
creating -> running -> sleeping -> waking -> running
    |          |          |          |
    +----------+----------+----------+-> failed

sleeping/running/failed -> deleting -> deleted
```

`running` means compute is allocated. `sleeping` means no CPU or memory is
reserved for the workspace; only its durable workspace state and metadata
remain. A provider that cannot fully release compute must report the retained
resources and does not satisfy the production sleep capability.

`stopped` remains an explicit administrative state in the current API during
migration. The target model distinguishes:

- **sleeping:** automatically reclaimable and transparently wakeable;
- **suspended:** intentionally prevented from automatic wake by an operator or
  user, replacing the long-term meaning of explicit `stopped`.

Commands, headless runs, attachments, and authenticated events may request a
wake. Concurrent wake requests coalesce into one provider transition.

### Activity leases

Compute remains allocated only while at least one bounded activity lease is
valid. Lease kinds initially include:

- command execution;
- active headless run;
- attached interactive client heartbeat;
- explicitly exposed service;
- setup, resume, readiness, snapshot, or provider maintenance operation.

Leases have owners, creation and expiry times, maximum durations, and renewal
rules. A process existing inside a workspace is not by itself a lease. This
prevents forgotten dev servers, background shells, or leaked descendants from
pinning a VM forever.

Default policy:

- after the final lease ends, the workspace receives a **two-minute idle grace
  period** and then sleeps;
- interactive attachment heartbeats expire after **90 seconds** without
  renewal;
- service leases default to **one hour** and require explicit renewal;
- command execution retains ADR 0002's server-side timeout and one-hour hard
  cap;
- a headless run has a bounded policy duration and heartbeat, defined with the
  run API; it cannot hold compute indefinitely merely by remaining in a
  `running` database state.

Operators may configure these values globally within documented safe bounds.
Projects cannot silently extend leases. Clients can request an extension, which
is policy checked and visible in diagnostics.

Before sleep, Scraps stops the full runtime and relies on durable workspace
storage, not arbitrary process memory. `.scrap/resume` restores ephemeral
services on wake. Providers may support memory-preserving suspend as an optional
optimization, but it is not the baseline because suspended RAM still consumes a
scarce resource or large disk image.

### Per-workspace limits

Every sandbox workspace has effective limits for:

- vCPU;
- memory and swap;
- PIDs;
- writable disk;
- command/run duration;
- concurrent execs and runs;
- exposed services;
- network policy.

ADR 0004's initial default of 2 vCPU, 4 GiB memory, 512 PIDs, and 20 GiB writable
storage remains the standard class. Scraps additionally supports smaller
resource classes so constrained installations can choose a lower default. A
request may ask for a class, never raw unbounded capacity. Overrides are capped
by operator policy and reported on the workspace.

The daemon also enforces installation-wide budgets. Provider capacity reporting
must distinguish total, reserved, in-use, and safely allocatable CPU, memory,
disk, and workspace slots. Scraps reserves configurable host headroom rather
than scheduling to physical exhaustion.

### Admission and queueing

When a create or wake request does not fit within the allocatable budget,
Scraps does not overcommit invisibly or randomly kill active work. It:

1. reclaims workspaces whose grace period has elapsed;
2. evicts eligible rebuildable caches if disk is the constraint;
3. queues the request with a visible reason and position, or fails immediately
   when the caller explicitly requests no queue;
4. starts queued work in fair order subject to resource-class fit.

Interactive work may receive a configurable priority over background
maintenance, but active headless runs are not preempted in the initial design.
Future preemption requires checkpoint semantics and a separate decision.

### Warmth hierarchy and the n+1 ready pool

Scraps optimizes startup in this order:

1. shared base images/templates;
2. dependency and package caches;
3. immutable project environment snapshots;
4. copy-on-write workspace storage;
5. stopped or unallocated provider objects that consume no CPU or memory;
6. a strictly bounded pool of running, unassigned containers or VMs.

Within the installation-wide budget, the daemon maintains **n+1 ready
runtimes**: enough runtimes for the `n` currently checked-out workspaces plus
one unassigned, preheated runtime. This is a target, not permission to
oversubscribe. The effective ready-spare target is therefore:

```text
min(1, safely allocatable workspace slots after active leases)
```

On daemon startup, reconciliation immediately requests the first ready runtime.
A new-workspace request atomically checks out an unassigned ready runtime,
assigns a fresh workspace identity, and starts repository/project
materialization. The checkout must never expose files, credentials, process
state, or identity from a prior owner. As soon as checkout commits, the daemon
asynchronously requests its replacement so the next request can use a ready
runtime.

If no ready runtime exists, creation follows normal admission control: it may
allocate directly when capacity fits, otherwise it queues or fails according to
the request policy. Failure to replenish the spare never fails or rolls back a
successful checkout. Concurrent requests claim distinct runtimes through a
durable compare-and-swap/transaction; a runtime can have only one owner.

A ready runtime is deliberately blank and disposable. Project-specific warmth
comes from ADR 0006's immutable project builds, not from passing a user
workspace between owners. Providers must reset or destroy a checked-in runtime;
they must not relabel a dirty workspace as ready. Until a provider proves a
safe reset operation with conformance tests, destruction and clean replacement
is mandatory.

The ready spare has no user lease, a hard pool count of one, and a short
creation deadline. It is the first running compute reclaimed under memory/CPU
pressure and may be absent while capacity is exhausted. Operators may disable
the running spare or cap it to zero on installations that cannot afford one;
Scraps reports the degraded cold-start path rather than overcommitting. Pool
state (`creating`, `ready`, `checking-out`, `destroying`, `failed`), target,
actual count, capacity reason, and checkout/replenishment latency are visible in
status and diagnostics.

This resolves the pool-policy portion of GitHub issue #9: leases govern active
workspace ownership and reclamation; the single disposable spare improves the
next allocation, while reusable project environments remain builds from ADR
0006.

### Disk budgets, retention, and garbage collection

Disk is treated as a finite scheduled resource. Storage accounting separates:

- immutable shared bases;
- rebuildable package caches;
- rebuildable project environment builds;
- unpinned workspace state;
- pinned workspace state;
- artifacts and logs.

The default workspace retention policy is:

- newly created workspaces are **temporary**;
- an inactive temporary workspace expires after **seven days**;
- users may pin a workspace or choose a shorter explicit TTL;
- expiry and pin status are shown at creation, listing, attachment, and before
  destructive collection where a client can receive warnings.

Expired temporary workspaces may be deleted automatically. Pinned workspaces
are never automatically deleted merely to satisfy normal disk pressure; if they
consume the protected budget, new allocation is queued or rejected with an
actionable error.

Cleanup is continuous rather than an operator-only job. On startup and after
every checkout, release, failed creation, lease expiry, and provider error, the
daemon reconciles owned resources. It immediately destroys failed or partially
initialized pool members, checked-in runtimes that cannot be proven clean,
duplicate ready runtimes above the target, and orphaned provider resources with
valid Scraps ownership labels. Unknown or ambiguously owned resources are
quarantined and reported instead of being deleted blindly. Workspace deletion
removes compute first, then mutable storage and metadata; retries are
idempotent, back off, and remain visible until every provider resource is gone.

Rebuildable data is reclaimed before unexpired workspace data. Providers use
configurable low/high/critical disk watermarks, with initial defaults of 70%,
85%, and 95%:

- above the low watermark, normal LRU cache trimming begins;
- above the high watermark, unused project builds and expired workspaces are
  collected aggressively;
- above the critical watermark, new creates/builds are rejected, running work
  receives a visible warning, and Scraps preserves enough headroom for orderly
  shutdown and metadata writes.

Deletion order, bytes recovered, and reasons are audit logged. Scraps never
claims a writable-disk limit that the provider cannot enforce, consistent with
ADR 0004.

### Reconciliation and failure behavior

Leases and desired state are stored durably. After daemon or provider restart,
Scraps reconciles rather than trusting stale `running` rows:

- expired leases are discarded;
- unleased runtimes are stopped;
- duplicate or orphaned provider resources are quarantined or removed according
  to ownership labels;
- queued requests are reconsidered against current capacity;
- a workspace is never leased mutably to two owners concurrently.

Sleep and wake operations are idempotent. Failure to persist a transition must
not leave an unaccounted running runtime.

### Visibility

`scrap ls`, `scrap status`, and the API expose:

- lifecycle and desired state;
- active lease kinds and expiry, without secrets;
- idle deadline and workspace expiry;
- resource class and effective limits;
- disk usage and pin state;
- wake queue reason and position;
- project build identity and expected wake path;
- provider capacity and protected host headroom.

Aggressive reclamation must feel predictable rather than mysterious.

## Consequences

### Positive

- Many durable workspaces can exist on hardware that can run only a few at once.
- Forgotten processes cannot consume resources indefinitely.
- Warm-start work focuses on snapshots and COW state, plus at most one bounded
  disposable ready runtime when capacity permits.
- Capacity exhaustion becomes visible queueing rather than host instability.
- Temporary state and rebuildable caches cannot silently fill the machine
  forever.
- The interface can feel abundant while remaining honest about finite capacity.

### Negative

- Wake latency occurs frequently because sleeping is the normal idle state.
- Projects must make resume reliable and cannot assume background processes live
  forever.
- Automatic expiry can surprise users unless TTL and pinning are prominent.
- Accurate disk accounting and provider-neutral capacity reporting are
  operationally difficult.
- Queues are less immediately gratifying than unsafe overcommit.
- A two-minute grace period may be too aggressive for some workflows and will
  require measurement and tuning.

## Alternatives considered

### Keep every workspace running until explicitly stopped

Rejected. It turns workspaces into pets, exhausts small hosts, and makes users
manage the resource lifecycle that Scraps exists to hide.

### Maintain a large running warm pool

Rejected. It consumes the exact CPU and memory Scraps needs for real work.
Scraps keeps at most the single n+1 spare allowed by capacity; snapshots,
caches, and stopped provider objects remain the preferred forms of warmth.

### Treat any live process as activity

Rejected because abandoned development servers and daemonized children would
prevent reclamation forever. Only explicit bounded leases count as activity.

### Overcommit and let the host swap or OOM-kill

Rejected. It produces unpredictable cross-workspace failures and can destabilize
`scrapd` and the host. Admission control and queueing are explicit.

### Never delete workspace data automatically

Rejected as the default for temporary workspaces on scrap hardware. Pinning
provides durable retention; temporary workspaces have a visible bounded life.

### Snapshot guest memory when sleeping

Not the baseline. Memory snapshots consume substantial disk, retain sensitive
runtime state, and complicate compatibility. Providers may add it as an
explicit capability after measurement.

## Follow-up work

1. Use this lifecycle, lease, pool, and pressure model to resolve GitHub issue #9.
2. Add persisted desired state, lease records, and idempotent reconciliation.
3. Add provider capacity and per-workspace usage reporting.
4. Implement automatic idle sleep and transparent wake for Docker, then map it
   to portable worker behavior before adding driver-specific optimizations.
5. Add TTL, pinning, disk watermarks, dry-run garbage collection, and audit logs.
6. Benchmark two-minute sleep, snapshot materialization, and readiness on target
   representative local and remote worker hardware before changing defaults.
7. Add integration tests for daemon restart, lease expiry, disk pressure,
   queueing, and protection of pinned workspaces.
8. Implement the durable ready-pool state machine: create one spare on startup,
   atomically check it out, replenish it after checkout, and suppress or reclaim
   it under capacity pressure.
9. Add provider conformance tests proving that dirty or failed runtimes are
   destroyed, duplicate spares are collected, and no state crosses checkout
   boundaries.
