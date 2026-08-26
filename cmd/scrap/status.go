package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/peelar/scraps/internal/client"
)

func runStatus(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: scrap status")
		return 2
	}
	endpoint := os.Getenv("SCRAP_DAEMON_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8484"
	}
	api := newClientFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := api.Info(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: cannot reach scrapd at %s: %v\n", endpoint, err)
		fmt.Fprintln(os.Stderr, "start the worker VM with: make up")
		return 1
	}
	fmt.Printf("scrapd v%s — %s (pid %d)\n", info.Version, endpoint, info.PID)
	fmt.Printf("  version %s (%s) · up %s · provider %s (%s) · data %s\n",
		info.Version, info.Commit, uptime(info.StartedAt), info.Provider, info.Isolation, info.DataDir)
	if info.Image != "" {
		fmt.Printf("  image %s\n", info.Image)
	}
	policy := info.Policy
	fmt.Printf("  policy env=%s · network=%s · resources=%s · credentials=%s · cleanup=%s\n",
		policy.Environment, policy.Network, policy.Resources, policy.Credentials, policy.ProcessCleanup)
	printWorkspaceSummary(api)
	return 0
}

func uptime(startedAt string) string {
	started, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return "?"
	}
	return time.Since(started).Round(time.Second).String()
}

func printWorkspaceSummary(api *client.Client) {
	workspaces, err := api.ListWorkspaces(context.Background())
	if err != nil {
		return
	}
	switch len(workspaces) {
	case 0:
		fmt.Println("  no workspaces yet — open Pi and run: /scrap")
	case 1:
		fmt.Printf("  1 workspace (%s) — attach in Pi with: /scrap-select %s\n", workspaces[0].ID, workspaces[0].ID)
	default:
		fmt.Printf("  %d workspaces — list with: scrap ls\n", len(workspaces))
	}
}
