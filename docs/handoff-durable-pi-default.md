# Handoff: durable remote Pi as the `/scrap` default

Date: 2026-08-27
Status: accepted product direction; first vertical slice implemented
Decisions: ADR 0012 and ADR 0013

## Product contract

The user experience stays:

```text
pi
/scrap
> do the task
```

Every prompt submitted while a Scraps workspace is attached belongs to a
remote durable Pi run. There is no normal/durable selector. Closing the laptop,
losing Wi-Fi, or losing the Tailscale connection detaches the local viewer but
does not cancel an accepted run. `pi -c` or `/resume` reconnects using the run
ID and event cursor stored in the local Pi session.

Durability is mandatory in Scraps mode. A worker without a durable runner fails
closed and shows `runner:unavailable`; it must never silently execute the agent
loop on the laptop.

## Implemented

### Control plane

- SQLite `runs` and `run_events` tables.
- Run states: `queued`, `running`, `succeeded`, `failed`, `cancelled`.
- Immutable, monotonically sequenced JSON event log.
- `POST /v1/workspaces/{id}/runs` accepts the prompt, session identity, and a
  bounded local-branch checkpoint; all are durable before acceptance.
- `GET /v1/runs/{id}` returns durable state.
- `GET /v1/runs/{id}/events?after=N` supports cursor-based replay.
- `POST /v1/runs/{id}/cancel` idempotently cancels active execution.
- Run execution is detached from the request context.
- One active mutating run per workspace in the current daemon process.
- Cancellation and server shutdown terminate the complete trusted Pi process
  group, with SIGTERM followed by a bounded SIGKILL fallback, and reap it.
- `GET /v1/info` reports `features.durableRuns` and recognized model authorization.

### Runner

- Worker bundles contain checksum-verified Node.js 22.23.2 and a lockfile-pinned
  Pi 0.84.3; installation and rollback keep the runner inside the versioned
  release under `/opt/scraps`.
- scrapd launches that trusted worker-side Pi executable using JSON mode.
- Each client session gets a dedicated Pi session directory, allowing `-c` to
  continue the remote conversation between prompts.
- The embedded Scraps extension is extracted for the worker-side Pi process.
- Remote Pi receives the workspace ID, loopback scrapd URL, and bearer token;
  its project tools continue targeting the OpenShell workspace.
- `scrap attach` packages an allowlisted local Pi profile (`auth.json`,
  `models.json`, `AGENTS.md`, skills, and prompt templates), hashes it in a
  non-secret manifest, and installs it atomically over the existing SSH/Tailscale
  administrative trust path.
- The worker validates archive paths and hashes, stores the profile outside
  OpenShell with owner-only permissions, preserves remotely refreshed OAuth
  state, and restarts scrapd.
- Remote Pi receives the protected profile through `PI_CODING_AGENT_DIR`;
  credentials are never copied into the project workspace.
- Environment- or Mac-keychain-bound credential references are rejected rather
  than cloning a profile that would depend on the laptop.
- The first durable run imports the captured local Pi branch into a remote JSONL
  session. Later local snapshots cannot replace that authoritative remote
  session. Selected provider, model, and thinking level cross the checkpoint.

### Local Pi extension

- Detects the required runner capability.
- Intercepts interactive prompts after `/scrap` and prevents the local agent
  loop from running.
- Creates a durable run, saves its binding immediately, and polls events.
- Replays assistant messages from Pi JSON events.
- Saves the last event cursor and closes the final-event/state race.
- Reconnects an unfinished run during Pi session resume.
- Fails closed when the runner is absent or cannot be verified.

### Tailscale

No new public listener was added. Run creation, polling, and replay use the
existing authenticated scrapd HTTP endpoint published through Tailscale Serve
HTTPS. Tailscale is transport only; connection lifetime does not own run
lifetime.

## Current profile bootstrap

Normal worker bundles install the pinned runner automatically. `scrap attach
user@worker` now clones the portable local Pi profile and verifies after the
worker restart that durable execution and model authorization are independently
available. `scraps-worker model-auth` remains a recovery/headless administration
command, not the normal product workflow. A custom installation without the
bundled runner reports `features.durableRuns: false`, and Scraps prompts fail
closed by design.

## Remaining work

### P0 — make the new default operational in normal installation

1. **Finish profile drift synchronization.**
   Initial cloning during `scrap attach` is implemented. Add a non-secret
   manifest/status API and automatic pre-`/scrap` reconciliation for changed
   skills, prompts, models, and safe settings. Add `scrap sync [--status]` for
   repair. Preserve remote OAuth refreshes and make credential replacement and
   revocation explicit rather than blindly overwriting auth state.
2. **Finish setup readiness verification.**
   `scrap status`, worker health checks, and `/scrap` now distinguish a missing
   runner from missing model authorization, and installation verifies the pinned
   Pi executable/capability. Add a non-billable provider/auth probe (or an
   explicitly confirmed billable probe) so a present but invalid/expired key is
   detected before the first real prompt. Report incompatible Pi separately
   from a missing executable on remotely managed custom installations.
3. **End-to-end acceptance test.**
   Start a run through the Tailscale Serve URL, terminate the local client,
   verify the run completes, resume the Pi session, replay the result, and
   verify files changed only inside the intended OpenShell workspace.

### P0 — correctness and control

4. **Finish native cancellation UI.**
   The cancellation API, process-group termination, durable `cancelled` state,
   idempotency, TypeScript client, and `/scrap-cancel` command are implemented.
   Connect Pi's native Escape/cancel action to the same API without making client
   disconnect itself a cancellation signal.
5. **Define daemon-restart retry policy.**
   Startup reconciliation now marks orphaned `queued`/`running` rows failed, so
   stale `running` is never reported. Decide whether eligible prompts should be
   retried after a crash; any retry must be explicit and idempotency-aware.
