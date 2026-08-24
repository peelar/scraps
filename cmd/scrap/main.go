package main

import (
	"fmt"
	"os"

	"github.com/peelar/scraps/internal/version"
)

const usage = `scrap controls self-hosted agent workspaces.

Usage:
  scrap <command>

Commands:
  setup       Configure self-hosted infrastructure
  auth        Configure credentials
  pi          Start Pi in a fresh workspace
  ls          List workspaces
  attach      Attach to a workspace
  ssh         Open a workspace shell
  open        Open a project preview
  diff        Show workspace changes
  sync        Sync changes to the local checkout
  rm          Remove a workspace
  version     Print version information
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(usage)
		return 0
	}

	if args[0] == "version" || args[0] == "--version" || args[0] == "-v" {
		fmt.Println(version.String("scrap"))
		return 0
	}

	fmt.Fprintf(os.Stderr, "scrap: command %q is not implemented yet\n", args[0])
	return 1
}
