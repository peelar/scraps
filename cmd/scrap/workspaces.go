package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/peelar/scraps/internal/client"
	"github.com/peelar/scraps/internal/workspace"
)

func newClientFromEnv() *client.Client {
	configured := resolvedClientConfig()
	return client.New(configured.DaemonURL, configured.Token)
}

func runList(_ []string) int {
	api := newClientFromEnv()
	workspaces, err := api.ListWorkspaces(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}
	if len(workspaces) == 0 {
		fmt.Println("no workspaces — open Pi and run: /scrap")
		return 0
	}

	writer := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tSTATE\tPROJECT\tREPO\tPORTS\tCREATED")
	for _, w := range workspaces {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			w.ID, w.State, w.Project, w.RepoURL, workspacePortsColumn(api, w), w.CreatedAt.Local().Format(time.DateTime))
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

// formatPortList renders listening ports as `:5173, :3000` for humans.
func formatPortList(ports []client.PortInfo) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, ":"+strconv.Itoa(p.Port))
	}
	return strings.Join(parts, ", ")
}

// workspacePortsColumn reports a workspace's listening ports, or "-" when
// none are known (stopped workspace or listing failure).
func workspacePortsColumn(api *client.Client, w workspace.Workspace) string {
	if w.State != "running" {
		return "-"
	}
	ports, err := api.WorkspacePorts(context.Background(), w.ID)
	if err != nil || len(ports) == 0 {
		return "-"
	}
	return formatPortList(ports)
}
