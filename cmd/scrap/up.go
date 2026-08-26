package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/peelar/scraps/internal/daemon"
)

const upUsage = `usage: scrap up [--restart]

Ensure a local scrapd is running: start it if stopped, kill and restart a
hung or stale instance (binary newer than the daemon), and report status.
scrap pi and other commands do this automatically; ` + "`scrap up`" + ` is the
explicit, verbose entry point.
`

func runUp(args []string) int {
	flags := flag.NewFlagSet("up", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	restart := flags.Bool("restart", false, "restart even if healthy")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	manager, err := managerFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if !manager.IsLocal() {
		status := manager.Probe(ctx)
		if status.State != daemon.StateRunning {
			fmt.Fprintf(os.Stderr, "scrap: daemon at %s is not reachable (%s); remote daemons are not managed by scrap\n", manager.URL(), status.State)
			return 1
		}
		printStatus(manager, status)
		return 0
	}

	action, status, err := manager.EnsureRunning(ctx, daemon.EnsureOptions{
		RestartStale: true,
		ForceRestart: *restart,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		if status.State == daemon.StateAuthError {
			fmt.Fprintln(os.Stderr, "the daemon requires a token — set SCRAP_TOKEN (and matching SCRAPD_TOKEN on the daemon)")
		}
		return 1
	}

	switch action {
	case daemon.ActionStarted:
		fmt.Printf("✓ started scrapd — %s (pid %d)\n", manager.URL(), status.PID)
		fmt.Printf("  log: %s\n", manager.LogFile())
	case daemon.ActionRestartedStale:
		fmt.Printf("↻ restarted scrapd — %s (pid %d); previous instance ran stale code\n", manager.URL(), status.PID)
	case daemon.ActionRestartedForced:
		fmt.Printf("↻ restarted scrapd — %s (pid %d)\n", manager.URL(), status.PID)
	default:
		fmt.Printf("• scrapd already running — %s\n", manager.URL())
	}
	if status.State == daemon.StateRunning && status.Info != nil {
		fmt.Printf("  version %s (%s) · up %s\n", status.Info.Version, status.Info.Commit, uptime(status.Info.StartedAt))
	}

	printWorkspaceSummary(manager)
	return 0
}

func runDown(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: scrap down")
		return 2
	}
	manager, err := managerFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}
	if !manager.IsLocal() {
		fmt.Fprintln(os.Stderr, "scrap: remote daemons are not managed by scrap")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status := manager.Probe(ctx)
	switch status.State {
	case daemon.StateStopped:
		fmt.Println("scrapd is not running")
		return 0
	case daemon.StateAuthError, daemon.StateForeign:
		fmt.Fprintf(os.Stderr, "scrap: refusing to stop a daemon that is not cooperating (%s)\n", status.State)
		return 1
	}

	pid := status.PID
	if err := manager.Stop(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}
	fmt.Printf("✓ stopped scrapd (pid %d)\n", pid)
	return 0
}

func runStatus(_ []string) int {
	manager, err := managerFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status := manager.Probe(ctx)
	switch status.State {
	case daemon.StateRunning:
		printStatus(manager, status)
		printWorkspaceSummary(manager)
		return 0
	case daemon.StateStopped, daemon.StateHungProcess:
		fmt.Printf("scrapd is %s at %s\n", status.State, manager.URL())
		if manager.IsLocal() {
			fmt.Println("start it with: scrap up")
		}
		return 1
	default:
		fmt.Printf("scrapd at %s: %s (%s)\n", manager.URL(), status.State, status.Detail)
		return 1
	}
}

func printStatus(manager *daemon.Manager, status daemon.Status) {
	fmt.Printf("scrapd %s — %s (pid %d)\n", versionLabel(status), manager.URL(), status.PID)
	if status.Info != nil {
		fmt.Printf("  version %s (%s) · up %s · provider %s (%s) · data %s\n",
			status.Info.Version, status.Info.Commit, uptime(status.Info.StartedAt),
			status.Info.Provider, status.Info.Isolation, status.Info.DataDir)
		policy := status.Info.Policy
		fmt.Printf("  policy env=%s · network=%s · resources=%s · credentials=%s · cleanup=%s\n",
			policy.Environment, policy.Network, policy.Resources,
			policy.Credentials, policy.ProcessCleanup)
	}
	if status.StaleBinary {
		fmt.Println("  ⚠ binary is newer than the running daemon — `scrap up` will restart it")
	}
}

func versionLabel(status daemon.Status) string {
	if status.Info == nil {
		return ""
	}
	return fmt.Sprintf("v%s", status.Info.Version)
}

func uptime(startedAt string) string {
	started, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return "?"
	}
	return time.Since(started).Round(time.Second).String()
}

func printWorkspaceSummary(manager *daemon.Manager) {
	workspaces, err := manager.Client().ListWorkspaces(context.Background())
	if err != nil {
		return
	}
	switch len(workspaces) {
	case 0:
		fmt.Println("  no workspaces yet — start one with: scrap pi")
	case 1:
		fmt.Printf("  1 workspace (%s) — attach with: scrap pi --workspace %s\n", workspaces[0].ID, workspaces[0].ID)
	default:
		fmt.Printf("  %d workspaces — list with: scrap ls\n", len(workspaces))
	}
}

// managerFromEnv builds a supervisor for the configured daemon URL.
func managerFromEnv() (*daemon.Manager, error) {
	return daemon.New(daemon.Options{
		URL:   daemonURLFromEnv(),
		Token: os.Getenv("SCRAP_TOKEN"),
	})
}

// ensureDaemonAuto best-effort starts a stopped local daemon (never
// restarts anything). Called by commands that need the API so `scrap pi`
// works without pre-starting scrapd.
func ensureDaemonAuto() {
	manager, err := managerFromEnv()
	if err != nil || !manager.IsLocal() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	action, _, err := manager.EnsureRunning(ctx, daemon.EnsureOptions{})
	if err == nil && action == daemon.ActionStarted {
		fmt.Fprintf(os.Stderr, "scrap: started scrapd automatically (%s)\n", manager.URL())
	}
}
