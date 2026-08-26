package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/peelar/scraps/internal/githubauth"
	"golang.org/x/term"
)

const authUsage = `usage: scrap auth github [--from-gh | --token-stdin]

Configure a fine-grained GitHub PAT in OpenShell's credential broker. The PAT
is never placed in a sandbox or command argument. New Scraps sandboxes attach
the provider automatically; existing Scraps sandboxes are updated when possible.

The token should be limited to selected repositories and have only:
  Repository permissions > Contents: Read and write

Options:
  --from-gh      Read the active token from the host gh CLI/keyring
  --token-stdin  Read the token from stdin (useful for a password manager)

With neither option, scrap prompts without echo when stdin is a terminal.
`

func runAuth(args []string) int {
	if len(args) == 0 || args[0] != "github" {
		fmt.Fprint(os.Stderr, authUsage)
		return 2
	}
	flags := flag.NewFlagSet("auth github", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(flags.Output(), authUsage) }
	fromGH := flags.Bool("from-gh", false, "read token from gh auth token")
	tokenStdin := flags.Bool("token-stdin", false, "read token from stdin")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || (*fromGH && *tokenStdin) {
		fmt.Fprint(os.Stderr, authUsage)
		return 2
	}

	token, err := readGitHubToken(*fromGH, *tokenStdin)
	if err != nil {
		return authError(err)
	}
	defer clearBytes(token)
	if !strings.HasPrefix(string(token), "github_pat_") {
		fmt.Fprintln(os.Stderr, "scrap auth: warning: this does not look like a fine-grained PAT; repository restrictions must be enforced by the token itself")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if workerVMMarkerExists() {
		if err := configureGitHubProviderInWorker(ctx, token); err != nil {
			return authError(err)
		}
		return 0
	}
	if err := configureGitHubProvider(ctx, token); err != nil {
		return authError(err)
	}
	attached, warnings := attachGitHubProvider(ctx)
	fmt.Println("✓ GitHub push credential stored in OpenShell — sandbox commands cannot read the PAT")
	fmt.Println("  allowed: GitHub reads, Git fetch/clone, and Git push over HTTPS")
	fmt.Println("  blocked: GitHub API mutations, workflow dispatch, and repository administration")
	if attached > 0 {
		fmt.Printf("  attached to %d existing Scraps workspace(s); new workspaces attach automatically\n", attached)
	} else {
		fmt.Println("  new Scraps workspaces will attach the provider automatically")
	}
	for _, warning := range warnings {
		fmt.Fprintf(os.Stderr, "scrap auth: warning: %s\n", warning)
	}
	return 0
}

func readGitHubToken(fromGH, tokenStdin bool) ([]byte, error) {
	if fromGH {
		output, err := exec.Command("gh", "auth", "token").Output()
		if err != nil {
			return nil, fmt.Errorf("read host gh credential: %w", err)
		}
		return normalizeToken(output)
	}
	if tokenStdin || !term.IsTerminal(int(os.Stdin.Fd())) {
		value, err := io.ReadAll(io.LimitReader(os.Stdin, 16*1024))
		if err != nil {
			return nil, fmt.Errorf("read PAT: %w", err)
		}
		return normalizeToken(value)
	}
	fmt.Fprint(os.Stderr, "Fine-grained GitHub PAT: ")
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("read PAT: %w", err)
	}
	return normalizeToken(value)
}

func normalizeToken(value []byte) ([]byte, error) {
	value = []byte(strings.TrimSpace(string(value)))
	if len(value) == 0 {
		return nil, errors.New("GitHub PAT is empty")
	}
	if strings.ContainsAny(string(value), "\r\n\x00") {
		return nil, errors.New("GitHub PAT contains invalid characters")
	}
	return value, nil
}

func configureGitHubProviderInWorker(ctx context.Context, token []byte) error {
	if err := requireCommand("limactl"); err != nil {
		return err
	}
	vmName := os.Getenv("SCRAPS_VM_NAME")
	if vmName == "" {
		vmName = "scraps"
	}
	cmd := exec.CommandContext(ctx, "limactl", "shell", vmName, "scrap", "auth", "github", "--token-stdin")
	cmd.Stdin = strings.NewReader(string(token) + "\n")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("configure credential in worker VM: %w", err)
	}
	return nil
}

func configureGitHubProvider(ctx context.Context, token []byte) error {
	if err := requireCommand("openshell"); err != nil {
		return err
	}
	if err := runAuthCommand(ctx, nil, "status"); err != nil {
		return fmt.Errorf("OpenShell gateway is not ready: %w", err)
	}
	dir, err := os.MkdirTemp("", "scrap-github-profile-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	profilePath := filepath.Join(dir, "github-push.yaml")
	if err := os.WriteFile(profilePath, githubauth.Profile, 0o600); err != nil {
		return err
	}

	if err := runAuthCommand(ctx, nil, "provider", "profile", "export", githubauth.ProfileID, "--output", "json"); err == nil {
		if err := runAuthCommand(ctx, nil, "provider", "profile", "update", githubauth.ProfileID, "--file", profilePath); err != nil {
			return fmt.Errorf("update GitHub provider profile: %w", err)
		}
	} else if err := runAuthCommand(ctx, nil, "provider", "profile", "import", "--file", profilePath); err != nil {
		return fmt.Errorf("import GitHub provider profile: %w", err)
	}

	tokenEnv := []string{githubauth.TokenEnvironment + "=" + string(token)}
	if err := runAuthCommand(ctx, nil, "provider", "get", githubauth.ProfileID); err == nil {
		if err := runAuthCommand(ctx, tokenEnv, "provider", "update", githubauth.ProfileID, "--from-existing"); err != nil {
			return fmt.Errorf("update GitHub credential: %w", err)
		}
	} else if err := runAuthCommand(ctx, tokenEnv, "provider", "create", "--name", githubauth.ProfileID, "--type", githubauth.ProfileID, "--from-existing"); err != nil {
		return fmt.Errorf("create GitHub credential: %w", err)
	}
	return nil
}

func runAuthCommand(ctx context.Context, extraEnv []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "openshell", args...)
	cmd.Env = credentialEnvironment(extraEnv)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, message)
}

func credentialEnvironment(extra []string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "GH_TOKEN" && key != "GITHUB_TOKEN" {
			env = append(env, entry)
		}
	}
	return append(env, extra...)
}

func attachGitHubProvider(ctx context.Context) (int, []string) {
	output, err := exec.CommandContext(ctx, "openshell", "sandbox", "list", "--output", "json").Output()
	if err != nil {
		return 0, []string{"could not list existing OpenShell sandboxes"}
	}
	var sandboxes []struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(output, &sandboxes); err != nil {
		return 0, []string{"could not decode existing OpenShell sandboxes"}
	}
	attached := 0
	var warnings []string
	for _, sandbox := range sandboxes {
		if sandbox.Labels["dev.scraps.workspace"] == "" {
			continue
		}
		listed, err := exec.CommandContext(ctx, "openshell", "sandbox", "provider", "list", sandbox.Name).CombinedOutput()
		if err == nil && strings.Contains(string(listed), githubauth.ProfileID) {
			continue
		}
		if err := runAuthCommand(ctx, nil, "sandbox", "provider", "attach", sandbox.Name, githubauth.ProfileID); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not attach provider to %s: %v", sandbox.Name, err))
			continue
		}
		attached++
	}
	return attached, warnings
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func authError(err error) int {
	fmt.Fprintf(os.Stderr, "scrap auth: %v\n", err)
	return 1
}
