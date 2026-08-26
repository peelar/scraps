# ADR 0002: scrapd workspace HTTP API

- Status: Accepted
- Date: 2026-08-24

## Context

ADR 0001 runs Pi locally and backs its project-facing tools with a remote
Scraps workspace served by `scrapd`. The daemon and the Pi extension need an
agreed contract for workspace lifecycle, command execution, and file/search
operations before either side is implemented.

The first provider is directory-backed: `scrapd` runs on the developer machine
(or a single Linux host) and each workspace is a directory under the data dir.
Commands execute on the `scrapd` host with the workspace directory as working
directory. This provider has no isolation; VM isolation arrives in M4.

## Decision

### Conventions

- Base URL default `http://127.0.0.1:8484`; all operations under `/v1`.
- If `SCRAPD_TOKEN` is set, every `/v1` request must carry
  `Authorization: Bearer <token>`. Comparison is constant-time. `/healthz` is
  open. An unset token disables auth (local development only).
- Request and response bodies are JSON. Errors use:

  ```json
  { "error": { "code": "not_found", "message": "..." } }
  ```

  Codes: `unauthorized` (401), `invalid_request` (400), `invalid_path` (400),
  `not_found` (404), `workspace_not_available` (409), `internal` (500).
- File paths and exec `cwd` values in API requests are **workspace-relative**.
  `.` denotes the workspace root. Absolute paths and lexical or symlink escapes
  are rejected with `invalid_path`.
- File search paths in responses are workspace-relative. Provider host paths,
  container mountpoints, and VM disk layouts never cross the API boundary.
- Pi and the model always see the stable virtual root `/workspace`. The
  workspace resource declares `rootPath: "/workspace"` and
  `pathContract: "workspace-relative-v1"`. The extension translates between
  this display space and relative API paths.

### Workspace resource

```json
{
  "id": "quiet-river",
  "project": "owner/repository",
  "repoUrl": "https://github.com/owner/repository",
  "state": "running",
  "rootPath": "/workspace",
  "pathContract": "workspace-relative-v1",
  "createdAt": "2026-08-24T12:00:00Z",
  "updatedAt": "2026-08-24T12:00:00Z"
}
```

- `POST /v1/workspaces` `{project?, repoUrl?}` → 201 workspace. IDs are
  generated adjective–noun names, unique in the store. When `repoUrl` is set
  the daemon runs `git clone <repoUrl> .` into the workspace root before
  returning; clone failure fails creation and cleans up. Private repositories
  are out of scope until M3 identity work.
- `GET /v1/workspaces` → `{workspaces: [...]}`
- `GET /v1/workspaces/{id}` → workspace
- `POST /v1/workspaces/{id}/start` → 200 with the running workspace.
- `POST /v1/workspaces/{id}/stop` → 200 with the stopped workspace.
- `DELETE /v1/workspaces/{id}` → 204, removes provider runtime/storage and the row.

State is `running` or `stopped`. Docker start/stop maps to container lifecycle
and preserves its private volume. Directory mode records the state but cannot
provide process isolation.

### Streaming execution

`POST /v1/workspaces/{id}/exec` `{command, cwd?, env?, timeoutMs?}`:

- `command` runs under `/bin/bash -c` in `cwd` (default `.`, validated like
  file paths). Sandboxed providers mount the workspace at `/workspace`. The
  trusted directory provider emulates that root and redacts its host path from
  command output.
- Providers construct a minimal environment from scratch; explicit `env`
  entries are request-scoped overrides. The daemon environment is never
  inherited wholesale (ADR 0004).
- `timeoutMs` caps execution server-side (default none, hard cap 1 hour).

The response is `application/x-ndjson`, flushed per event:

```json
{"type":"start","pid":1234}
{"type":"output","stream":"stdout","data":"<base64>"}
{"type":"exit","code":0,"durationMs":12}
{"type":"exit","code":null,"reason":"timeout","durationMs":30000}
```

- Output chunks are base64-encoded and interleaved in arrival order; the
  extension decodes them into Pi's `BashOperations.onData` buffers.
- `exit.code` is the process exit code, or `null` with `reason` `timeout` or
  `cancelled` when the process was killed.
- **Client disconnect is the abort mechanism**: closing the request cancels
  the server context, which kills the process group (`SIGKILL` on the process
  group, since commands are started with `setpgid`). No stdin support in M1.
- Pre-execution failures (unknown workspace, invalid path, bad request) use
  the HTTP error shape, not events.

### Files and search

All take `{path, ...}` with workspace-relative validated paths:

