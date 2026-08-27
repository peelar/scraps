---
name: scraps-workspaces
description: Working inside Scraps remote workspaces in Pi — especially opening workspace services in a browser with `scrap open` port tunneling, workspace lifecycle (/scrap, /scrap-select, /scrap toss), and troubleshooting connectivity. Use when a dev server or port is started inside a Scraps workspace, when the user asks how to view/preview/open something running remotely, or mentions scraps, /scrap, scrapd, or scrap CLI commands.
---

# Scraps workspaces

When `/scrap` is active, Pi's project tools (bash, read, write, edit, ls,
find, grep) run inside a disposable container on a remote worker VM. Files and
processes live there, not on the local machine — so anything you start (dev
server, notebook, database UI) is listening on the *workspace's* network, and
the user cannot reach it by browsing localhost. That's what `scrap open` is
for.

## Opening a workspace service in the browser

`scrap open` tunnels a workspace port onto the user's local machine and opens
the browser. It runs **locally** (in the user's terminal), not inside the
workspace — tell the user to run it, don't try to run it yourself with the
bash tool (that runs remotely).

```
scrap open                 # auto-detect the listening port in the attached workspace
scrap open 3000            # explicit remote port
scrap open --no-browser    # print the local URL without opening a browser
scrap open --local 8080 3000   # choose the local port too
```

Auto-detection lists what the workspace is actually listening on; when
several ports are live it prints a picker. If nothing is listening it says so
— the service must be started first (e.g. `npm run dev` via the bash tool).

**When to bring this up:** any time you start or the user mentions a server,
preview, dashboard, web UI, or "can I see it" while a Scraps workspace is
attached. The reliable recipe:

1. Start the service in the workspace with the bash tool; note the port.
2. Tell the user: `scrap open <port>` in a local terminal (Ctrl-C to close).

## Workspace lifecycle

- `/scrap [project]` — create a workspace and switch Pi's tools to it
- `/scrap-select ID` — attach an existing one
- `/scrap toss` — permanently delete it and return tools to local
- CLI: `scrap ls`, `scrap start/stop ID`, `scrap rm ID`

## Moving directories and workspaces

Local files are never synced automatically. To move a local directory into a
workspace, or workspace files back to the machine, use the one-shot archive
transfer (ADR 0014) in a local terminal:

- `scrap push [<workspace-id>] <dir>` — copy a local directory into an empty
  workspace (literal copy, `.git` included). Add `--replace` to clear the
  workspace first.
- `scrap pull [<workspace-id>] [target]` — copy the workspace into a fresh
  local directory. Add `--force` to overlay an existing directory.

`/scrap` in a non-git directory offers the same copy at creation time.
Without a repository, this is the way to seed a workspace and to get agent
work back out (the other exit is pushing to a git remote).

## Missing environment variables

Scraps deliberately does not copy Pi's ordinary local environment into a
workspace. The active system prompt lists the environment-variable names that
were explicitly approved and loaded, plus approvals whose values were missing
when Pi started.

When software explicitly reports that a variable such as `DATABASE_URL` is
missing:

1. Explain that Scraps isolates local environment variables by default.
2. Tell the user to run this in a **local terminal**:

   ```bash
   scrap env allow DATABASE_URL
   ```

3. Tell them to restart Pi with the variable supplied by their existing secret
   manager, for example `op run -- pi`, `doppler run -- pi`, or
   `infisical run -- pi`.

The bash tool runs inside the workspace, so do not attempt to run `scrap env`
for the user. Never ask them to paste a secret value into chat. Recommend only
the variable explicitly required by the software, and prefer a broker such as
`scrap auth github` whenever Scraps supports one.

## Connectivity and setup pointers

- `scrap status` (local terminal) shows whether the daemon is reachable.
- No client profile yet (`~/.config/scraps/client.json` missing) or worker
  moved: `scrap attach` discovers a worker on the tailnet and configures it;
  fresh installs get this from the setup script.
- Tools failing closed is by design — they never fall back to the local
  machine. Fix the connection rather than working around it.
- Deep reference: docs/homelab-nuc.md in the Scraps repository.
