package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	openShellVersion = "v0.0.113"
	defaultImage     = "scraps-dev:bookworm"
)

//go:embed files/workspace.Dockerfile
var workspaceDockerfile []byte

const setupUsage = `usage: scrap setup [--image IMAGE]

Install and start the pinned OpenShell release and build Scraps' workspace
image. This is an idempotent, local-machine operation. It does not start
scrapd; follow it with ` + "`scrap up`" + `.

Set SCRAPD_PROVIDER=docker to prepare only the direct Docker backend.
Directory mode requires no setup.
`

func runSetup(args []string) int {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), setupUsage) }
	image := flags.String("image", imageFromEnv(), "workspace image name")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprint(os.Stderr, setupUsage)
		return 2
	}

	provider := os.Getenv("SCRAPD_PROVIDER")
	if provider == "" {
		provider = "openshell"
	}
	if provider == "directory" {
		fmt.Println("✓ directory provider needs no infrastructure setup (it is not isolated)")
		return 0
	}
	if provider != "openshell" && provider != "docker" {
		fmt.Fprintf(os.Stderr, "scrap: unsupported provider %q\n", provider)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := requireCommand("docker"); err != nil {
		return setupError(err)
	}
	if err := runQuietCommand(ctx, "docker", "info"); err != nil {
		return setupError(fmt.Errorf("Docker is not ready: %w", err))
	}
	if provider == "openshell" {
		if err := ensureOpenShell(ctx); err != nil {
			return setupError(err)
		}
	}
	if err := buildWorkspaceImage(ctx, *image); err != nil {
		return setupError(err)
	}
	fmt.Printf("✓ workspace image ready — %s\n", *image)
	fmt.Println("Next: scrap up")
	return 0
}

func imageFromEnv() string {
	if image := os.Getenv("SCRAPD_OPENSHELL_IMAGE"); image != "" {
		return image
	}
	if image := os.Getenv("SCRAPD_DOCKER_IMAGE"); image != "" {
		return image
	}
	return defaultImage
}

func ensureOpenShell(ctx context.Context) error {
	installed := ""
	if path, err := exec.LookPath("openshell"); err == nil {
		out, _ := exec.CommandContext(ctx, path, "--version").Output()
		fields := strings.Fields(string(out))
		if len(fields) >= 2 {
			installed = fields[1]
		}
	}
	expected := strings.TrimPrefix(openShellVersion, "v")
	if installed != expected {
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			return fmt.Errorf("OpenShell automatic installation is unsupported on %s; install %s manually", runtime.GOOS, openShellVersion)
		}
		fmt.Printf("Installing OpenShell %s (found %s)...\n", openShellVersion, valueOr(installed, "none"))
		url := fmt.Sprintf("https://raw.githubusercontent.com/NVIDIA/OpenShell/%s/install.sh", openShellVersion)
		cmd := exec.CommandContext(ctx, "sh", "-c", `curl --proto '=https' --tlsv1.2 -LsSf "$1" | sh`, "scrap-openshell-installer", url)
		cmd.Env = append(os.Environ(), "OPENSHELL_VERSION="+openShellVersion)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("install OpenShell %s: %w", openShellVersion, err)
		}
	}
	if err := requireCommand("openshell"); err != nil {
		return err
	}
	if err := runQuietCommand(ctx, "openshell", "status"); err != nil {
		return fmt.Errorf("OpenShell gateway is not ready: %w", err)
	}
	fmt.Printf("✓ OpenShell gateway ready — %s\n", openShellVersion)
	return nil
}

func buildWorkspaceImage(ctx context.Context, image string) error {
	dir, err := os.MkdirTemp("", "scrap-image-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(file, workspaceDockerfile, 0o600); err != nil {
		return err
	}
	if err := runSetupCommand(ctx, "docker", "build", "--pull", "-t", image, dir); err != nil {
		return fmt.Errorf("build workspace image: %w", err)
	}
	return nil
}

func requireCommand(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s is required but was not found in PATH", name)
	}
	return nil
}

func runSetupCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func runQuietCommand(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return fmt.Errorf("%w: %s", err, message)
		}
	}
	return err
}

func setupError(err error) int {
	fmt.Fprintf(os.Stderr, "scrap setup: %v\n", err)
	return 1
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
