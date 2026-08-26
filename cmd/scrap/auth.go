package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

const authUsage = `usage: scrap auth github [--no-browser]

Create a private, self-hosted GitHub App and install it on selected repositories.
Scraps keeps the App key in the worker control plane and automatically refreshes
short-lived installation tokens. Sandboxes never receive the key or token.

Options:
  --no-browser  Print the authorization URL instead of opening it
`

func runAuth(args []string) int {
	if len(args) == 0 || args[0] != "github" {
		fmt.Fprint(os.Stderr, authUsage)
		return 2
	}
	flags := flag.NewFlagSet("auth github", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), authUsage) }
	noBrowser := flags.Bool("no-browser", false, "print authorization URL")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprint(os.Stderr, authUsage)
		return 2
	}

	api := newClientFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	started, err := api.StartGitHubAuth(ctx)
	if err != nil {
		return authError(err)
	}
	if *noBrowser {
		fmt.Printf("Open this URL to grant repository access:\n%s\n", started.BrowserURL)
	} else if err := openBrowser(started.BrowserURL); err != nil {
		fmt.Fprintf(os.Stderr, "scrap auth: could not open browser: %v\n", err)
		fmt.Printf("Open this URL to grant repository access:\n%s\n", started.BrowserURL)
	} else {
		fmt.Println("Opening GitHub — choose the repositories Scraps may access…")
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastState := ""
	for {
		select {
		case <-ctx.Done():
			return authError(errors.New("timed out waiting for GitHub authorization"))
		case <-ticker.C:
			status, err := api.GitHubAuthStatus(ctx, started.State)
			if err != nil {
				return authError(err)
			}
			if status.State != lastState {
				switch status.State {
				case "waiting_for_installation":
					fmt.Println("GitHub App created — choose its owner and repositories in the browser…")
				case "configuring":
					fmt.Println("Installation received — configuring secure repository access…")
				}
				lastState = status.State
			}
			switch status.State {
			case "complete":
				fmt.Println("✓ GitHub App installed — repository access is ready")
				fmt.Println("  installation tokens refresh automatically and are never exposed to sandboxes")
				fmt.Println("  allowed: repository reads and Git fetch/push over HTTPS")
				fmt.Println("  blocked: workflows, administration, and unrelated GitHub API mutations")
				return 0
			case "error":
				return authError(errors.New(status.Error))
			}
		}
	}
}

func openBrowser(target string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{target}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		command, args = "xdg-open", []string{target}
	}
	return exec.Command(command, args...).Start()
}

func authError(err error) int {
	fmt.Fprintf(os.Stderr, "scrap auth: %v\n", err)
	return 1
}
