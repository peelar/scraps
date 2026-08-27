# ADR 0013: Clone and move the local Pi environment into the personal worker

- Status: Accepted
- Date: 2026-08-27
- Supersedes: the standalone worker-configuration assumptions in ADR 0012

## Context

ADR 0012 moved the authoritative Pi agent loop to the worker so `/scrap` turns
survive client sleep and disconnection. Its first implementation treated the
worker like a newly provisioned cloud service: install Pi, separately enroll a
model credential, and begin a new remote conversation.

That is the wrong product model for Scraps. A Scraps worker is the operator's
personal, trusted compute environment—an extension of the local machine—not a
multi-tenant SaaS account. The operator has already configured and authorized
Pi locally. Repeating cloud-style onboarding creates drift and makes `/scrap`
feel like switching to another agent rather than moving the current agent to
durable compute.

Scraps already has an appropriate privileged bootstrap path. `scrap attach
[user@]worker` reaches the worker over the operator's existing SSH/Tailscale
trust, retrieves its runtime endpoint and bearer token, and writes a protected
local client profile. Normal run traffic then uses authenticated Tailscale Serve
HTTPS.

## Decision

### Personal replica, not a second account

The trusted worker-side Pi runner is a replica of the operator's local Pi
environment. Normal setup clones safe, relevant local state to the worker rather
than asking the operator to configure Pi again.

The normal workflow remains:

```text
scrap attach user@worker   # first attachment also bootstraps the runner replica
pi
/scrap                     # moves execution of this Pi session to the worker
```

There is no required `scrap auth model` step in the normal path. A
worker-local credential command may remain as a recovery and headless
administration mechanism, but it is not the product's primary authorization
model.

### Two operations: clone environment, move execution

Scraps distinguishes two related operations:

1. **Clone the trusted Pi environment.** Copy an allowlisted, versioned profile
   from the local Pi installation into trusted worker control-plane storage.
   Local Pi remains usable outside Scraps.
2. **Move session execution.** On `/scrap`, import the relevant local conversation
   branch into a remote Pi session and make that remote session authoritative
   for subsequent turns. The local session becomes a reconnectable viewer and
   cache.

“Move” describes ownership, not destructive file transfer. Local session data
is retained for recovery, but it must not continue an independent agent loop
for the moved branch.

### Bootstrap and synchronization channel

Initial cloning uses the same SSH/Tailscale administrative trust path as
`scrap attach`, not the ordinary workspace API bearer token. The transfer must:

- use SSH transport or an equivalently authenticated, privileged Tailscale
  administrative channel;
- stage files privately and atomically activate a complete profile generation;
- preserve owner-only credential permissions;
- avoid printing secrets, placing them in command arguments, or writing them to
  shell history and logs;
- never pass profile contents through an OpenShell workspace.

Normal `/scrap` activation compares a non-secret profile manifest and
synchronizes safe drift before accepting a prompt. `scrap sync` and `scrap sync
--status` may exist as explicit repair and diagnostic commands, but routine use
must not require them.

### Profile boundary

The cloned runner profile is allowlisted by category.

Clone by default:

- Pi provider authorization from `auth.json`;
- model/provider declarations needed to resolve the selected model;
- safe behavioral settings, system-prompt inputs, and context files;
- skills and prompt templates;
- Scraps-specific declarative environment approvals;
- the selected model and reasoning level needed to continue the moved session.

Do not clone by default:

- themes, terminal settings, keybindings, or other local TUI preferences;
- local filesystem paths and project checkout state;
- browser state, shell history, caches, telemetry identifiers, or transient
  lock files;
- arbitrary executable Pi extensions or packages;
- unrelated process environment variables.

The Scraps extension required by the runner remains release-controlled and
embedded by Scraps. Additional executable extensions require an explicit
allowlist and version/integrity manifest. Declarative skills and prompts are not
implicitly allowed to escape the same review and size limits applied to the
profile bundle.

Environment-only model keys are not silently scraped from the local process.
Where local Pi authorization lives only in an environment variable, Scraps must
ask for explicit consent to clone that named credential or guide the operator
to store it in Pi's protected `auth.json`. Values are never displayed.

