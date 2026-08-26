package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/peelar/scraps/internal/client"
)

func newClientFromEnv() *client.Client {
	url := os.Getenv("SCRAP_DAEMON_URL")
	if url == "" {
		url = "http://127.0.0.1:8484"
	}
	return client.New(url, os.Getenv("SCRAP_TOKEN"))
}

func runList(_ []string) int {
	ensureDaemonAuto()
	api := newClientFromEnv()
	workspaces, err := api.ListWorkspaces(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}
	if len(workspaces) == 0 {
		fmt.Println("no workspaces — start one with: scrap pi")
		return 0
	}

	writer := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tSTATE\tPROJECT\tREPO\tCREATED")
	for _, w := range workspaces {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			w.ID, w.State, w.Project, w.RepoURL, w.CreatedAt.Local().Format(time.DateTime))
	}
	if err := writer.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}
	return 0
}

func runWorkspaceState(command string, args []string) int {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: scrap %s <workspace-id>\n", command)
		return 2
	}
	ensureDaemonAuto()
	api := newClientFromEnv()
	var err error
	if command == "start" {
		_, err = api.StartWorkspace(context.Background(), args[0])
	} else {
		_, err = api.StopWorkspace(context.Background(), args[0])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %s %s: %v\n", command, args[0], err)
		return 1
	}
	verb := "stopped"
	if command == "start" {
		verb = "started"
	}
	fmt.Printf("%s %s\n", verb, args[0])
	return 0
}

func runRemove(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: scrap rm <workspace-id>...")
		return 2
	}

	api := newClientFromEnv()
	ensureDaemonAuto()
	failed := false
	for _, id := range args {
		if err := api.DeleteWorkspace(context.Background(), id); err != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "scrap: remove %s: %v\n", id, err)
			continue
		}
		fmt.Printf("removed %s\n", id)
	}
	if failed {
		return 1
	}
	return 0
}
