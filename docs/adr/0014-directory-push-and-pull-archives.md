# ADR 0014: Explicit directory push and pull archives

- Status: Accepted
- Date: 2026-08-27
- Amends: ADR 0001 (git remote as the only workspace transport)

## Context

ADR 0001 made a git remote the transport between a developer machine and a
Scraps workspace: workspaces never receive local files directly, and a local
checkout contributes its origin URL at creation time. This keeps provenance in
git and local inspection minimal, and it works well for the primary flow — a
repository exists, both sides clone and push.

Four real flows fall outside that decision:

1. **Scratch workspaces.** `scrap new` without `--repo` creates an empty
   workspace and the agent builds a project inside it. There is no reverse
   path: the only ways out are file-by-file reads or "publish to a git remote
   first," which presumes a remote that does not exist yet.
2. **Work in progress.** Dirty trees, untracked files, and never-pushed
   configuration cannot cross the boundary without a commit, which is exactly
   what the developer is not ready to do.
3. **Non-git projects.** A directory of notes, a downloaded dataset, or a
   one-off experiment has no origin URL to offer.
4. **Bootstrap latency.** Cloning a large repository from a forge is slower
   than copying bytes that already sit on the local machine.

The files API (`/v1/workspaces/{id}/files/*`) already moves individual files in
both directions, so the boundary is not an airlock; bulk transfer does not
create a new category of access, only a faster version of an existing one.

## Decision

Workspace seeding and extraction gain an explicit, user-initiated, one-shot
transport: **tar archives streamed over the existing authenticated API**.
Git remains the recommended continuous transport for repository work.

```text
scrap push [--replace] [workspace-id] <dir>   # local directory → workspace
scrap pull [workspace-id] [target]            # workspace → local directory
```

### API

- `POST /v1/workspaces/{id}/files/archive` with `Content-Type: application/x-tar`
  imports the archive into `/workspace`.
- `GET /v1/workspaces/{id}/files/archive` exports the workspace as a tar
  stream. `.scrap/**` (Scraps' internal directory) is excluded in both
  directions, and oversized or non-regular entries are skipped and counted in
  an `X-Scraps-Skipped-Entries` response header.

### Semantics

- **Literal copy.** The directory is copied as-is, `.git` included: a pushed
  checkout arrives with its history and the same dirty working tree, so
  `git status` inside the workspace matches the local one. No ignore rules.
- **Empty-workspace rule.** Push requires an empty workspace (`.scrap` alone
  is tolerated); otherwise the API answers `409 conflict`. `--replace` clears
  the workspace first through a new provider `RemoveAll` primitive. Deletion
  is never implied by an overwrite.
- **Pull safety.** The CLI extracts into a nonexistent or empty target
  directory; `--force` overlays an existing directory without deleting
  anything.
- **One-shot only.** No watch mode, no rsync protocol, no bidirectional sync.
  Workspaces are sessions, not mirrors (ADR 0007); conflicting concurrent
  edits are out of scope by construction.

### Security

- Both endpoints sit behind the standard bearer token like every other
  workspace operation.
- Imported entry names must be workspace-relative: absolute paths and `..`
  components are rejected, and every write re-validates through the provider's
  path containment (`ResolvePath` for the directory provider, the containment
  script for OpenShell). Only regular files and directories are imported;
  symlinks, hardlinks, and device entries are rejected rather than recreated,
  so an archive cannot plant a symlink escape.
- Per-file and whole-archive size limits match the existing files API limits;
  archives are streamed, never buffered whole.
- Copying secrets (`.env`, keys) into a workspace crosses the same trust
  boundary the files API already allows; the risk is unchanged and the
  decision to send remains an explicit user action.

## Consequences

- Scratch workspaces become full round trips: push a directory in, work
  agent-side, pull the result back out, or publish it to a forge later. The
  workspace exit paths are now pull (files back), publish (into a repo),
  or toss (gone).
- The extension offers a directory copy when `/scrap` runs in a non-git
  directory, mirroring the existing clone confirmation for git checkouts.
- The provider interface grows `RemoveAll` so `--replace` works uniformly for
  the directory and OpenShell providers.
- Large-file and large-archive transfers over the per-file files API would be
  slow for big trees; if that matters, a provider-level bulk path can be added
  later without changing the API contract.
