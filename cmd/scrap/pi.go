package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/peelar/scraps/internal/client"
	"github.com/peelar/scraps/internal/extension"
	"github.com/peelar/scraps/internal/version"
	"github.com/peelar/scraps/internal/workspace"
)

const piUsage = `usage: scrap pi [flags] [prompt]

Start Pi with the Scraps extension attached to a workspace.

  --workspace <id>   Attach to an existing workspace instead of creating one
  --repo <url>       Repository to clone into a new workspace (http/https)
  --project <name>   Project label for a new workspace
                     (default: repository name or current directory name)
  --url <url>        scrapd base URL (default $SCRAP_DAEMON_URL or
                     http://127.0.0.1:8484)

The daemon token is taken from $SCRAP_TOKEN.
`

func runPi(args []string) int {
	flags := flag.NewFlagSet("pi", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var (
		workspaceID = flags.String("workspace", "", "workspace to attach to")
		repoURL     = flags.String("repo", "", "repository to clone")
		project     = flags.String("project", "", "project label")
		daemonURL   = flags.String("url", "", "scrapd base URL")
	)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))

	url := *daemonURL
	if url == "" {
		url = os.Getenv("SCRAP_DAEMON_URL")
	}
	if url == "" {
		url = "http://127.0.0.1:8484"
	}
	api := client.New(url, os.Getenv("SCRAP_TOKEN"))

	ensureDaemonAuto()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if err := api.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "scrap: cannot reach scrapd at %s\n", url)
		fmt.Fprintln(os.Stderr, "start it with: make dev-daemon  # or scrapd directly")
		return 1
	}

	target, err := resolveWorkspace(ctx, api, *workspaceID, *repoURL, *project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}

	extensionPath, err := extensionPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: %v\n", err)
		return 1
	}

	pi, err := exec.LookPath("pi")
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrap: pi executable not found in PATH\n")
		return 1
	}

	fmt.Fprintf(os.Stderr, "scrap: workspace %s at %s\n", target.ID, url)

	arguments := []string{
		"--scrap",
		"--workspace", target.ID,
		"--no-builtin-tools",
		"--extension", extensionPath,
	}
	if prompt != "" {
		arguments = append(arguments, "--", prompt)
	}

	child := exec.Command(pi, arguments...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Env = append(os.Environ(),
		"SCRAP_DAEMON_URL="+url,
		"SCRAP_WORKSPACE_ID="+target.ID,
		"SCRAP_PROJECT="+target.Project,
	)
	if token := os.Getenv("SCRAP_TOKEN"); token != "" {
		child.Env = append(child.Env, "SCRAP_TOKEN="+token)
	}

	// Let pi own Ctrl+C handling; scrap only waits.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		for range signals {
		}
	}()

	runError := child.Run()
	signal.Stop(signals)

	fmt.Fprintf(os.Stderr, "scrap: workspace %s kept — scrap ls, scrap attach later, or scrap rm %s\n", target.ID, target.ID)
	if runError != nil {
		if exitError, ok := runError.(*exec.ExitError); ok {
			return exitError.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "scrap: pi: %v\n", runError)
		return 1
	}
	return 0
}

func resolveWorkspace(ctx context.Context, api *client.Client, workspaceID, repoURL, project string) (workspace.Workspace, error) {
	if workspaceID != "" {
		found, err := api.GetWorkspace(ctx, workspaceID)
		if err != nil {
			return found, fmt.Errorf("attach to workspace: %w", err)
		}
		return found, nil
	}

	if project == "" {
		project = defaultProject(repoURL)
	}
	created, err := api.CreateWorkspace(ctx, project, repoURL)
	if err != nil {
		return created, fmt.Errorf("create workspace: %w", err)
	}
	return created, nil
}

func defaultProject(repoURL string) string {
	if repoURL != "" {
		trimmed := strings.TrimSuffix(strings.TrimRight(repoURL, "/"), ".git")
		parts := strings.Split(trimmed, "/")
		if name := parts[len(parts)-1]; name != "" {
			return name
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "workspace"
	}
	base := filepath.Base(cwd)
	if base == "" || base == "." || base == "/" {
		return "workspace"
	}
	return base
}

// extensionPath returns the extension entry point, preferring an explicit
// override for development and otherwise extracting the embedded copy.
func extensionPath() (string, error) {
	if override := os.Getenv("SCRAP_EXTENSION_PATH"); override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("SCRAP_EXTENSION_PATH: %w", err)
		}
		return override, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	dir := filepath.Join(home, ".scrap", "extension", version.Version+"-"+version.Commit)
	entry, err := extension.Install(dir)
	if err != nil {
		return "", fmt.Errorf("install extension: %w", err)
	}
	return entry, nil
}

// daemonURLFromEnv resolves the daemon base URL.
func daemonURLFromEnv() string {
	url := os.Getenv("SCRAP_DAEMON_URL")
	if url == "" {
		return "http://127.0.0.1:8484"
	}
	return url
}
