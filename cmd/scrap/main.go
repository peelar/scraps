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
  pi [prompt]   Start Pi in a fresh workspace (see scrap pi --help)
  ls            List workspaces
  start <id>    Start a stopped workspace
  stop <id>     Stop a running workspace
  rm <id>...    Remove workspaces
  up            Ensure the local scrapd daemon is running
  down          Stop the local scrapd daemon
  status        Show daemon and workspace status
  setup         Install/check OpenShell and build the workspace image
  auth          Configure credentials (not implemented)
  attach        Attach to a workspace (not implemented)
  ssh           Open a workspace shell (not implemented)
  open          Open a project preview (not implemented)
  diff          Show workspace changes (not implemented)
  sync          Sync changes to the local checkout (not implemented)
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
	case "pi":
		return runPi(rest)
	case "ls":
		return runList(rest)
	case "start", "stop":
		return runWorkspaceState(command, rest)
	case "rm":
		return runRemove(rest)
	case "setup":
		return runSetup(rest)
	case "up":
		return runUp(rest)
	case "down":
		return runDown(rest)
	case "status":
		return runStatus(rest)
	default:
		fmt.Fprintf(os.Stderr, "scrap: command %q is not implemented yet\n", command)
		return 1
	}
}