### Remote profile storage and mutation

Runner profiles live outside project workspaces under protected Scraps
control-plane storage, keyed by local client identity and profile generation.
The runner receives that path through `PI_CODING_AGENT_DIR`; it does not use an
unmanaged service-user home as the source of truth.

Profile activation is atomic. Synchronization preserves remote mutable auth
state, such as refreshed OAuth tokens, rather than overwriting it with an older
local snapshot. Credential replacement, revocation, and conflict behavior must
be explicit. Remote profile state is included in encrypted control-plane
backups and excluded from workspace snapshots and run events.

### Session import and authority

Before the first durable prompt for a local Pi branch, Scraps creates or resumes
a remote session and imports a normalized snapshot of the branch needed for
model continuity. The protocol records at least:

- local client and session identity;
- source branch/head identity;
- remote session identity;
- profile generation;
- workspace binding;
- last imported local entry and last replayed remote event.

Import is idempotent. Once moved, the remote session is authoritative for model
messages, tool calls, compaction, branching, usage, and subsequent prompts. The
local JSONL is a mirror/cache and must not become a competing source of truth.
Reconnection uses the remote session ID and monotonic event cursor.

Moving to a different workspace requires an explicit remote workspace-binding
transition or a new remote session; it must not accidentally reuse an unrelated
workspace's conversation merely because both runs came from the same local
session directory.

### Run acceptance is the durability boundary

Profile synchronization and session import happen before, or are durably
captured as part of, run creation. Once scrapd returns an accepted run ID, the
worker must have everything required to finish without contacting the laptop.
No accepted runner may lazily fetch credentials, skills, prompts, session
entries, or extension code from the client. A failure to establish this
self-sufficient checkpoint rejects the run rather than accepting a weaker local
dependency.

### Runtime remains pinned

Scraps continues to ship a pinned, checksum-verified worker-side Node and Pi
runtime. “Clone the local environment” means cloning compatible profile and
session state, not copying a host-specific executable or `node_modules` tree.
The profile manifest declares its source Pi version, and attachment rejects or
migrates incompatible state deliberately.

## Consequences

- A personal worker feels like durable continuation of local Pi rather than a
  separately configured cloud agent.
- Initial attachment becomes responsible for both connection setup and trusted
  runner bootstrap.
- Model credentials no longer need a second normal enrollment flow.
- SSH/Tailscale administrative access remains necessary for initial attachment
  and sensitive profile replacement; ordinary run traffic remains on Tailscale
  Serve HTTPS.
- Scraps needs profile manifests, filtering, atomic generations, drift handling,
  and protected backup/retention; the initial allowlist, manifest, and atomic
  install are implemented.
- Exact conversational continuation requires local-branch import before run
  acceptance; the initial linear active-branch checkpoint/import is implemented.
- OAuth refresh creates mutable remote credential state, so synchronization
  cannot be implemented as blind one-way directory replacement.
- Multi-user tenancy, organization policy, shared cloud credential vaults, and
  generic SaaS onboarding are explicitly out of scope for this personal-cloud
  architecture.

## Compatibility with the current implementation

The durable run/event API, detached process lifecycle, cancellation,
reconnection cursor, Tailscale transport, pinned runner, workspace isolation,
and remote-session storage are compatible foundations.

The initial implementation now:

- treats `scraps-worker model-auth` as recovery/headless administration;
- clones an allowlisted, integrity-manifested profile during `scrap attach`;
- rejects credentials tied to laptop commands or environment variables;
- installs the profile atomically in dedicated writable control-plane storage;
- runs worker Pi with that directory through `PI_CODING_AGENT_DIR`;
- durably stores the active local branch with run creation, imports it once into
  remote JSONL, and forwards provider/model/thinking selection; and
- never replaces an existing authoritative remote session with a later local
  snapshot.

Automatic profile drift reconciliation, safe settings projection, profile
version rollback, explicit remote branch APIs, and a faithful local JSONL mirror
remain implementation gaps. They do not justify separate local and remote
configuration modes.