6. **Finish multi-server run coordination.**
   A partial SQLite unique index now authoritatively enforces one active run per
   workspace, with the in-memory map retained as a fast path. If scrapd becomes
   multi-process or multi-host, add ownership leases so cancellation and crash
   takeover can locate the executing process.
7. **Finish run limits and lifecycle leases.**
   The server now enforces prompt/session-key size, two-hour duration, per-event
   size, event count, and total persisted output limits. Add configurable
   operator policy, retained-session/disk quotas, and integration with workspace
   activity leases before automatic sleeping is enabled.

### P1 — native Pi fidelity

9. **Stream instead of polling.**
   Add authenticated SSE or a reconnectable streaming response with sequence
   cursors. Keep polling as a recovery path. Do not tie stream closure to run
   cancellation.
10. **Render complete Pi events.**
    The initial client displays finalized assistant text. Add native-looking
    thinking, tool call, partial tool output, usage, compaction, retry, and error
    rendering. Preserve event IDs to prevent duplicate display after reconnect.
11. **Remote runtime hook in Pi.**
    Evaluate adding a Pi SDK/extension hook that replaces the agent runtime
    while retaining the built-in TUI and session renderers. This is preferable
    to indefinitely recreating native rendering with custom messages.
12. **Forward native controls.**
    Model selection, thinking level, steering/follow-up messages, `/compact`,
    `/tree`, `/fork`, and `/clone` need defined remote semantics. Until then,
    unsupported controls should fail clearly rather than mutate only the local
    mirror.

### P1 — personalization and session ownership

13. **Finish the versioned runner profile.**
    The allowlisted profile archive, integrity manifest, SSH installation,
    protected runner directory, and executable-extension exclusion are
    implemented. Add a filtered safe-settings projection, explicit extension
    allowlisting, Pi-version compatibility migrations, atomic generation
    history/rollback, and automatic drift reconciliation.
14. **Finish authoritative remote session semantics.**
    The first accepted run now durably stores and imports the active local branch
    exactly once; the worker then retains authority. Define explicit remote
    branch/session IDs and metadata APIs instead of relying on the latest file in
    a per-client directory, and support deterministic remote branching.
15. **Local mirror rules.**
    Define exactly which remote events are written into local Pi JSONL so
    `/resume`, exports, token totals, and tree navigation remain useful without
    creating two sources of truth.
16. **Workspace switching.**
    Decide whether one Pi conversation can move between workspaces. If allowed,
    record each binding transition remotely; otherwise create a new remote
    session when `/scrap-select` changes workspace.

### P1 — security and operations

17. **Runner isolation.**
    Run Pi under a dedicated service identity or constrained runner container,
    separate from both scrapd and OpenShell workloads. Give it only the API
    capability for its assigned workspace and run.
18. **Per-run capabilities.**
    Replace the daemon-wide bearer token inherited by Pi with short-lived,
    workspace- and run-scoped tokens. Revoke them on completion/cancellation.
19. **Secret redaction and audit.**
    Audit JSON events, stderr, prompts, errors, and session files for credential
    leakage. Record run creator, workspace, model/provider, lifecycle, and
    cancellation without recording secrets.
20. **Tailscale authorization.**
    Keep scrapd behind Tailscale Serve HTTPS and validate ACL/tag assumptions.
    The bearer token remains defense in depth; future multi-user support needs
    an authenticated user identity rather than one shared operator token.
21. **Backup and retention.**
    Include remote Pi sessions and run metadata in encrypted backups. Define
    event/session retention and deletion behavior when a workspace is tossed.

## Known limitations of the current slice

- Initial profile cloning is automatic during `scrap attach`, but profile drift
  is not yet reconciled automatically during `/scrap`. Environment- or
  keychain-command-based credentials require conversion to portable Pi auth.
- Results are polled every 500 ms rather than streamed.
- Only finalized assistant text is mirrored into the local TUI.
- `/scrap-cancel` cancels remotely, but Escape is not wired to it yet.
- Steering and queued follow-up messages are not forwarded remotely.
- A scrapd restart now marks orphaned queued/running rows failed rather than
  leaving stale `running`; automatic retry/resume is not implemented.
- The active-run cache is process-local, but a partial SQLite unique index is
  now authoritative across daemon instances.
- Auth, model declarations, AGENTS.md, skills, and prompts are cloned; a
  filtered safe subset of `settings.json` is not implemented yet.
- Worker Node and Pi are pinned and verified, but profile-format compatibility
  migrations between local and worker Pi versions are not implemented.

## Key files

- `docs/adr/0012-durable-pi-runs-behind-scrap.md`
- `docs/adr/0013-clone-the-local-pi-environment-into-the-personal-worker.md`
- `cmd/scrap/profile.go` — local profile allowlist, integrity manifest, and SSH sync
- `internal/store/runs.go`
- `internal/store/store.go`
- `internal/server/runs.go`
- `internal/server/server.go`
- `cmd/scrapd/main.go`
- `deploy/worker/manifest.env` — pinned Node/Pi versions and Node checksums
- `deploy/worker/pi-package-lock.json` — pinned Pi dependency graph
- `scripts/build-worker-bundle` — trusted runner assembly
- `deploy/worker/install` and `deploy/worker/scraps-worker` — installation,
  readiness, health, and model-key enrollment
- `packages/pi-extension/src/runs.ts`
- `packages/pi-extension/src/client.ts`
- `packages/pi-extension/src/index.ts`
- `internal/extension/files/` — generated embedded copy; run `make sync-extension`

## Validation

Run:

```bash
make sync-extension
make check
pnpm test
go test -race ./internal/server ./internal/store
```

The initial implementation passed all of these checks when this handoff was
written.
