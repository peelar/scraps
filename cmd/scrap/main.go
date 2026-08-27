package main

import (
	"fmt"
	"os"

	"github.com/peelar/scraps/internal/version"
)

const usage = `scrap controls self-hosted agent workspaces.

Usage:
  scrap <command> [arguments]

Commands:
  new [--repo URL] [project]
                Create a workspace, optionally cloning a repository
  push [--replace] [<id>] <dir>
                Copy a local directory into a workspace (ADR 0014)
  pull [--force] [<id>] [target]
                Copy a workspace into a local directory (ADR 0014)
  ls            List workspaces
  start <id>    Start a stopped workspace
  stop <id>     Stop a running workspace
  rm <id>...    Remove workspaces
  open [id] [port]
                Tunnel a workspace port to localhost and open it
  attach [user@]worker
                Discover a tailnet Scraps worker and configure this computer
  status        Show daemon and workspace status
  configure     Configure local worker VM sizing
  auth github   Grant repositories to a self-hosted GitHub App
  env           Manage explicit environment-variable approvals
  version       Print version information

Environment:
  SCRAP_DAEMON_URL   scrapd base URL (default http://127.0.0.1:8484)
  SCRAP_TOKEN        scrapd bearer token when the daemon requires one
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(usage)
		return 0
	}

	command, rest := args[0], args[1:]
	switch command {
	case "version", "--version", "-v":
		fmt.Println(version.String("scrap"))
		return 0
	case "new":
		return runNew(rest)
	case "push":
		return runPush(rest)
	case "pull":
		return runPull(rest)
	case "ls":
		return runList(rest)
	case "start", "stop":
		return runWorkspaceState(command, rest)
	case "rm":
		return runRemove(rest)
	case "open":
		return runOpen(rest)
	case "attach":
		return runAttach(rest)
	case "configure":
		return runConfigure(rest)
	case "_worker-setup":
		return runSetup(rest)
	case "auth":
		return runAuth(rest)
	case "env":
		return runEnv(rest)
	case "status":
		return runStatus(rest)
	default:
		fmt.Fprintf(os.Stderr, "scrap: unknown command %q\n", command)
		return 1
	}
}