- `POST /v1/workspaces/{id}/files/read` → `{content: "<base64>", size}`.
  Binary-safe; the extension decodes to a `Buffer` for Pi's read/edit
  operations. Files over 100 MB are rejected (`invalid_request`).
- `POST /v1/workspaces/{id}/files/write` `{content: "<base64>"}` → `{size}`.
  Parent directories are created.
- `POST /v1/workspaces/{id}/files/mkdir` → `{}`
- `POST /v1/workspaces/{id}/files/stat` → `{exists, isDirectory, size, mode,
  modTime}`; missing path is `not_found`.
- `POST /v1/workspaces/{id}/files/access` `{want: "read"|"write"}` → `{}` or
  error, used to implement Pi's `access()` operations.
- `POST /v1/workspaces/{id}/files/readdir` → `{entries: ["name", ...]}`
- `POST /v1/workspaces/{id}/files/glob` `{pattern, path?, limit?}` →
  `{paths: [...]}` for Pi's find tool. Walks from `path` (default root),
  ignores `node_modules` and `.git`, matches the glob (`*`, `**`, `?`)
  against the relative path and the basename, and returns relative paths.
- `POST /v1/workspaces/{id}/files/grep`
  `{pattern, path?, glob?, ignoreCase?, literal?, context?, limit?}` →

  ```json
  { "matches": [ { "path": "src/file.ts", "lineNumber": 3,
                   "lineText": "...",
                   "lines": [ {"n":2,"text":"...","match":false},
                              {"n":3,"text":"...","match":true} ] } ],
    "limitReached": false }
  ```

  Server-side search returns matches in walk order; `lines` carries expanded
  context (empty when `context` is 0 or omitted) so the extension can format
  output exactly like Pi's built-in grep (`path:line: text` for matches,
  `path-line- text` for context, plus truncation notices).

### M1 simplifications (accepted)

- Server-side grep uses Go's RE2 regexp (no backreferences/lookaround),
  walks the tree ignoring `.git`/`node_modules`/`.DS_Store`, and does not yet
  respect `.gitignore` or other ripgrep niceties. Fidelity improves later if
  agent workflows demand it.
- exec uses the provider-defined minimal environment plus explicit request
  overrides; daemon credentials and unrelated variables are absent (ADR 0004).
- Path validation cleans lexically and evaluates symlinks of existing
  components; a symlink created mid-session inside the workspace pointing out
  of it is accepted M1 risk on the local provider.
- No auth on `/v1/info` (name/version only); everything else is gated when a
  token is configured.

### Path-contract migration

The original M1 contract returned directory-provider host paths. The migration
is intentionally fail-closed rather than dual-mode:

- Existing workspace rows require no database migration because host roots were
  derived at read time and never persisted.
- Updated daemons return `/workspace` and `workspace-relative-v1` for both new
  and existing workspaces.
- Updated extensions require that exact pair before attaching. Missing or
  mismatched values produce an explicit compatibility error instructing the
  user to upgrade daemon and extension together.
- Updated daemons reject all absolute request paths. They do not guess whether
  an absolute path is a legacy host path or a virtual path, preventing silent
  routing to the wrong provider location.

This is a versioned resource-level contract while HTTP routes remain under
`/v1`; a future incompatible path model must use a new `pathContract` value and
explicit client support.

## Consequences

- The extension implements Pi's `BashOperations`, `ReadOperations`,
  `WriteOperations`, `EditOperations`, `LsOperations`, and `FindOperations`
  against this API, and registers a remote-backed `grep` tool (Pi's grep tool
  spawns local ripgrep, so it cannot be redirected through operations and is
  reimplemented with matching output format).
- Ordinary Pi starts locally. `/scrap` synchronously replaces all seven
  project-facing tools before workspace acquisition; acquisition failure leaves
  those overrides disconnected and fail-closed per ADR 0001.
- Aborting a running command is one HTTP connection close; no separate cancel
  endpoint is needed in M1.
- The API is intentionally transport-agnostic about where scrapd runs: moving
  from the local provider to a remote host changes only `SCRAP_DAEMON_URL`.

## Follow-up work

1. `.gitignore`-aware search (M2, likely via ripgrep on the host).
2. Scoped credential brokering (the minimal exec environment is implemented;
   see ADR 0004).
3. Sleeping/automatic-idle lifecycle policy (M4/M6); explicit start/stop is implemented.
4. Add provider-specific enforcement that `/workspace` is the real mounted
   root rather than directory-provider emulation.
