package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

const newUsage = `usage: scrap new [--repo URL] [project]

Create a workspace, optionally cloning a repository. SSH and scp-style Git
origins are normalized to HTTPS by scrapd. Private GitHub repositories require
one-time setup with: scrap auth github
`

func runNew(args []string) int {
	flags := flag.NewFlagSet("new", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), newUsage) }
	repoURL := flags.String("repo", "", "repository URL to clone")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 1 {
		flags.Usage()
		return 2
	}
	project := ""
	if flags.NArg() == 1 {
		project = flags.Arg(0)
	}

	created, err := newClientFromEnv().CreateWorkspace(context.Background(), project, *repoURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: create workspace: %v\n", err)
		return 1
	}
	fmt.Printf("created %s", created.ID)
	if created.RepoURL != "" {
		fmt.Printf(" from %s", created.RepoURL)
	}
	fmt.Println()
	return 0
}
